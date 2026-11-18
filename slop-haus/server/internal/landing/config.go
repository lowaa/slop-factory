package landing

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// Config bounds a landing. Zero fields take the defaults.
type Config struct {
	// RestartMargin is added to the CI repo timeout to bound the wait for
	// checks: the time the woodpecker-watchdog needs to notice a stuck
	// pipeline and restart it. The wait follows the newest pipeline, so the
	// margin covers one restart and no more.
	RestartMargin time.Duration
	// FallbackChecksTimeout bounds the wait when the CI repo timeout cannot be
	// read.
	FallbackChecksTimeout time.Duration
	// NeverStartedWait bounds how long a head with no checks at all is waited
	// for: CI that has not even started is not CI that is running, and holding
	// the queue for the whole CI bound on it stalls every ticket behind.
	NeverStartedWait time.Duration
	// NoPipelineWait bounds how long a head the CI has no pipeline for at all is
	// waited for, which is shorter than NeverStartedWait: a pipeline that exists
	// may just be queued, but a missing pull_request event does not arrive late.
	// It never exceeds NeverStartedWait.
	NoPipelineWait time.Duration
	// CheckStuckAfter is how long a required check may stay pending on the
	// host before the CI is asked what became of it: a status the CI never
	// delivered holds the queue for the whole CI bound otherwise.
	CheckStuckAfter time.Duration
	// UpdateWait bounds how long a branch update the host accepted but has not
	// applied is waited for, before it is asked for again, and again before the
	// ticket is handed back. The host usually applies it in seconds; waiting for
	// the CI bound instead freezes the whole queue.
	UpdateWait time.Duration
	// LockTimeout bounds the wait in the integration lock's queue.
	LockTimeout time.Duration
	// PollInterval is how often checks and the lock are asked about.
	PollInterval time.Duration
	// SweepInterval is how often every tenant's queue is looked at again, which
	// is what recovers a landing whose runner died.
	SweepInterval time.Duration
	// RepairGrace is how old a queue row or merge-queue attempt must be before
	// the sweep repairs it (ticket.RepairMergeQueue), so a write in flight in
	// another replica is not mistaken for a break.
	RepairGrace time.Duration
	// LeaseTTL is the server agent's lease; HeartbeatInterval must be well
	// under it.
	LeaseTTL          time.Duration
	HeartbeatInterval time.Duration
	// Affinity is the window the author gets first claim of a handed-back
	// ticket for.
	Affinity time.Duration
	// HostRetries is how many times a code host error is tried again, with
	// HostBackoff times the attempt number between.
	HostRetries int
	HostBackoff time.Duration
	// BatchStuckAfter is how long a batch may sit in one state, held by a lease
	// that is not expired, before it is alerted as stuck. It is not taken over.
	BatchStuckAfter time.Duration
	// KnownFlaky names checks whose failure is retried once. An entry is
	// "workflow:owner-ticket:review-date" (review date as 2026-11-22); a bare
	// name is still read, with a warning, and never expires.
	KnownFlaky []string
}

func (c Config) withDefaults() Config {
	def := func(d *time.Duration, v time.Duration) {
		if *d <= 0 {
			*d = v
		}
	}
	def(&c.RestartMargin, 15*time.Minute)
	def(&c.FallbackChecksTimeout, time.Hour)
	def(&c.NeverStartedWait, 10*time.Minute)
	def(&c.NoPipelineWait, 3*time.Minute)
	c.NoPipelineWait = min(c.NoPipelineWait, c.NeverStartedWait)
	def(&c.CheckStuckAfter, 10*time.Minute)
	def(&c.UpdateWait, 3*time.Minute)
	def(&c.LockTimeout, 30*time.Minute)
	def(&c.PollInterval, 10*time.Second)
	def(&c.SweepInterval, time.Minute)
	def(&c.RepairGrace, 2*time.Minute)
	def(&c.LeaseTTL, 5*time.Minute)
	def(&c.HeartbeatInterval, time.Minute)
	def(&c.Affinity, ticket.DefaultResumeAffinity)
	def(&c.HostBackoff, 5*time.Second)
	def(&c.BatchStuckAfter, 3*time.Hour)
	if c.HostRetries <= 0 {
		c.HostRetries = 3
	}
	return c
}

const flakyReviewLayout = time.DateOnly

// flakyEntry is a KnownFlaky entry: the workflow, the ticket that owns its
// flake and the day it is due for review. Owner 0 and a zero Review are a bare
// name.
type flakyEntry struct {
	Name   string
	Owner  int64
	Review time.Time
}

// expired is whether the review date has passed. The retry carries on, so an
// entry nobody reviews costs a warning and not a broken queue.
func (e flakyEntry) expired(now time.Time) bool {
	return !e.Review.IsZero() && !now.Before(e.Review.AddDate(0, 0, 1))
}

func (e flakyEntry) bare() bool { return e.Owner == 0 }

func parseFlakyEntry(raw string) (flakyEntry, error) {
	parts := strings.Split(raw, ":")
	switch len(parts) {
	case 1:
		return flakyEntry{Name: parts[0]}, nil
	case 3:
		owner, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil || owner <= 0 || parts[0] == "" {
			return flakyEntry{}, fmt.Errorf("known flaky entry %q: want name:owner-ticket:review-date", raw)
		}
		review, err := time.Parse(flakyReviewLayout, parts[2])
		if err != nil {
			return flakyEntry{}, fmt.Errorf("known flaky entry %q: review date: %w", raw, err)
		}
		return flakyEntry{Name: parts[0], Owner: owner, Review: review}, nil
	}
	return flakyEntry{}, fmt.Errorf("known flaky entry %q: want name:owner-ticket:review-date", raw)
}

// parseKnownFlaky reads every entry, or the first malformed one.
func parseKnownFlaky(raw []string) ([]flakyEntry, error) {
	out := make([]flakyEntry, 0, len(raw))
	for _, s := range raw {
		e, err := parseFlakyEntry(s)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, nil
}
