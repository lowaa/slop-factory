package change_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/change"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
)

func ref(n int64) codehost.ChangeRef {
	return codehost.ChangeRef{Host: hostName, Owner: "acme", Repo: "app", Number: n}
}

func TestStore_OpenSupersedesTheLiveChangeAndKeepsOneLive(t *testing.T) {
	f := newFixture(t)
	h := f.hold(false)
	rec := func(n int64, head string) *change.Record {
		return &change.Record{TicketID: h.ticket.ID, Change: ref(n), Head: head, Base: "main", OpenedBy: h.agent}
	}
	first, err := f.openRecord(f.ctx, rec(1, "a"))
	require.NoError(t, err)
	second, err := f.openRecord(f.ctx, rec(2, "b"))
	require.NoError(t, err)

	current, err := f.store.Current(f.ctx, h.ticket.ID, 1)
	require.NoError(t, err)
	assert.Equal(t, second.ID, current.ID)
	merged, err := f.store.MarkMerged(f.ctx, first.ID, h.agent, "sha")
	require.NoError(t, err)
	assert.False(t, merged, "a superseded change cannot be merged")

	// The database itself allows one live change per ticket, whatever the
	// code above does.
	_, err = f.database.Exec(f.ctx, `
		INSERT INTO ticket_changes (ticket_id, attempt_number, host, owner, repo, number, head_branch, base_branch, draft, opened_by_agent_id)
		VALUES ($1, 1, $2, 'acme', 'app', 3, 'c', 'main', false, $3)`, h.ticket.ID, hostName, h.agent)
	require.Error(t, err)
}

func TestStore_AChangeBelongsToOneTicket(t *testing.T) {
	f := newFixture(t)
	h := f.hold(false)
	_, err := f.openRecord(f.ctx, &change.Record{TicketID: h.ticket.ID, Change: ref(1), Head: "a", Base: "main", OpenedBy: h.agent})
	require.NoError(t, err)

	other := f.hold2()
	_, err = f.openRecord(f.ctx, &change.Record{TicketID: other.ticket.ID, Change: ref(1), Head: "b", Base: "main", OpenedBy: other.agent})

	require.Error(t, err, "the same change cannot be recorded for a second ticket")
}

func TestStore_MarkMergedOnce(t *testing.T) {
	f := newFixture(t)
	h := f.hold(false)
	rec, err := f.openRecord(f.ctx, &change.Record{TicketID: h.ticket.ID, Change: ref(1), Head: "a", Base: "main", OpenedBy: h.agent})
	require.NoError(t, err)

	first, err := f.store.MarkMerged(f.ctx, rec.ID, h.agent, "sha1")
	require.NoError(t, err)
	second, err := f.store.MarkMerged(f.ctx, rec.ID, h.agent, "sha2")
	require.NoError(t, err)

	assert.True(t, first)
	assert.False(t, second)
	got, err := f.store.Current(f.ctx, h.ticket.ID, 1)
	require.NoError(t, err)
	assert.Equal(t, "sha1", got.MergeSHA)
}

func TestStore_ANewChangeAfterAMergeLeavesTheMergedOneAlone(t *testing.T) {
	f := newFixture(t)
	h := f.hold(false)
	rec, err := f.openRecord(f.ctx, &change.Record{TicketID: h.ticket.ID, Change: ref(1), Head: "a", Base: "main", OpenedBy: h.agent})
	require.NoError(t, err)
	_, err = f.store.MarkMerged(f.ctx, rec.ID, h.agent, "sha")
	require.NoError(t, err)

	next, err := f.openRecord(f.ctx, &change.Record{TicketID: h.ticket.ID, Change: ref(2), Head: "b", Base: "main", OpenedBy: h.agent})
	require.NoError(t, err)

	var mergedStill int
	require.NoError(t, f.database.QueryRow(f.ctx, `SELECT count(*) FROM ticket_changes WHERE id = $1 AND merged_at IS NOT NULL AND superseded_at IS NULL`, rec.ID).Scan(&mergedStill))
	assert.Equal(t, 1, mergedStill)
	current, err := f.store.Current(f.ctx, h.ticket.ID, 1)
	require.NoError(t, err)
	assert.Equal(t, next.ID, current.ID)
}

func TestStore_TenantsDoNotSeeEachOthersChanges(t *testing.T) {
	f := newFixture(t)
	h := f.hold(false)
	_, err := f.openRecord(f.ctx, &change.Record{TicketID: h.ticket.ID, Change: ref(1), Head: "a", Base: "main", OpenedBy: h.agent})
	require.NoError(t, err)
	otherTenant := dbtest.NewTenant(t, f.database)
	otherCtx := db.WithTenant(context.Background(), otherTenant)

	_, err = f.store.Current(otherCtx, h.ticket.ID, 1)

	assert.ErrorIs(t, err, change.ErrNoChange, "the same ticket number in another tenant has no change")
	rec, err := f.openRecord(f.ctx, &change.Record{TicketID: h.ticket.ID, Change: ref(2), Head: "b", Base: "main", Draft: true, OpenedBy: h.agent})
	require.NoError(t, err)

	merged, err := f.store.MarkMerged(otherCtx, rec.ID, h.agent, "sha")
	require.NoError(t, err)
	assert.False(t, merged, "nor can another tenant mark one merged")
	claim, err := f.store.BeginMerge(otherCtx, rec.ID)
	require.NoError(t, err)
	assert.Nil(t, claim, "nor claim one for a merge")
	require.NoError(t, f.store.MarkReady(otherCtx, rec.ID))
	still, err := f.store.Current(f.ctx, h.ticket.ID, 1)
	require.NoError(t, err)
	assert.True(t, still.Draft, "nor mark one ready")
}

// The store refuses on its own to replace a change being merged, whatever
// the service checked before: the two can race.
func TestStore_OpenWaitsOutAMergeInFlight(t *testing.T) {
	f := newFixture(t)
	h := f.hold(false)
	rec, err := f.openRecord(f.ctx, &change.Record{TicketID: h.ticket.ID, Change: ref(1), Head: "a", Base: "main", OpenedBy: h.agent})
	require.NoError(t, err)
	claim, err := f.store.BeginMerge(f.ctx, rec.ID)
	require.NoError(t, err)
	require.NotNil(t, claim)
	again, err := f.store.BeginMerge(f.ctx, rec.ID)
	require.NoError(t, err)
	assert.Nil(t, again, "one merge at a time")

	_, err = f.openRecord(f.ctx, &change.Record{TicketID: h.ticket.ID, Change: ref(2), Head: "b", Base: "main", OpenedBy: h.agent})

	require.ErrorIs(t, err, change.ErrMergeInFlight)
	current, err := f.store.Current(f.ctx, h.ticket.ID, 1)
	require.NoError(t, err)
	assert.Equal(t, rec.ID, current.ID, "the claimed change is still the live one")
	assert.True(t, current.Merging)
}

// A merge that ran past its claim's bound and then failed must not end the
// claim another merge has taken over since.
func TestStore_AnExpiredClaimsEndDoesNotEndTheNewOne(t *testing.T) {
	f := newFixture(t)
	h := f.hold(false)
	rec, err := f.openRecord(f.ctx, &change.Record{TicketID: h.ticket.ID, Change: ref(1), Head: "a", Base: "main", OpenedBy: h.agent})
	require.NoError(t, err)
	old, err := f.store.BeginMerge(f.ctx, rec.ID)
	require.NoError(t, err)
	require.NotNil(t, old)
	_, err = f.database.Exec(f.ctx, `UPDATE ticket_changes SET merging_since = now() - interval '10 minutes' WHERE id = $1`, rec.ID)
	require.NoError(t, err)
	newer, err := f.store.BeginMerge(f.ctx, rec.ID)
	require.NoError(t, err)
	require.NotNil(t, newer, "the stale claim is taken over")

	require.NoError(t, f.store.EndMerge(f.ctx, old))

	current, err := f.store.Current(f.ctx, h.ticket.ID, 1)
	require.NoError(t, err)
	assert.True(t, current.Merging, "the newer claim stands")
}

// The clear is part of the hand-over's transaction: when that does not
// commit, the recorded head is as it was.
func TestStore_ClearReviewedHeadRollsBackWithItsTransaction(t *testing.T) {
	f := newFixture(t)
	h := f.hold(true)
	f.open(h, false)
	rec, err := f.store.Current(f.ctx, h.ticket.ID, 1)
	require.NoError(t, err)
	require.NoError(t, f.database.InTx(f.ctx, func(q db.Querier) error {
		return f.store.SetReviewed(f.ctx, q, rec.ID, &change.Reviewed{HeadSHA: headA, DiffFingerprint: "v1:aa", BaseSHA: "base1"})
	}))

	abort := errors.New("abort")
	err = f.database.InTx(f.ctx, func(q db.Querier) error {
		require.NoError(t, f.store.ClearReviewedHead(f.ctx, q, rec.ID))
		return abort
	})
	require.ErrorIs(t, err, abort)
	got, err := f.store.Current(f.ctx, h.ticket.ID, 1)
	require.NoError(t, err)
	assert.Equal(t, headA, got.ReviewedHead)
	assert.Equal(t, "v1:aa", got.ReviewedDiff)
	assert.Equal(t, "base1", got.ReviewedBase)

	require.NoError(t, f.database.InTx(f.ctx, func(q db.Querier) error {
		return f.store.ClearReviewedHead(f.ctx, q, rec.ID)
	}))
	got, err = f.store.Current(f.ctx, h.ticket.ID, 1)
	require.NoError(t, err)
	assert.Empty(t, got.ReviewedHead)
	assert.Empty(t, got.ReviewedDiff, "the fingerprint goes with the head")
	assert.Empty(t, got.ReviewedBase)
}

// A later hand-over replaces the diff recorded by an earlier one; one whose
// diff could not be compared leaves none, never the old one.
func TestStore_SetReviewedReplacesTheWholeRecord(t *testing.T) {
	f := newFixture(t)
	h := f.hold(true)
	f.open(h, false)
	rec, err := f.store.Current(f.ctx, h.ticket.ID, 1)
	require.NoError(t, err)
	set := func(rev *change.Reviewed) {
		require.NoError(t, f.database.InTx(f.ctx, func(q db.Querier) error { return f.store.SetReviewed(f.ctx, q, rec.ID, rev) }))
	}
	set(&change.Reviewed{HeadSHA: headA, DiffFingerprint: "v1:aa", BaseSHA: "base1"})
	set(&change.Reviewed{HeadSHA: headB})
	got, err := f.store.Current(f.ctx, h.ticket.ID, 1)
	require.NoError(t, err)
	assert.Equal(t, headB, got.ReviewedHead)
	assert.Empty(t, got.ReviewedDiff)
	assert.Empty(t, got.ReviewedBase)
}
