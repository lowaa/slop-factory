package landing_test

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/landing"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

type timeoutOnly struct {
	landing.Pipelines
	timeout time.Duration
}

func (p timeoutOnly) RepoTimeout(context.Context) (time.Duration, error) { return p.timeout, nil }

func exceeds(t *testing.T, p landing.Progress) bool {
	t.Helper()
	bounds := landing.NewStepBounds(timeoutOnly{timeout: time.Hour}, landing.Config{RestartMargin: 5 * time.Minute}, nil, slog.DiscardHandler)
	return p.StepAge > bounds.Of(context.Background(), p.Step)
}

func TestProgress_TestingBatchIsWaitingOnChecksNotMergingMain(t *testing.T) {
	b := newBatch(t, 2)
	batch, err := reserveNow(b.ctx, b.store, baseOld, b.ids())
	require.NoError(t, err)
	require.NoError(t, b.store.SetState(b.ctx, batch.ID, landing.BatchTesting))
	_, err = b.database.Exec(b.ctx, `UPDATE landings SET updated_at = now() - interval '20 minutes' WHERE batch_id = $1`, batch.ID)
	require.NoError(t, err)

	p, err := b.queue.Progress(b.ctx)
	require.NoError(t, err)
	assert.Equal(t, landing.WaitingChecks, p.Step)
	assert.Greater(t, p.StepAge, 19*time.Minute)
	assert.False(t, exceeds(t, p), "20 minutes of CI is within the checks bound")

	_, err = b.database.Exec(b.ctx, `UPDATE landings SET updated_at = now() - interval '150 minutes' WHERE batch_id = $1`, batch.ID)
	require.NoError(t, err)
	p, err = b.queue.Progress(b.ctx)
	require.NoError(t, err)
	assert.True(t, exceeds(t, p), "a batch past both CI bounds (the lost pipeline's and its restart's) is stuck")
}

func TestProgress_ReservedBatchGetsChecksBoundAndSerialRowKeepsItsStep(t *testing.T) {
	b := newBatch(t, 2)
	batch, err := reserveNow(b.ctx, b.store, baseOld, b.ids())
	require.NoError(t, err)
	_, err = b.database.Exec(b.ctx, `UPDATE landings SET updated_at = now() - interval '20 minutes' WHERE batch_id = $1`, batch.ID)
	require.NoError(t, err)
	p, err := b.queue.Progress(b.ctx)
	require.NoError(t, err)
	assert.Equal(t, landing.WaitingChecks, p.Step, "a reserved member of a batch is waiting on its checks, not merging main")
	assert.False(t, exceeds(t, p), "20 minutes is within CI's bound, not the 15 minute merging bound")

	f := newFixture(t)
	serial := f.finishedChange(model.PriorityNormal)
	require.NoError(t, f.queue.SetStep(f.ctx, serial.ticketID, landing.MergingMain))
	_, err = f.database.Exec(f.ctx, `UPDATE landings SET updated_at = now() - interval '20 minutes' WHERE ticket_id = $1`, serial.ticketID)
	require.NoError(t, err)
	p, err = f.queue.Progress(f.ctx)
	require.NoError(t, err)
	assert.Equal(t, landing.MergingMain, p.Step)
	assert.True(t, exceeds(t, p), "a serial landing stuck on merging_main past 15 minutes")
}
