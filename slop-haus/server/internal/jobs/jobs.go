// Package jobs runs the server's background jobs: each on its own interval,
// logged and measured the same way, and stopped in order on the way out.
package jobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/lowaa/slop-factory/go-clock"
)

// Job is one background task.
type Job struct {
	Name string
	// Interval is the wait between the end of a run and the start of the
	// next. Zero runs the job once, at start. A job that is configured off is
	// not started at all.
	Interval time.Duration
	// Jitter adds up to this much, at random, to each wait, so replicas do not
	// all run in step.
	Jitter time.Duration
	// Run does one pass. Its error is logged and the job carries on: a
	// database blip must not silently stop a job for good.
	Run func(ctx context.Context) error
}

// Options are the Scheduler's seams.
type Options struct {
	Clock  clock.Clock
	Logger slog.Handler
	// AfterRun, when set, sees every finished run. For tests, which must know a
	// run happened without sleeping to find out.
	AfterRun func(name string, err error)
}

// Scheduler starts jobs and stops them again.
type Scheduler struct {
	clock    clock.Clock
	logger   *slog.Logger
	afterRun func(name string, err error)

	mu      sync.Mutex
	started []*running
	closed  bool
	stats   map[string]*stat
}

type running struct {
	name   string
	cancel context.CancelFunc
	done   chan struct{}
	once   sync.Once
}

func (r *running) stop() {
	r.once.Do(r.cancel)
	<-r.done
}

type stat struct {
	runs     int
	lastRun  time.Time
	lastErr  bool
	duration time.Duration
}

func New(opts *Options) *Scheduler {
	handler := opts.Logger
	if handler == nil {
		handler = slog.DiscardHandler
	}
	return &Scheduler{
		clock:    clock.Or(opts.Clock),
		logger:   slog.New(handler),
		afterRun: opts.AfterRun,
		stats:    map[string]*stat{},
	}
}

// Start runs the job in the background, the first pass at once. It returns
// the job's stop, which cancels the job and returns only once its run has
// returned, so that nothing the job uses is closed under it. Stopping twice
// is fine. ctx ending stops the job as well.
func (s *Scheduler) Start(ctx context.Context, job *Job) (stop func(), err error) {
	if job.Name == "" || job.Run == nil {
		return nil, errors.New("jobs: a job needs a name and a Run")
	}
	if job.Interval < 0 || job.Jitter < 0 {
		return nil, fmt.Errorf("jobs: %s: negative interval or jitter", job.Name)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, fmt.Errorf("jobs: %s: scheduler is closed", job.Name)
	}
	if _, dup := s.stats[job.Name]; dup {
		return nil, fmt.Errorf("jobs: %s is already started", job.Name)
	}
	s.stats[job.Name] = &stat{}

	ctx, cancel := context.WithCancel(ctx)
	r := &running{name: job.Name, cancel: cancel, done: make(chan struct{})}
	s.started = append(s.started, r)
	go func() {
		defer close(r.done)
		s.loop(ctx, job)
	}()
	return r.stop, nil
}

// Close stops every job, the last started first: a job started later may
// use what an earlier one does.
func (s *Scheduler) Close() {
	s.mu.Lock()
	s.closed = true
	started := s.started
	s.mu.Unlock()
	for i := len(started) - 1; i >= 0; i-- {
		started[i].stop()
	}
}

func (s *Scheduler) loop(ctx context.Context, job *Job) {
	for {
		s.runOnce(ctx, job)
		if job.Interval == 0 {
			return
		}
		wait := job.Interval
		if job.Jitter > 0 {
			wait += rand.N(job.Jitter)
		}
		select {
		case <-ctx.Done():
			return
		case <-s.clock.After(wait):
		}
	}
}

func (s *Scheduler) runOnce(ctx context.Context, job *Job) {
	start := s.clock.Now()
	err := job.Run(ctx)
	if ctx.Err() != nil {
		// Stopping cancels what is in flight; that is not a failure.
		return
	}
	if err != nil {
		s.logger.ErrorContext(ctx, "job failed", "job", job.Name, "err", err)
	}
	s.mu.Lock()
	st := s.stats[job.Name]
	st.runs++
	st.lastRun = s.clock.Now()
	st.lastErr = err != nil
	st.duration = clock.Since(s.clock, start)
	s.mu.Unlock()
	if s.afterRun != nil {
		s.afterRun(job.Name, err)
	}
}

var (
	lastRunDesc = prometheus.NewDesc("slophaus_job_last_run_timestamp_seconds", "When the job last finished a run.", []string{"job"}, nil)
	lastErrDesc = prometheus.NewDesc("slophaus_job_last_run_failed", "1 if the job's last run returned an error, else 0.", []string{"job"}, nil)
	durDesc     = prometheus.NewDesc("slophaus_job_last_run_duration_seconds", "How long the job's last run took.", []string{"job"}, nil)
	runsDesc    = prometheus.NewDesc("slophaus_job_runs_total", "Runs the job has finished since the process started.", []string{"job"}, nil)
)

// Collector exports each job's last run, last outcome and duration.
func (s *Scheduler) Collector() prometheus.Collector { return collector{s} }

type collector struct{ s *Scheduler }

func (collector) Describe(ch chan<- *prometheus.Desc) {
	ch <- lastRunDesc
	ch <- lastErrDesc
	ch <- durDesc
	ch <- runsDesc
}

func (c collector) Collect(ch chan<- prometheus.Metric) {
	c.s.mu.Lock()
	defer c.s.mu.Unlock()
	for name, st := range c.s.stats {
		if st.runs == 0 {
			continue
		}
		failed := 0.0
		if st.lastErr {
			failed = 1
		}
		ch <- prometheus.MustNewConstMetric(lastRunDesc, prometheus.GaugeValue, float64(st.lastRun.UnixNano())/1e9, name)
		ch <- prometheus.MustNewConstMetric(lastErrDesc, prometheus.GaugeValue, failed, name)
		ch <- prometheus.MustNewConstMetric(durDesc, prometheus.GaugeValue, st.duration.Seconds(), name)
		ch <- prometheus.MustNewConstMetric(runsDesc, prometheus.CounterValue, float64(st.runs), name)
	}
}
