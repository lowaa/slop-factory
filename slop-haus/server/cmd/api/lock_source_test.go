package main

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/landing"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/metrics"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

type lockStatesFake struct{ locks []ticket.NamedLockState }

func (f lockStatesFake) IntegrationLocks(context.Context) ([]ticket.NamedLockState, error) {
	return f.locks, nil
}

type progressFake struct{ p landing.Progress }

func (f progressFake) Progress(context.Context) (landing.Progress, error) { return f.p, nil }

type boundsFake struct{}

func (boundsFake) Of(_ context.Context, step landing.Step) time.Duration {
	if step == landing.WaitingChecks {
		return 75 * time.Minute
	}
	return 15 * time.Minute
}

func TestTicketLockSource_CountsTheLineNotItsAgeUnderMergeQueue(t *testing.T) {
	t.Parallel()
	locks := lockStatesFake{locks: []ticket.NamedLockState{{LockState: &ticket.LockState{
		Holder: &ticket.LockHolder{Held: time.Minute}, Queue: make([]ticket.LockQueueEntry, 2),
	}}}}
	head := progressFake{landing.Progress{Landing: 6, OldestWait: 2 * time.Hour, Step: landing.WaitingChecks, StepAge: time.Minute}}

	for name, tc := range map[string]struct {
		on   bool
		held time.Duration
		wait int
	}{
		"worker keeps the lock holder":                                {false, time.Minute, 2},
		"merge_queue holds nothing, however long the line has waited": {true, 0, 5},
	} {
		t.Run(name, func(t *testing.T) {
			src := ticketLockSource{tickets: locks, queue: head, mode: landing.ModeFunc(func(context.Context) (bool, error) { return tc.on, nil })}
			loads, err := src.LockLoads(context.Background())
			require.NoError(t, err)
			require.Len(t, loads, 1)
			assert.Equal(t, tc.held, loads[0].Held)
			assert.Equal(t, tc.wait, loads[0].Waiters)
		})
	}
}

func TestLandingProgressSource_BoundsOnlyTheStepInHand(t *testing.T) {
	t.Parallel()
	src := landingProgressSource{bounds: boundsFake{}, queue: progressFake{landing.Progress{
		Landing: 8, OldestWait: 100 * time.Minute, Step: landing.WaitingChecks, StepAge: 5 * time.Minute,
	}}}
	got, err := src.LandingProgress(context.Background())
	require.NoError(t, err)
	assert.Equal(t, metrics.LandingProgress{
		Step: "waiting_checks", StepAge: 5 * time.Minute, StepBound: 75 * time.Minute, Queued: 8, OldestWait: 100 * time.Minute,
	}, got)

	src.queue = progressFake{landing.Progress{Landing: 1, Step: landing.Merging, StepAge: time.Minute}}
	got, err = src.LandingProgress(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 15*time.Minute, got.StepBound, "a step that is not a wait for CI has the fixed bound")

	src.queue = progressFake{landing.Progress{Landing: 8, OldestWait: 100 * time.Minute}}
	got, err = src.LandingProgress(context.Background())
	require.NoError(t, err)
	assert.Zero(t, got.StepBound, "no step has begun")
}
