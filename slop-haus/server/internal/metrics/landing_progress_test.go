package metrics_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/metrics"
)

type fakeProgress struct {
	p   metrics.LandingProgress
	err error
}

func (f fakeProgress) LandingProgress(context.Context) (metrics.LandingProgress, error) {
	return f.p, f.err
}

func progressRegistry(t *testing.T, src metrics.LandingSource) *prometheus.Registry {
	t.Helper()
	reg := prometheus.NewPedanticRegistry()
	require.NoError(t, reg.Register(metrics.NewLandingCollector(src, oneTenant{}, slog.NewTextHandler(io.Discard, nil))))
	return reg
}

func TestLandingCollector_StepAgeAgainstItsBoundAndTheQueueSeparately(t *testing.T) {
	reg := progressRegistry(t, fakeProgress{p: metrics.LandingProgress{
		Step: "waiting_checks", StepAge: 5 * time.Minute, StepBound: 75 * time.Minute,
		Queued: 8, OldestWait: 100 * time.Minute,
	}})
	mfs, err := reg.Gather()
	require.NoError(t, err)
	got := map[string]float64{}
	for _, mf := range mfs {
		got[mf.GetName()] = mf.GetMetric()[0].GetGauge().GetValue()
	}
	assert.EqualValues(t, 300, got["slophaus_landing_head_step_seconds"])
	assert.EqualValues(t, 4500, got["slophaus_landing_head_step_bound_seconds"])
	assert.EqualValues(t, 8, got["slophaus_landing_queue_length"])
	assert.EqualValues(t, 6000, got["slophaus_landing_oldest_wait_seconds"])
}

func TestLandingCollector_NoStepSeriesWhileNothingHasBegun(t *testing.T) {
	reg := progressRegistry(t, fakeProgress{p: metrics.LandingProgress{Queued: 2, OldestWait: time.Hour}})
	assert.Equal(t, 5, testutil.CollectAndCount(metrics.NewLandingCollector(fakeProgress{p: metrics.LandingProgress{Queued: 2}}, oneTenant{}, slog.NewTextHandler(io.Discard, nil))))
	mfs, err := reg.Gather()
	require.NoError(t, err)
	for _, mf := range mfs {
		assert.NotContains(t, mf.GetName(), "head_step")
	}
}

func TestLandingCollector_AFailedReadIsAGapNotAZero(t *testing.T) {
	reg := progressRegistry(t, fakeProgress{err: errors.New("db down")})
	mfs, err := reg.Gather()
	require.Error(t, err)
	assert.Empty(t, mfs)
}

func TestLandingLastFinished_IsTheStartUntilALandingEndsThenThatLanding(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	m := metrics.New(&metrics.Options{Now: func() time.Time { return now }})
	m.TrackLanding([]string{"merged", "checks_red"})
	read := func(want string) {
		t.Helper()
		assert.Contains(t, scrape(t, m), "slophaus_landing_last_finished_timestamp_seconds "+want)
	}
	read("1e+06")
	now = now.Add(20 * time.Minute)
	m.LandingOutcome("checks_red", 0, 0, 1)
	read("1.0012e+06") // a handback is a landing that finished
	now = now.Add(time.Minute)
	m.LandingOutcome("merged", 0, time.Minute, 1)
	read("1.00126e+06")
}

type twoTenants struct{}

func (twoTenants) Tenants(context.Context) ([]uuid.UUID, error) {
	return []uuid.UUID{uuid.New(), uuid.New()}, nil
}

// inOrder answers each call with the next progress, one per tenant.
type inOrder struct{ next []metrics.LandingProgress }

func (s *inOrder) LandingProgress(context.Context) (metrics.LandingProgress, error) {
	p := s.next[0]
	s.next = s.next[1:]
	return p, nil
}

func TestLandingCollector_SumsTheQueuesAndMaxesTheAges(t *testing.T) {
	src := &inOrder{next: []metrics.LandingProgress{
		{Step: "waiting_checks", StepAge: 10 * time.Minute, StepBound: 75 * time.Minute, Queued: 3, OldestWait: time.Hour},
		{Step: "waiting_checks", StepAge: 20 * time.Minute, StepBound: 30 * time.Minute, Queued: 4, OldestWait: 10 * time.Minute},
	}}
	reg := prometheus.NewPedanticRegistry()
	require.NoError(t, reg.Register(metrics.NewLandingCollector(src, twoTenants{}, slog.NewTextHandler(io.Discard, nil))))
	mfs, err := reg.Gather()
	require.NoError(t, err)
	got := map[string]float64{}
	for _, mf := range mfs {
		got[mf.GetName()] = mf.GetMetric()[0].GetGauge().GetValue()
	}
	assert.EqualValues(t, 7, got["slophaus_landing_queue_length"])
	assert.EqualValues(t, 3600, got["slophaus_landing_oldest_wait_seconds"])
	assert.EqualValues(t, 1200, got["slophaus_landing_head_step_seconds"])
	assert.EqualValues(t, 4500, got["slophaus_landing_head_step_bound_seconds"])
}

func TestLandingCollector_ExportsStarvationAndBatchesWithoutCI(t *testing.T) {
	reg := progressRegistry(t, fakeProgress{p: metrics.LandingProgress{
		TopWait: 40 * time.Minute, BatchesWithoutCI: 2, OldestBatchWithoutCI: 6 * time.Minute,
	}})
	mfs, err := reg.Gather()
	require.NoError(t, err)
	got := map[string]float64{}
	for _, mf := range mfs {
		got[mf.GetName()] = mf.GetMetric()[0].GetGauge().GetValue()
	}
	assert.EqualValues(t, 2400, got["slophaus_landing_top_ranked_wait_seconds"])
	assert.EqualValues(t, 2, got["slophaus_landing_batches_without_ci"])
	assert.EqualValues(t, 360, got["slophaus_landing_oldest_batch_without_ci_seconds"])
}

// sequenceProgress answers one tenant after another.
type sequenceProgress struct {
	ps []metrics.LandingProgress
	n  int
}

func (s *sequenceProgress) LandingProgress(context.Context) (metrics.LandingProgress, error) {
	p := s.ps[s.n]
	s.n++
	return p, nil
}

func TestLandingCollector_SumsCountsAndMaxesAgesAcrossTenants(t *testing.T) {
	src := &sequenceProgress{ps: []metrics.LandingProgress{
		{TopWait: 50 * time.Minute, BatchesWithoutCI: 1, OldestBatchWithoutCI: 9 * time.Minute},
		{TopWait: 5 * time.Minute, BatchesWithoutCI: 2, OldestBatchWithoutCI: time.Minute},
	}}
	reg := prometheus.NewPedanticRegistry()
	require.NoError(t, reg.Register(metrics.NewLandingCollector(src, twoTenants{}, slog.NewTextHandler(io.Discard, nil))))
	mfs, err := reg.Gather()
	require.NoError(t, err)
	got := map[string]float64{}
	for _, mf := range mfs {
		got[mf.GetName()] = mf.GetMetric()[0].GetGauge().GetValue()
	}
	assert.EqualValues(t, 3000, got["slophaus_landing_top_ranked_wait_seconds"], "the longest, not the last tenant's")
	assert.EqualValues(t, 3, got["slophaus_landing_batches_without_ci"])
	assert.EqualValues(t, 540, got["slophaus_landing_oldest_batch_without_ci_seconds"])
}
