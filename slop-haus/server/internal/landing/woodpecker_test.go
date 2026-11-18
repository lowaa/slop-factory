package landing_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/landing"
)

const wpToken = "s3cret-token"

type wpWorkflow struct {
	PID   int64  `json:"pid"`
	Name  string `json:"name"`
	State string `json:"state"`
}

type wpFake struct {
	mu        sync.Mutex
	timeout   int64
	pipelines []map[string]any
	workflows map[int64][]wpWorkflow
	requests  []string
	url       string
	badAuth   bool
	status    int
}

func (w *wpFake) ServeHTTP(rw http.ResponseWriter, r *http.Request) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.requests = append(w.requests, r.Method+" "+r.URL.Path)
	if r.Header.Get("Authorization") != "Bearer "+wpToken {
		w.badAuth = true
		rw.WriteHeader(http.StatusUnauthorized)
		return
	}
	if w.status != 0 {
		rw.WriteHeader(w.status)
		return
	}
	path := r.URL.Path
	switch {
	case r.Method == http.MethodGet && path == "/api/repos/7":
		_ = json.NewEncoder(rw).Encode(map[string]any{"timeout": w.timeout})
	case r.Method == http.MethodGet && path == "/api/repos/7/pipelines":
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		per, _ := strconv.Atoi(r.URL.Query().Get("perPage"))
		lo, hi := min((page-1)*per, len(w.pipelines)), min(page*per, len(w.pipelines))
		_ = json.NewEncoder(rw).Encode(w.pipelines[lo:hi])
	case strings.HasPrefix(path, "/api/repos/7/pipelines/"):
		var n int64
		var commit any
		for _, p := range w.pipelines {
			if strings.HasSuffix(path, "/"+itoa(p["number"].(int64))) {
				n = p["number"].(int64)
				commit = p["commit"]
			}
		}
		if r.Method == http.MethodPost {
			_ = json.NewEncoder(rw).Encode(map[string]any{"number": n + 100})
			return
		}
		_ = json.NewEncoder(rw).Encode(map[string]any{"number": n, "commit": commit, "workflows": w.workflows[n]})
	default:
		rw.WriteHeader(http.StatusNotFound)
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func (w *wpFake) posts() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	var out []string
	for _, r := range w.requests {
		if strings.HasPrefix(r, "POST") {
			out = append(out, r)
		}
	}
	return out
}

func pipe(number int64, status, event, branch, commit string) map[string]any {
	return map[string]any{"number": number, "status": status, "event": event, "branch": branch, "commit": commit,
		"created": int64(1_700_000_000), "finished": int64(1_700_000_600)}
}

func newWP(t *testing.T) (*wpFake, *landing.WoodpeckerPipelines) {
	t.Helper()
	fake := &wpFake{workflows: map[int64][]wpWorkflow{}}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	fake.url = srv.URL
	p, err := landing.NewWoodpeckerPipelines(&landing.WoodpeckerConfig{URL: srv.URL + "/", Token: wpToken, RepoID: 7})
	require.NoError(t, err)
	return fake, p
}

func TestWoodpecker_NewestIsTheHighestNumberOnTheHead(t *testing.T) {
	fake, p := newWP(t)
	fake.pipelines = []map[string]any{
		pipe(12, "success", "push", "main", "other"),
		pipe(11, "running", "pull_request", "b", "ABC"),
		pipe(9, "killed", "pull_request", "b", "abc"),
	}

	got, err := p.Newest(context.Background(), "abc")

	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, int64(11), got.ID)
	assert.Equal(t, landing.PipelineRunning, got.State)
	assert.Equal(t, time.Unix(1_700_000_000, 0), got.Created)
	assert.Equal(t, time.Unix(1_700_000_600, 0), got.Finished)
	assert.False(t, fake.badAuth)
}

func TestWoodpecker_NewestIsNilWithNoPipelineOnTheHead(t *testing.T) {
	fake, p := newWP(t)
	fake.pipelines = []map[string]any{pipe(1, "success", "push", "main", "other")}

	got, err := p.Newest(context.Background(), "abc")

	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestWoodpecker_StatesAreMapped(t *testing.T) {
	for status, want := range map[string]landing.PipelineState{
		"created": landing.PipelinePending, "pending": landing.PipelinePending, "blocked": landing.PipelinePending,
		"running": landing.PipelineRunning, "started": landing.PipelineRunning,
		"success": landing.PipelineSuccess,
		"failure": landing.PipelineFailure, "error": landing.PipelineFailure,
		"killed": landing.PipelineCancelled, "declined": landing.PipelineCancelled, "skipped": landing.PipelineCancelled,
	} {
		t.Run(status, func(t *testing.T) {
			fake, p := newWP(t)
			fake.pipelines = []map[string]any{pipe(1, status, "push", "b", "abc")}
			got, err := p.Newest(context.Background(), "abc")
			require.NoError(t, err)
			assert.Equal(t, want, got.State)
		})
	}
}

func TestWoodpecker_AnUnknownStatusIsAnError(t *testing.T) {
	fake, p := newWP(t)
	fake.pipelines = []map[string]any{pipe(1, "teleported", "push", "b", "abc")}

	got, err := p.Newest(context.Background(), "abc")

	assert.Error(t, err)
	assert.Nil(t, got)
}

func TestWoodpecker_RepoTimeoutIsInMinutes(t *testing.T) {
	fake, p := newWP(t)
	fake.timeout = 45

	got, err := p.RepoTimeout(context.Background())

	require.NoError(t, err)
	assert.Equal(t, 45*time.Minute, got)
}

func TestWoodpecker_RetryRestartsTheNewestPipelineOnTheHead(t *testing.T) {
	fake, p := newWP(t)
	fake.pipelines = []map[string]any{pipe(11, "failure", "pull_request", "b", "abc"), pipe(9, "failure", "pull_request", "b", "abc")}

	require.NoError(t, p.Retry(context.Background(), "abc"))

	assert.Equal(t, []string{"POST /api/repos/7/pipelines/11"}, fake.posts())
}

// A pipeline that is going or green on the head is a retry already made; a
// second would double it.
func TestWoodpecker_RetryLeavesAPipelineThatIsNotFailedAlone(t *testing.T) {
	for _, status := range []string{"running", "pending", "success"} {
		t.Run(status, func(t *testing.T) {
			fake, p := newWP(t)
			fake.pipelines = []map[string]any{pipe(12, status, "pull_request", "b", "abc"), pipe(11, "failure", "pull_request", "b", "abc")}

			require.NoError(t, p.Retry(context.Background(), "abc"))

			assert.Empty(t, fake.posts())
		})
	}
}

func TestWoodpecker_RestartCancelsALostPipelineThenRunsItAgain(t *testing.T) {
	fake, p := newWP(t)
	fake.pipelines = []map[string]any{pipe(11, "running", "pull_request", "b", "abc")}

	require.NoError(t, p.Restart(context.Background(), "abc", 11))

	assert.Equal(t, []string{"POST /api/repos/7/pipelines/11/cancel", "POST /api/repos/7/pipelines/11"}, fake.posts())
}

func TestWoodpecker_RestartIgnoresPipelinesOnOtherHeads(t *testing.T) {
	fake, p := newWP(t)
	fake.pipelines = []map[string]any{pipe(12, "running", "pull_request", "b", "other"), pipe(11, "running", "pull_request", "b", "abc")}

	require.NoError(t, p.Restart(context.Background(), "abc", 11))

	assert.Equal(t, []string{"POST /api/repos/7/pipelines/11/cancel", "POST /api/repos/7/pipelines/11"}, fake.posts())
}

// A newer pipeline than the lost one is a restart already made.
func TestWoodpecker_RestartLeavesAHeadWithANewerPipelineAlone(t *testing.T) {
	fake, p := newWP(t)
	fake.pipelines = []map[string]any{pipe(12, "running", "pull_request", "b", "abc"), pipe(11, "running", "pull_request", "b", "abc")}

	require.NoError(t, p.Restart(context.Background(), "abc", 11))

	assert.Empty(t, fake.posts())
}

func TestWoodpecker_NewestFindsAPipelineOnALaterPage(t *testing.T) {
	fake, p := newWP(t)
	for n := int64(300); n > 150; n-- {
		fake.pipelines = append(fake.pipelines, pipe(n, "success", "push", "main", "other"))
	}
	fake.pipelines = append(fake.pipelines, pipe(100, "failure", "pull_request", "b", "abc"))

	got, err := p.Newest(context.Background(), "abc")

	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, int64(100), got.ID)
}

func TestWoodpecker_RetryWithNoPipelineIsAnErrorAndPostsNothing(t *testing.T) {
	fake, p := newWP(t)

	assert.Error(t, p.Retry(context.Background(), "abc"))
	assert.Empty(t, fake.posts())
}

func TestWoodpecker_PassedOnBaseReadsTheNewestFinishedPushOnTheBaseBranch(t *testing.T) {
	fake, p := newWP(t)
	fake.pipelines = []map[string]any{
		pipe(20, "running", "push", "main", "m2"),
		pipe(19, "success", "pull_request", "main", "pr"),
		pipe(18, "success", "push", "feature", "f"),
		pipe(17, "failure", "push", "main", "m1"),
	}
	fake.workflows[17] = []wpWorkflow{{Name: "build", State: "success"}, {Name: "lint", State: "failure"}}

	build, err := p.PassedOnBase(context.Background(), []string{"ci/woodpecker/push/build"})
	require.NoError(t, err)
	both, err := p.PassedOnBase(context.Background(), []string{"ci/woodpecker/push/build", "ci/woodpecker/push/lint"})
	require.NoError(t, err)

	assert.True(t, build)
	assert.False(t, both)
}

func TestWoodpecker_PassedOnBaseLooksBackPastPushesThatLackTheWorkflow(t *testing.T) {
	fake, p := newWP(t)
	fake.pipelines = []map[string]any{
		pipe(22, "success", "push", "main", "m3"),
		pipe(21, "success", "push", "main", "m2"),
		pipe(20, "failure", "push", "main", "m1"),
	}
	fake.workflows[22] = []wpWorkflow{{Name: "lint", State: "success"}}
	fake.workflows[21] = []wpWorkflow{{Name: "e2e", State: "success"}, {Name: "lint", State: "success"}}
	fake.workflows[20] = []wpWorkflow{{Name: "e2e", State: "failure"}}

	ok, err := p.PassedOnBase(context.Background(), []string{"ci/woodpecker/pr/e2e"})

	require.NoError(t, err)
	assert.True(t, ok)
}

func TestWoodpecker_PassedOnBaseReadsANightlyCronRunOnTheBaseBranch(t *testing.T) {
	fake, p := newWP(t)
	fake.pipelines = []map[string]any{
		pipe(22, "success", "push", "main", "m3"),
		pipe(21, "success", "cron", "main", "m2"),
	}
	fake.workflows[22] = []wpWorkflow{{Name: "lint", State: "success"}}
	fake.workflows[21] = []wpWorkflow{{Name: "e2e", State: "success"}}

	ok, err := p.PassedOnBase(context.Background(), []string{"ci/woodpecker/pr/e2e"})

	require.NoError(t, err)
	assert.True(t, ok)
}

func TestWoodpecker_PassedOnBaseReportsAFailureOfTheNewestPipelineThatHasTheWorkflow(t *testing.T) {
	fake, p := newWP(t)
	fake.pipelines = []map[string]any{pipe(22, "success", "push", "main", "m3"), pipe(21, "failure", "push", "main", "m2"), pipe(20, "success", "push", "main", "m1")}
	fake.workflows[22] = []wpWorkflow{{Name: "lint", State: "success"}}
	fake.workflows[21] = []wpWorkflow{{Name: "e2e", State: "failure"}}
	fake.workflows[20] = []wpWorkflow{{Name: "e2e", State: "success"}}

	ok, err := p.PassedOnBase(context.Background(), []string{"ci/woodpecker/push/e2e"})

	require.NoError(t, err)
	assert.False(t, ok)
}

func TestWoodpecker_PassedOnBaseLookBackIsBounded(t *testing.T) {
	fake, p := newWP(t)
	for n := int64(205); n >= 1; n-- {
		fake.pipelines = append(fake.pipelines, pipe(n, "success", "push", "main", "m"))
		fake.workflows[n] = []wpWorkflow{{Name: "lint", State: "success"}}
	}
	// Only the oldest, beyond the look-back, has the workflow.
	fake.workflows[1] = []wpWorkflow{{Name: "e2e", State: "success"}}

	ok, err := p.PassedOnBase(context.Background(), []string{"ci/woodpecker/push/e2e"})

	assert.Error(t, err)
	assert.False(t, ok)
}

func TestWoodpecker_PassedOnBaseSkipsAPipelineThatHasOnlySomeOfTheWorkflows(t *testing.T) {
	fake, p := newWP(t)
	fake.pipelines = []map[string]any{pipe(22, "success", "push", "main", "m2"), pipe(21, "success", "push", "main", "m1")}
	fake.workflows[22] = []wpWorkflow{{Name: "e2e", State: "success"}}
	fake.workflows[21] = []wpWorkflow{{Name: "e2e", State: "failure"}, {Name: "kube", State: "success"}}

	ok, err := p.PassedOnBase(context.Background(), []string{"ci/woodpecker/push/e2e", "ci/woodpecker/push/kube"})

	require.NoError(t, err)
	assert.False(t, ok, "judged by 21, the newest with both, not by 22 that lacks kube")
}

func TestWoodpecker_PassedOnBaseLooksBackAcrossPages(t *testing.T) {
	fake, p := newWP(t)
	for n := int64(120); n >= 1; n-- {
		fake.pipelines = append(fake.pipelines, pipe(n, "success", "push", "main", "m"))
		fake.workflows[n] = []wpWorkflow{{Name: "lint", State: "success"}}
	}
	fake.workflows[30] = []wpWorkflow{{Name: "e2e", State: "success"}}

	ok, err := p.PassedOnBase(context.Background(), []string{"ci/woodpecker/push/e2e"})

	require.NoError(t, err)
	assert.True(t, ok)
}

func TestWoodpecker_PassedOnBaseIsAnErrorWhenAPipelineCannotBeRead(t *testing.T) {
	fake, p := newWP(t)
	fake.pipelines = []map[string]any{pipe(22, "success", "push", "main", "m")}
	fake.status = http.StatusBadGateway

	ok, err := p.PassedOnBase(context.Background(), []string{"ci/woodpecker/push/e2e"})

	assert.Error(t, err)
	assert.False(t, ok)
}

func TestWoodpecker_ACheckWithNoWorkflowIsUnknownNotAPass(t *testing.T) {
	fake, p := newWP(t)
	fake.pipelines = []map[string]any{pipe(17, "success", "push", "main", "m1")}
	fake.workflows[17] = []wpWorkflow{{Name: "build", State: "success"}}

	ok, err := p.PassedOnBase(context.Background(), []string{"ci/woodpecker/push/other"})

	assert.Error(t, err)
	assert.False(t, ok)
}

func TestWoodpecker_PassedOnCommitReadsThatCommitsFinishedPipeline(t *testing.T) {
	fake, p := newWP(t)
	fake.pipelines = []map[string]any{pipe(30, "running", "pull_request", "b", "pre"), pipe(28, "success", "pull_request", "b", "pre")}
	fake.workflows[28] = []wpWorkflow{{Name: "build", State: "success"}}

	ok, err := p.PassedOnCommit(context.Background(), "pre", []string{"ci/woodpecker/pr/build"})

	require.NoError(t, err)
	assert.True(t, ok)
}

func TestWoodpecker_AFailedCallIsAnErrorThatNeverCarriesTheToken(t *testing.T) {
	fake, p := newWP(t)
	fake.status = http.StatusInternalServerError

	_, err := p.RepoTimeout(context.Background())

	require.Error(t, err)
	assert.NotContains(t, err.Error(), wpToken)
}

func TestWoodpecker_ARejectedTokenIsAnError(t *testing.T) {
	fake, _ := newWP(t)
	srv := httptest.NewServer(fake)
	defer srv.Close()
	p, err := landing.NewWoodpeckerPipelines(&landing.WoodpeckerConfig{URL: srv.URL, Token: "wrong", RepoID: 7})
	require.NoError(t, err)

	_, err = p.RepoTimeout(context.Background())

	require.Error(t, err)
	assert.NotContains(t, err.Error(), "wrong")
}

func TestNewWoodpeckerPipelines_NeedsURLTokenAndRepo(t *testing.T) {
	for _, c := range []landing.WoodpeckerConfig{{Token: "t", RepoID: 1}, {URL: "http://x", RepoID: 1}, {URL: "http://x", Token: "t"}} {
		_, err := landing.NewWoodpeckerPipelines(&c)
		assert.Error(t, err)
	}
}

func TestWoodpecker_CheckVerdictReadsTheWorkflowTheTargetURLNames(t *testing.T) {
	fake, p := newWP(t)
	fake.pipelines = []map[string]any{pipe(11, "success", "pull_request", "b", "abc")}
	fake.workflows[11] = []wpWorkflow{{PID: 1, Name: "build", State: "success"}, {PID: 2, Name: "lint", State: "failure"}, {PID: 3, Name: "slow", State: "running"}}
	target := func(repo, n, pid int) string {
		return fake.url + "/repos/" + strconv.Itoa(repo) + "/pipeline/" + strconv.Itoa(n) + "/" + strconv.Itoa(pid)
	}

	for _, tc := range []struct {
		name, head, url string
		want            landing.CheckVerdict
		unknown         bool
	}{
		{name: "passed", head: "ABC", url: target(7, 11, 1), want: landing.CIPassed},
		{name: "failed", head: "abc", url: target(7, 11, 2), want: landing.CIFailed},
		{name: "running", head: "abc", url: target(7, 11, 3), want: landing.CIRunning},
		{name: "another repo", head: "abc", url: target(8, 11, 1), unknown: true},
		{name: "another server", head: "abc", url: "https://elsewhere.example/repos/7/pipeline/11/1", unknown: true},
		{name: "another commit", head: "def", url: target(7, 11, 1), unknown: true},
		{name: "no such workflow", head: "abc", url: target(7, 11, 9), unknown: true},
		{name: "not a pipeline url", head: "abc", url: fake.url + "/somewhere", unknown: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := p.CheckVerdict(context.Background(), tc.head, tc.url)
			if tc.unknown {
				assert.ErrorIs(t, err, landing.ErrUnknown)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}
