package jobs_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-clock"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/jobs"
)

type finished struct {
	name string
	err  error
}

// rig is a Scheduler on a fake clock whose finished runs arrive on a channel,
// so a test steps through passes without sleeping.
type rig struct {
	clock *clock.Fake
	sched *jobs.Scheduler
	runs  chan finished
}

func newRig() *rig {
	r := &rig{clock: clock.NewFake(time.Unix(1_000_000, 0)), runs: make(chan finished, 64)}
	r.sched = jobs.New(&jobs.Options{Clock: r.clock, AfterRun: func(name string, err error) { r.runs <- finished{name, err} }})
	return r
}

func (r *rig) next(t *testing.T) finished {
	t.Helper()
	select {
	case f := <-r.runs:
		return f
	case <-time.After(10 * time.Second):
		t.Fatal("no run finished")
		return finished{}
	}
}

func TestJob_RunsAtStartThenOncePerInterval(t *testing.T) {
	r := newRig()
	var mu sync.Mutex
	calls := 0
	stop, err := r.sched.Start(context.Background(), &jobs.Job{Name: "j", Interval: time.Minute, Run: func(context.Context) error {
		mu.Lock()
		defer mu.Unlock()
		calls++
		return nil
	}})
	require.NoError(t, err)
	defer stop()

	r.next(t)
	r.clock.BlockUntil(1)
	r.clock.Advance(59 * time.Second)
	r.clock.Advance(time.Second)
	r.next(t)
	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, 2, calls)
}

func TestJob_ZeroIntervalRunsOnce(t *testing.T) {
	r := newRig()
	stop, err := r.sched.Start(context.Background(), &jobs.Job{Name: "once", Run: func(context.Context) error { return nil }})
	require.NoError(t, err)
	defer stop()
	r.next(t)
	require.Zero(t, r.clock.Waiters())
}

func TestJob_ReportsASuccessfulRunInTheMetrics(t *testing.T) {
	r := newRig()
	stop, err := r.sched.Start(context.Background(), &jobs.Job{Name: "ok", Interval: time.Minute, Run: func(context.Context) error { return nil }})
	require.NoError(t, err)
	defer stop()
	r.next(t)
	reg := prometheus.NewRegistry()
	require.NoError(t, reg.Register(r.sched.Collector()))
	require.NoError(t, testutil.GatherAndCompare(reg, strings.NewReader(`
# HELP slophaus_job_last_run_failed 1 if the job's last run returned an error, else 0.
# TYPE slophaus_job_last_run_failed gauge
slophaus_job_last_run_failed{job="ok"} 0
`), "slophaus_job_last_run_failed"))
}

func TestJob_ReportsMetrics(t *testing.T) {
	r := newRig()
	fail := errors.New("boom")
	stop, err := r.sched.Start(context.Background(), &jobs.Job{Name: "m", Interval: time.Minute, Run: func(context.Context) error { return fail }})
	require.NoError(t, err)
	defer stop()
	require.Equal(t, fail, r.next(t).err)

	reg := prometheus.NewRegistry()
	require.NoError(t, reg.Register(r.sched.Collector()))
	require.NoError(t, testutil.GatherAndCompare(reg, strings.NewReader(`
# HELP slophaus_job_last_run_duration_seconds How long the job's last run took.
# TYPE slophaus_job_last_run_duration_seconds gauge
slophaus_job_last_run_duration_seconds{job="m"} 0
# HELP slophaus_job_last_run_failed 1 if the job's last run returned an error, else 0.
# TYPE slophaus_job_last_run_failed gauge
slophaus_job_last_run_failed{job="m"} 1
# HELP slophaus_job_last_run_timestamp_seconds When the job last finished a run.
# TYPE slophaus_job_last_run_timestamp_seconds gauge
slophaus_job_last_run_timestamp_seconds{job="m"} 1e+06
# HELP slophaus_job_runs_total Runs the job has finished since the process started.
# TYPE slophaus_job_runs_total counter
slophaus_job_runs_total{job="m"} 1
`)))
}

func TestScheduler_AFailingJobDoesNotStopTheOthers(t *testing.T) {
	r := newRig()
	for _, name := range []string{"bad", "good"} {
		fails := name == "bad"
		stop, err := r.sched.Start(context.Background(), &jobs.Job{Name: name, Interval: time.Minute, Run: func(context.Context) error {
			if fails {
				return errors.New("boom")
			}
			return nil
		}})
		require.NoError(t, err)
		defer stop()
	}
	got := map[string]error{}
	for range 2 {
		f := r.next(t)
		got[f.name] = f.err
	}
	require.Error(t, got["bad"])
	require.NoError(t, got["good"])

	r.clock.BlockUntil(2)
	r.clock.Advance(time.Minute)
	second := map[string]error{}
	for range 2 {
		f := r.next(t)
		second[f.name] = f.err
	}
	require.Error(t, second["bad"], "the failing job ran again")
	require.NoError(t, second["good"])
}

func TestScheduler_CloseStopsLastStartedFirstAndWaitsForRuns(t *testing.T) {
	r := newRig()
	var order []string
	var mu sync.Mutex
	for _, name := range []string{"first", "second", "third"} {
		started := make(chan struct{})
		var once sync.Once
		_, err := r.sched.Start(context.Background(), &jobs.Job{Name: name, Interval: time.Hour, Run: func(ctx context.Context) error {
			once.Do(func() { close(started) })
			<-ctx.Done()
			mu.Lock()
			order = append(order, name)
			mu.Unlock()
			return ctx.Err()
		}})
		require.NoError(t, err)
		<-started
	}
	r.sched.Close()
	require.Equal(t, []string{"third", "second", "first"}, order)
	r.sched.Close() // closing again is harmless
	_, err := r.sched.Start(context.Background(), &jobs.Job{Name: "late", Run: func(context.Context) error { return nil }})
	require.Error(t, err, "a closed scheduler starts nothing")
}

func TestStart_RejectsBadJobs(t *testing.T) {
	r := newRig()
	run := func(context.Context) error { return nil }
	_, err := r.sched.Start(context.Background(), &jobs.Job{Run: run})
	require.Error(t, err)
	_, err = r.sched.Start(context.Background(), &jobs.Job{Name: "neg", Interval: -1, Run: run})
	require.Error(t, err)
	stop, err := r.sched.Start(context.Background(), &jobs.Job{Name: "dup", Run: run})
	require.NoError(t, err)
	defer stop()
	_, err = r.sched.Start(context.Background(), &jobs.Job{Name: "dup", Run: run})
	require.Error(t, err)
}

// stop returns only once the run has: a closer pushed before it -- the
// database, under a job -- is never closed under a run still using it.
func TestScheduler_StopWaitsForTheRunToReturn(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := newRig()
		started := make(chan struct{})
		finish := make(chan struct{})
		stop, err := r.sched.Start(context.Background(), &jobs.Job{Name: "slow", Interval: time.Minute, Run: func(ctx context.Context) error {
			close(started)
			<-ctx.Done()
			<-finish // still mid-pass when told to stop
			return nil
		}})
		require.NoError(t, err)
		<-started

		stopped := make(chan struct{})
		go func() { stop(); close(stopped) }()
		synctest.Wait()
		select {
		case <-stopped:
			t.Fatal("stop returned while the run was still going")
		default:
		}
		close(finish)
		<-stopped
	})
}
