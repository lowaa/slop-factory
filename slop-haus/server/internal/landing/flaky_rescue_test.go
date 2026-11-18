package landing_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/landing"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

type flakyRetry struct {
	workflow, result string
	expired          bool
}

type flakyObserver struct {
	recordingObserver
	retries []flakyRetry
}

func (o *flakyObserver) LandingFlakyRetry(workflow, result string, expired bool) {
	o.retries = append(o.retries, flakyRetry{workflow, result, expired})
}

// retryScenario lands a change whose build fails on pipeline 1; the retry's
// pipeline is green when rescued, red otherwise.
func retryScenario(t *testing.T, entry string, rescued bool) (*fixture, *flakyObserver) {
	f := newFixture(t)
	obs := &flakyObserver{}
	f.observer = obs
	c := f.finishedChange(model.PriorityNormal)
	f.setChecks(c.ref, headA, true, red)
	f.pipes.newest = func(string) *landing.Pipeline {
		id := int64(1 + len(f.pipes.retries))
		if id > 1 && rescued {
			return &landing.Pipeline{ID: id, State: landing.PipelineRunning}
		}
		return &landing.Pipeline{ID: id, State: landing.PipelineFailure}
	}
	if rescued {
		f.host.AfterChecks = func() {
			if len(f.pipes.retried()) > 0 {
				f.setChecks(c.ref, headA, true, green)
			}
		}
	}
	f.land(f.runner(landing.Config{KnownFlaky: []string{entry}}))
	return f, obs
}

func TestFlakyRetry_RescuedIsRecorded(t *testing.T) {
	_, obs := retryScenario(t, "build:1566:2999-01-01", true)
	assert.Equal(t, []flakyRetry{{"build", landing.FlakyRescued, false}}, obs.retries)
}

func TestFlakyRetry_ConfirmedRedIsRecorded(t *testing.T) {
	_, obs := retryScenario(t, "build:1566:2999-01-01", false)
	assert.Equal(t, []flakyRetry{{"build", landing.FlakyConfirmed, false}}, obs.retries)
}

func TestFlakyRetry_ABareNameIsStillRetriedAndRecorded(t *testing.T) {
	_, obs := retryScenario(t, "build", true)
	assert.Equal(t, []flakyRetry{{"build", landing.FlakyRescued, false}}, obs.retries)
}

func TestFlakyRetry_AnExpiredEntryIsStillRetriedAndFlagged(t *testing.T) {
	f, obs := retryScenario(t, "build:1566:2000-01-01", true)
	assert.Len(t, f.pipes.retried(), 1)
	assert.Equal(t, []flakyRetry{{"build", landing.FlakyRescued, true}}, obs.retries)
}

func TestFlakyRetry_ARetryForAFailurePassingOnMainIsNotACountedFlake(t *testing.T) {
	f := newFixture(t)
	obs := &flakyObserver{}
	f.observer = obs
	c := f.finishedChange(model.PriorityNormal)
	f.setChecks(c.ref, headA, true, red)
	f.pipes.passedOnBase = true
	f.land(f.runner(landing.Config{}))
	assert.Empty(t, obs.retries)
}

func TestFlakyRetry_AMalformedEntryIsRefusedAtStartup(t *testing.T) {
	for _, bad := range []string{"build:x:2026-11-22", "build:12:soon", "build:12", ":12:2026-11-22"} {
		_, err := landing.New(&landing.Options{Config: landing.Config{KnownFlaky: []string{bad}}})
		require.ErrorContains(t, err, "known flaky entry", bad)
	}
}
