package landing

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-clock"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/change"
)

// stepClock moves forward by whatever a wait sleeps for, so a stall scenario
// runs in no real time and without a second goroutine to advance it.
type stepClock struct {
	*clock.Fake
}

func (c stepClock) After(d time.Duration) <-chan time.Time {
	c.Advance(d)
	ch := make(chan time.Time, 1)
	ch <- c.Now()
	return ch
}

// scriptedCI answers the checks, the newest pipeline and retries of a stall
// scenario by the time since the wait began.
type scriptedCI struct {
	Pipelines
	Changes
	mu       sync.Mutex
	clk      stepClock
	start    time.Time
	checks   func(since time.Duration) *change.Report
	pipe     func(since time.Duration) *Pipeline
	retries  int
	restarts int
	// restartErr is what asking the CI to restart a lost pipeline answers.
	restartErr error
	// pipeErr is what asking the CI for the newest pipeline fails with.
	pipeErr error
}

func (s *scriptedCI) since() time.Duration { return s.clk.Now().Sub(s.start) }

func (s *scriptedCI) Checks(context.Context, uuid.UUID, uuid.UUID, string) (*change.Report, error) {
	return s.checks(s.since()), nil
}

func (s *scriptedCI) Newest(context.Context, string) (*Pipeline, error) {
	if s.pipeErr != nil {
		return nil, s.pipeErr
	}
	if s.pipe == nil {
		return nil, nil
	}
	return s.pipe(s.since()), nil
}

func (s *scriptedCI) RepoTimeout(context.Context) (time.Duration, error) {
	return 30 * time.Minute, nil
}

func (s *scriptedCI) Restart(context.Context, string, int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.restarts++
	return s.restartErr
}

func (s *scriptedCI) Retry(context.Context, string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.retries++
	return nil
}

func (s *scriptedCI) PassedOnBase(context.Context, []string) (bool, error) { return false, nil }

func (s *scriptedCI) CheckVerdict(context.Context, string, string) (CheckVerdict, error) {
	return "", ErrUnknown
}

type reportAt = func(since time.Duration) *change.Report

func constReport(state string, checks ...change.CheckLine) reportAt {
	return func(time.Duration) *change.Report {
		return &change.Report{HeadSHA: "head", State: state, Checks: checks}
	}
}

// Both callers of the CI wait are driven through the same scenarios: the
// single landing reads the held change's checks, a batch reads its branch's.
func TestCIWait_StallScenariosOfBothCallers(t *testing.T) {
	flaky := change.CheckLine{Name: "ci/flaky", State: "failure", TargetURL: "https://ci/repo/pipeline/1/1"}
	cfg := Config{NeverStartedWait: 5 * time.Minute, NoPipelineWait: 5 * time.Minute, RestartMargin: 10 * time.Minute, PollInterval: time.Minute, KnownFlaky: []string{"flaky"}}
	tests := []struct {
		name   string
		checks reportAt
		pipe   func(time.Duration) *Pipeline
		want   Outcome
		// retries is how many times CI was asked to run the head again.
		retries int
		// restarts is how many lost pipelines were cancelled and run again.
		restarts int
		// within bounds how long the wait took, so a stall ends at its own bound.
		minWait, maxWait time.Duration
	}{
		{name: "green", checks: constReport("success"), minWait: 0, maxWait: time.Minute},
		{name: "no checks ever", checks: constReport("none"), want: ChecksNeverStarted, minWait: 5 * time.Minute, maxWait: 7 * time.Minute},
		{
			name:   "a pending check that never finishes ends at the CI bound",
			checks: constReport("pending", change.CheckLine{Name: "ci/build", State: "pending"}),
			pipe:   func(time.Duration) *Pipeline { return &Pipeline{ID: 1, State: PipelineRunning} },
			want:   ChecksPendingTimeout,
			// An id that never changes is a lost pipeline: restarted once, and the
			// same one still going after the restart's bound is a handback.
			restarts: 1, minWait: 80 * time.Minute, maxWait: 83 * time.Minute,
		},
		{
			name:   "a pending pipeline that never starts is restarted too",
			checks: constReport("pending", change.CheckLine{Name: "ci/build", State: "pending"}),
			pipe:   func(time.Duration) *Pipeline { return &Pipeline{ID: 1, State: PipelinePending} },
			want:   ChecksPendingTimeout, restarts: 1, minWait: 80 * time.Minute, maxWait: 83 * time.Minute,
		},
		{
			name:   "cancelled and not restarted",
			checks: constReport("failure", change.CheckLine{Name: "ci/build", State: "failure"}),
			pipe: func(time.Duration) *Pipeline {
				return &Pipeline{ID: 1, State: PipelineCancelled, Finished: time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC)}
			},
			want: ChecksPendingTimeout,
		},
		{
			name: "red then retried once, green on the new pipeline",
			checks: func(since time.Duration) *change.Report {
				if since < 3*time.Minute {
					return &change.Report{HeadSHA: "head", State: "failure", Checks: []change.CheckLine{flaky}}
				}
				return &change.Report{HeadSHA: "head", State: "success"}
			},
			pipe: func(since time.Duration) *Pipeline {
				if since < 3*time.Minute {
					return &Pipeline{ID: 1, State: PipelineFailure}
				}
				return &Pipeline{ID: 2, State: PipelineSuccess}
			},
			retries: 1,
		},
		{
			name:   "red again after the retry is final",
			checks: constReport("failure", flaky),
			pipe: func(since time.Duration) *Pipeline {
				// The retry is a new pipeline, which fails too.
				if since < 3*time.Minute {
					return &Pipeline{ID: 1, State: PipelineFailure}
				}
				return &Pipeline{ID: 2, State: PipelineFailure}
			},
			want:    ChecksRed,
			retries: 1, minWait: 0, maxWait: time.Hour,
		},
	}
	wirings := []struct {
		name string
		wait func(l *landing) (*change.Report, *failure)
	}{
		{"single", func(l *landing) (*change.Report, *failure) { return l.awaitChecks(context.Background(), "", "", nil) }},
		{"batch", func(l *landing) (*change.Report, *failure) {
			l.read = func(ctx context.Context) (*change.Report, *failure) {
				rep, _ := l.r.o.Changes.Checks(ctx, uuid.Nil, uuid.Nil, "")
				return rep, nil
			}
			return l.awaitChecks(context.Background(), "head", "", nil)
		}},
	}
	for _, w := range wirings {
		for _, tc := range tests {
			t.Run(w.name+"/"+tc.name, func(t *testing.T) {
				fake := clock.NewFake(time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC))
				ci := &scriptedCI{clk: stepClock{fake}, start: fake.Now(), checks: tc.checks, pipe: tc.pipe}
				r := &Runner{o: Options{Pipelines: ci, Changes: ci, Mode: ModeFunc(func(context.Context) (bool, error) { return true, nil })}, cfg: cfg.withDefaults(), clock: ci.clk, log: slog.New(slog.DiscardHandler)}
				l := &landing{r: r, row: &LandingRow{}, rec: &Record{}}

				report, fail := w.wait(l)

				if tc.want == "" {
					require.Nil(t, fail)
					assert.Equal(t, "success", report.State)
				} else {
					require.NotNil(t, fail)
					assert.Equal(t, tc.want, fail.outcome)
				}
				assert.Equal(t, tc.retries, ci.retries)
				assert.Equal(t, tc.restarts, ci.restarts)
				if tc.maxWait > 0 {
					assert.GreaterOrEqual(t, ci.since(), tc.minWait)
					assert.LessOrEqual(t, ci.since(), tc.maxWait)
				}
			})
		}
	}
}

// A runner that starts waiting partway through a pipeline's life (an API
// restart) counts the bound from the pipeline's creation, not from its own start.
func TestCIWait_TheBoundCountsFromThePipelineCreated(t *testing.T) {
	cfg := Config{RestartMargin: 10 * time.Minute, PollInterval: time.Minute}
	for _, tc := range []struct{ age, want time.Duration }{
		{0, 40 * time.Minute},
		{25 * time.Minute, 15 * time.Minute},
		// Past its bound already: still the restart margin, to find a restart.
		{3 * time.Hour, 10 * time.Minute},
		// A creation time ahead of the runner's clock does not extend the wait.
		{-time.Hour, 40 * time.Minute},
	} {
		age := tc.age
		t.Run(age.String(), func(t *testing.T) {
			fake := clock.NewFake(time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC))
			created := fake.Now().Add(-age)
			ci := &scriptedCI{clk: stepClock{fake}, start: fake.Now(), checks: constReport("pending", change.CheckLine{Name: "ci/build", State: "pending"}),
				pipe: func(time.Duration) *Pipeline { return &Pipeline{ID: 1, State: PipelineRunning, Created: created} }}
			r := &Runner{o: Options{Pipelines: ci, Changes: ci, Mode: ModeFunc(func(context.Context) (bool, error) { return true, nil })}, cfg: cfg.withDefaults(), clock: ci.clk, log: slog.New(slog.DiscardHandler)}
			l := &landing{r: r, row: &LandingRow{}, rec: &Record{}}

			l.waiter().restarted = true
			_, fail := l.awaitChecks(context.Background(), "", "", nil)

			require.NotNil(t, fail)
			assert.Equal(t, ChecksPendingTimeout, fail.outcome)
			assert.Equal(t, tc.want, ci.since())
		})
	}
}

// A red check is run again once per landing, not once per wait: merge-main
// waits a second time on the same landing.
func TestCIWait_TheRetryIsOncePerLandingAcrossWaits(t *testing.T) {
	fake := clock.NewFake(time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC))
	flaky := change.CheckLine{Name: "ci/flaky", State: "failure", TargetURL: "https://ci/repo/pipeline/1/1"}
	ci := &scriptedCI{clk: stepClock{fake}, start: fake.Now(), checks: constReport("failure", flaky)}
	// Every pipeline fails, and a new one appears once the head was retried or
	// the landing moved on to its next wait.
	generation := int64(1)
	ci.pipe = func(time.Duration) *Pipeline {
		ci.mu.Lock()
		defer ci.mu.Unlock()
		return &Pipeline{ID: generation + int64(ci.retries), State: PipelineFailure}
	}
	r := &Runner{o: Options{Pipelines: ci, Changes: ci, Mode: ModeFunc(func(context.Context) (bool, error) { return true, nil })},
		cfg: Config{KnownFlaky: []string{"flaky"}}.withDefaults(), clock: ci.clk, log: slog.New(slog.DiscardHandler)}
	l := &landing{r: r, row: &LandingRow{}, rec: &Record{}}

	_, fail := l.awaitChecks(context.Background(), "", "", nil)
	require.NotNil(t, fail)
	require.Equal(t, ChecksRed, fail.outcome)
	require.Equal(t, 1, ci.retries)

	generation = 10
	_, fail = l.awaitChecks(context.Background(), "", "", nil)

	require.NotNil(t, fail)
	assert.Equal(t, ChecksRed, fail.outcome)
	assert.Equal(t, 1, ci.retries, "the second wait does not run the head again")
}

// A head with no checks says why in the hand-back: the CI having no pipeline
// at all (the missing pull_request event) is not a pipeline stuck in created.
func TestCIWait_NeverStartedGuidanceSaysWhatTheCIHasForTheHead(t *testing.T) {
	tests := []struct {
		name    string
		pipe    func(time.Duration) *Pipeline
		pipeErr error
		want    string
		notWant string
	}{
		{name: "no pipeline", want: "Woodpecker has no pipeline for this commit", notWant: "stuck in created"},
		{name: "a pipeline that reports no check", pipe: func(time.Duration) *Pipeline { return &Pipeline{ID: 1, State: PipelinePending} },
			want: "a pipeline exists for this commit", notWant: "has no pipeline"},
		{name: "a host-only tenant has no pipelines to ask", pipeErr: ErrUnknown, want: "could not ask Woodpecker", notWant: "has no pipeline"},
		{name: "the CI cannot be asked", pipeErr: assert.AnError, want: "could not ask Woodpecker", notWant: "has no pipeline"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fake := clock.NewFake(time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC))
			ci := &scriptedCI{clk: stepClock{fake}, start: fake.Now(), checks: constReport("none"), pipe: tc.pipe, pipeErr: tc.pipeErr}
			r := &Runner{o: Options{Pipelines: ci, Changes: ci, Mode: ModeFunc(func(context.Context) (bool, error) { return true, nil })},
				cfg: Config{NeverStartedWait: 5 * time.Minute, PollInterval: time.Minute}.withDefaults(), clock: ci.clk, log: slog.New(slog.DiscardHandler)}
			l := &landing{r: r, row: &LandingRow{}, rec: &Record{}}

			_, fail := l.awaitChecks(context.Background(), "", "", nil)

			require.NotNil(t, fail)
			require.Equal(t, ChecksNeverStarted, fail.outcome)
			assert.Contains(t, fail.guidance, tc.want)
			assert.NotContains(t, fail.guidance, tc.notWant)
		})
	}
}

func lostPipelineLanding(restartErr error) (*landing, *scriptedCI) {
	fake := clock.NewFake(time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC))
	ci := &scriptedCI{clk: stepClock{fake}, start: fake.Now(), restartErr: restartErr,
		checks: constReport("pending", change.CheckLine{Name: "ci/build", State: "pending"}),
		pipe:   func(time.Duration) *Pipeline { return &Pipeline{ID: 1, State: PipelineRunning} }}
	r := &Runner{o: Options{Pipelines: ci, Changes: ci, Mode: ModeFunc(func(context.Context) (bool, error) { return true, nil })},
		cfg: Config{RestartMargin: 10 * time.Minute, PollInterval: time.Minute}.withDefaults(), clock: ci.clk, log: slog.New(slog.DiscardHandler)}
	return &landing{r: r, row: &LandingRow{}, rec: &Record{}}, ci
}

// A re-queued ticket must not wait on the same lost pipeline for another full
// bound: a second wait on the landing hands back without restarting again.
func TestCIWait_ALostPipelineIsRestartedOncePerLandingAcrossWaits(t *testing.T) {
	l, ci := lostPipelineLanding(nil)

	_, fail := l.awaitChecks(context.Background(), "", "", nil)
	require.NotNil(t, fail)
	require.Equal(t, ChecksPendingTimeout, fail.outcome)
	require.Equal(t, 1, ci.restarts)

	before := ci.since()
	_, fail = l.awaitChecks(context.Background(), "", "", nil)

	require.NotNil(t, fail)
	assert.Equal(t, ChecksPendingTimeout, fail.outcome)
	assert.Equal(t, 1, ci.restarts)
	assert.LessOrEqual(t, ci.since()-before, 41*time.Minute, "the second wait is one bound")
}

func TestCIWait_ACIThatCannotRestartHandsBackAtTheFirstBound(t *testing.T) {
	l, ci := lostPipelineLanding(ErrNoRetry)

	_, fail := l.awaitChecks(context.Background(), "", "", nil)

	require.NotNil(t, fail)
	assert.Equal(t, ChecksPendingTimeout, fail.outcome)
	assert.Contains(t, fail.detail, "cannot restart")
	assert.Positive(t, ci.restarts)
	assert.LessOrEqual(t, ci.since(), 42*time.Minute)
}
