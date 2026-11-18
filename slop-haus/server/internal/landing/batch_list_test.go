package landing_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/landing"
)

// seedBatches leaves two batches in the fixture's tenant: an older one that
// was tested, given a pipeline and a change, then voided; and a newer one that
// has never recorded a pipeline.
func (b *batchFixture) seedBatches() (older, newer *landing.Batch) {
	b.t.Helper()
	older, err := reserveNow(b.ctx, b.store, baseOld, b.ids())
	require.NoError(b.t, err)
	require.NoError(b.t, b.store.SetTested(b.ctx, older.ID, []string{headA, headB}, []string{headA, headB}))
	require.NoError(b.t, b.store.SetPipeline(b.ctx, older.ID, "412"))
	require.NoError(b.t, b.store.SetChange(b.ctx, older.ID, b.ref(77)))
	require.NoError(b.t, b.store.Void(b.ctx, older.ID, "test"))
	newer, err = reserveNow(b.ctx, b.store, baseNew, b.ids()[:1])
	require.NoError(b.t, err)
	return older, newer
}

func TestBatchStore_RecentIsNewestFirstWithWhatTheBatchRecorded(t *testing.T) {
	b := newBatch(t, 2)
	older, newer := b.seedBatches()

	got, err := b.store.Recent(b.ctx, 20, nil)
	require.NoError(t, err)

	require.Len(t, got, 2)
	assert.Equal(t, newer.ID, got[0].ID)
	assert.Empty(t, got[0].CIPipeline, "a pipeline never recorded stays empty")
	assert.Equal(t, landing.BatchBuilding, got[0].State)
	assert.Equal(t, older.ID, got[1].ID)
	assert.Equal(t, landing.BatchDone, got[1].State)
	assert.Equal(t, b.ids(), got[1].Members)
	assert.Equal(t, []string{headA, headB}, got[1].Heads)
	assert.Equal(t, "412", got[1].CIPipeline)
	require.NotNil(t, got[1].Change)
	assert.Equal(t, int64(77), got[1].Change.Number)
}

func TestBatchStore_RecentLimit(t *testing.T) {
	b := newBatch(t, 2)
	_, newer := b.seedBatches()

	got, err := b.store.Recent(b.ctx, 1, nil)
	require.NoError(t, err)

	require.Len(t, got, 1)
	assert.Equal(t, newer.ID, got[0].ID)
}

func TestBatchStore_RecentSince(t *testing.T) {
	b := newBatch(t, 2)
	older, newer := b.seedBatches()
	// Dated apart in the table itself, so the test does not wait on a clock.
	cut := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	_, err := b.database.Exec(b.ctx, `UPDATE batches SET created_at = $2 WHERE id = $1`, older.ID, cut.Add(-time.Hour))
	require.NoError(t, err)
	_, err = b.database.Exec(b.ctx, `UPDATE batches SET created_at = $2 WHERE id = $1`, newer.ID, cut.Add(time.Hour))
	require.NoError(t, err)

	got, err := b.store.Recent(b.ctx, 20, &cut)
	require.NoError(t, err)

	require.Len(t, got, 1)
	assert.Equal(t, newer.ID, got[0].ID)
}

func TestBatchStore_RecentNamesWhyABatchEndedUnmerged(t *testing.T) {
	b := newBatch(t, 1)
	older, newer := b.seedBatches()
	// An older failure, the latest failure, a newer merge, and a failure of
	// another batch: only the latest failure of this batch is its reason.
	seed := func(batchID int64, outcome, detail string) {
		_, err := b.database.Exec(b.ctx, `
INSERT INTO server_merge_outcomes (ticket_id, attempt_number, change_url, head_sha, outcome, detail, batch_id)
SELECT ticket_id, 1, 'u', 'h', $2, $3, $1 FROM landings LIMIT 1`, batchID, outcome, detail)
		require.NoError(t, err)
	}
	seed(older.ID, "batch_interaction", "old")
	seed(older.ID, "batch_voided", "main moved")
	seed(older.ID, "merged", "")
	seed(newer.ID, "checks_red", "other batch")

	got, err := b.store.Recent(b.ctx, 20, nil)
	require.NoError(t, err)

	byID := map[int64]string{}
	for _, s := range got {
		byID[s.ID] = s.Reason
	}
	assert.Equal(t, "batch_voided: main moved", byID[older.ID])
	assert.Equal(t, "checks_red: other batch", byID[newer.ID])
}

func TestBatchStore_RecentIsTheCallersTenantsOnly(t *testing.T) {
	b := newBatch(t, 2)
	mine, _ := b.seedBatches()
	otherCtx := db.WithTenant(b.ctx, dbtest.NewTenant(t, b.database))
	_, err := b.database.Exec(otherCtx, `
INSERT INTO batches (base_sha, branch, member_ticket_ids, state) VALUES ($1, 'theirs', '{9}', 'done')`, baseOld)
	require.NoError(t, err)

	got, err := b.store.Recent(b.ctx, 100, nil)
	require.NoError(t, err)
	for _, s := range got {
		assert.NotEqual(t, "theirs", s.Branch)
	}
	assert.Len(t, got, 2)
	assert.Contains(t, []int64{got[0].ID, got[1].ID}, mine.ID)

	theirs, err := b.store.Recent(otherCtx, 100, nil)
	require.NoError(t, err)
	require.Len(t, theirs, 1)
	assert.Equal(t, "theirs", theirs[0].Branch)
}
