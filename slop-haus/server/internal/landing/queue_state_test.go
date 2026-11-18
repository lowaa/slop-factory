package landing_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/landing"
)

// requireQueueStateConsistent holds every landing to a stored phase that
// agrees with its batch membership, and every ended batch to carry a result.
func requireQueueStateConsistent(t *testing.T, ctx context.Context, database *db.DB) {
	t.Helper()
	rows, err := database.Query(ctx, `SELECT ticket_id, coalesce(batch_id, 0), queue_state FROM landings`)
	require.NoError(t, err)
	defer rows.Close()
	for rows.Next() {
		var id, batch int64
		var stored string
		require.NoError(t, rows.Scan(&id, &batch, &stored))
		assert.True(t, landing.EntryState{Phase: landing.Phase(stored), BatchID: batch}.Valid(), "ticket %d: %s with batch %d is no state", id, stored, batch)
	}
	require.NoError(t, rows.Err())

	var unresulted int
	require.NoError(t, database.QueryRow(ctx, `SELECT count(*) FROM batches WHERE (state = 'done') <> (result IS NOT NULL)`).Scan(&unresulted))
	assert.Zero(t, unresulted, "every done batch has a result, and no live one")
}

func TestQueueStateIsWrittenByEveryLandingWriter(t *testing.T) {
	f := newStoreFixture(t)
	s := landing.NewLandingStore(f.database)
	check := func() { requireQueueStateConsistent(t, f.ctx, f.database) }

	a, b := f.ticket(t), f.ticket(t)
	require.NoError(t, s.Enqueue(f.ctx, landingRow(a, "h1")))
	require.NoError(t, s.Enqueue(f.ctx, landingRow(b, "h1")))
	check()
	for _, st := range []landing.Step{landing.MergingMain, landing.WaitingChecks, landing.Merging} {
		require.NoError(t, s.SetStep(f.ctx, a, st))
		check()
	}
	require.NoError(t, s.HandBack(f.ctx, a, landing.ChecksRed, false))
	check()
	f.attempt(t, a, 2)
	next := landingRow(a, "h2")
	next.AttemptNumber = 2
	require.NoError(t, s.Enqueue(f.ctx, next))
	check()
	var phase string
	require.NoError(t, f.database.QueryRow(f.ctx, `SELECT queue_state FROM landings WHERE ticket_id = $1`, a).Scan(&phase))
	assert.Equal(t, "queued", phase)
}

func TestQueueStateIsWrittenByEveryBatchWriter(t *testing.T) {
	f := newStoreFixture(t)
	s := landing.NewLandingStore(f.database)
	bs := landing.NewBatchStore(f.database)
	check := func() { requireQueueStateConsistent(t, f.ctx, f.database) }
	phaseOf := func(id int64) string {
		var p string
		require.NoError(t, f.database.QueryRow(f.ctx, `SELECT queue_state FROM landings WHERE ticket_id = $1`, id).Scan(&p))
		return p
	}

	ids := []int64{f.ticket(t), f.ticket(t), f.ticket(t), f.ticket(t)}
	for _, id := range ids {
		require.NoError(t, s.Enqueue(f.ctx, landingRow(id, "h")))
	}
	batch, err := bs.Create(f.ctx, baseOld, ids)
	require.NoError(t, err)
	check()
	assert.Equal(t, "queued", phaseOf(ids[0]), "building reserves nothing")
	_, err = bs.Drop(f.ctx, batch.ID, ids[3])
	require.NoError(t, err)
	check()
	_, err = bs.Reserve(f.ctx, batch.ID)
	require.NoError(t, err)
	check()
	assert.Equal(t, "batch_testing", phaseOf(ids[0]))

	// The batch runner moves a member's step while the batch is live.
	require.NoError(t, s.SetStep(f.ctx, ids[0], landing.Merging))
	check()
	assert.Equal(t, "batch_merging", phaseOf(ids[0]))

	require.NoError(t, bs.Release(f.ctx, batch.ID, ids[2]))
	check()
	assert.Equal(t, "queued", phaseOf(ids[2]))

	require.NoError(t, s.HandBack(f.ctx, ids[1], landing.ChecksRed, false))
	check()

	require.NoError(t, bs.Void(f.ctx, batch.ID, "test"))
	check()
	assert.Equal(t, "queued", phaseOf(ids[0]))
	got, err := bs.Get(f.ctx, batch.ID)
	require.NoError(t, err)
	assert.Equal(t, landing.BatchDone, got.State)
	assertBatchResult(t, f, batch.ID, "voided")
}

func assertBatchResult(t *testing.T, f *storeFixture, id int64, want string) {
	t.Helper()
	var got string
	require.NoError(t, f.database.QueryRow(f.ctx, `SELECT coalesce(result, '') FROM batches WHERE id = $1`, id).Scan(&got))
	assert.Equal(t, want, got)
}

func TestBatchResultIsWrittenWhenABatchEnds(t *testing.T) {
	f := newStoreFixture(t)
	s := landing.NewLandingStore(f.database)
	bs := landing.NewBatchStore(f.database)
	reserve := func(n int) (int64, []int64) {
		var ids []int64
		for range n {
			id := f.ticket(t)
			require.NoError(t, s.Enqueue(f.ctx, landingRow(id, "h")))
			ids = append(ids, id)
		}
		b, err := reserveNow(f.ctx, bs, baseOld, ids)
		require.NoError(t, err)
		return b.ID, ids
	}

	viaSetState, _ := reserve(1)
	require.NoError(t, bs.SetState(f.ctx, viaSetState, landing.BatchDone))
	assertBatchResult(t, f, viaSetState, "voided")

	merged, _ := reserve(1)
	require.NoError(t, bs.Finish(f.ctx, merged, landing.ResultMerged, ""))
	assertBatchResult(t, f, merged, "merged")

	split, _ := reserve(1)
	require.NoError(t, bs.Finish(f.ctx, split, landing.ResultSplit, "red"))
	var reason string
	require.NoError(t, f.database.QueryRow(f.ctx, `SELECT result_reason FROM batches WHERE id = $1`, split).Scan(&reason))
	assert.Equal(t, "red", reason)
	assert.Error(t, bs.Finish(f.ctx, split, landing.ResultMerged, ""), "an ended batch is not ended again")
	assert.ErrorIs(t, bs.Finish(f.ctx, merged, "bogus", ""), landing.ErrInvalid)

	var ids []int64
	id := f.ticket(t)
	require.NoError(t, s.Enqueue(f.ctx, landingRow(id, "h")))
	ids = append(ids, id)
	created, err := bs.Create(f.ctx, baseOld, ids)
	require.NoError(t, err)
	emptied := created.ID
	_, err = bs.Drop(f.ctx, emptied, ids[0])
	require.NoError(t, err)
	assertBatchResult(t, f, emptied, "voided")

	live, _ := reserve(1)
	assertBatchResult(t, f, live, "")
	requireQueueStateConsistent(t, f.ctx, f.database)
}
