package landing_test

import (
	"context"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/landing"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
)

type storeFixture struct {
	database *db.DB
	store    *landing.Store
	ctx      context.Context
}

func newStoreFixture(t *testing.T) *storeFixture {
	t.Helper()
	database := dbtest.WithDB(t)
	return &storeFixture{
		database: database,
		store:    landing.NewStore(database),
		ctx:      db.WithTenant(context.Background(), dbtest.NewTenant(t, database)),
	}
}

func (f *storeFixture) ticket(t *testing.T) int64 {
	t.Helper()
	var id int64
	require.NoError(t, f.database.QueryRow(f.ctx, `INSERT INTO tickets (title, status) VALUES ('t', 'open') RETURNING id`).Scan(&id))
	return id
}

func rec(ticket int64, o landing.Outcome) *landing.Record {
	return &landing.Record{TicketID: ticket, AttemptNumber: 1, ChangeURL: "https://example.invalid/pr/1", HeadSHA: "abc", Outcome: o, Detail: "why"}
}

var wide = struct{ from, to time.Time }{time.Now().Add(-time.Hour), time.Now().Add(time.Hour)}

func TestEveryOutcomeRecordsAndReadsBack(t *testing.T) {
	f := newStoreFixture(t)
	ticket := f.ticket(t)
	user := dbtest.NewUser(t, f.database)
	approved := time.Now().Add(-time.Minute).UTC().Truncate(time.Microsecond)
	for _, o := range landing.All {
		r := rec(ticket, o)
		r.ApproverUserID, r.ApprovedAt = &user, &approved
		depth := 3
		r.QueueDepth = &depth
		r.EnqueuedAt = &approved
		require.NoError(t, f.store.Record(f.ctx, r), o)
	}
	got, err := f.store.List(f.ctx, wide.from, wide.to, 100)
	require.NoError(t, err)
	require.Len(t, got, len(landing.All))
	// Newest first, so the last recorded comes first.
	for i, o := range landing.All {
		r := got[len(got)-1-i]
		assert.Equal(t, o, r.Outcome)
		assert.Equal(t, int64(ticket), r.TicketID)
		assert.Equal(t, 1, r.AttemptNumber)
		assert.Equal(t, "https://example.invalid/pr/1", r.ChangeURL)
		assert.Equal(t, "abc", r.HeadSHA)
		assert.Equal(t, "why", r.Detail)
		require.NotNil(t, r.ApproverUserID)
		assert.Equal(t, user, *r.ApproverUserID)
		assert.True(t, approved.Equal(*r.ApprovedAt))
		require.NotNil(t, r.QueueDepth)
		assert.Equal(t, 3, *r.QueueDepth)
		require.NotNil(t, r.EnqueuedAt)
		assert.True(t, approved.Equal(*r.EnqueuedAt))
	}
}

func TestRecordWithoutApproval(t *testing.T) {
	f := newStoreFixture(t)
	require.NoError(t, f.store.Record(f.ctx, rec(f.ticket(t), landing.NotApprovedForHead)))
	got, err := f.store.List(f.ctx, wide.from, wide.to, 10)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Nil(t, got[0].ApproverUserID)
	assert.Nil(t, got[0].ApprovedAt)
	assert.Nil(t, got[0].QueueDepth)
	assert.Nil(t, got[0].EnqueuedAt)
}

func TestUnknownOutcomeIsRefusedByStoreAndByTheTable(t *testing.T) {
	f := newStoreFixture(t)
	ticket := f.ticket(t)
	err := f.store.Record(f.ctx, rec(ticket, "bogus"))
	assert.ErrorIs(t, err, landing.ErrInvalid)

	_, err = f.database.Exec(f.ctx, `INSERT INTO server_merge_outcomes (ticket_id, attempt_number, change_url, head_sha, outcome)
		VALUES ($1, 1, 'u', 'h', 'bogus')`, ticket)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "server_merge_outcomes_outcome_check")
}

func TestOutcomesAreIsolatedByTenant(t *testing.T) {
	f := newStoreFixture(t)
	require.NoError(t, f.store.Record(f.ctx, rec(f.ticket(t), landing.Merged)))

	other := &storeFixture{database: f.database, store: f.store,
		ctx: db.WithTenant(context.Background(), dbtest.NewTenant(t, f.database))}
	got, err := other.store.List(other.ctx, wide.from, wide.to, 10)
	require.NoError(t, err)
	assert.Empty(t, got)
	counts, err := other.store.CountByOutcome(other.ctx, wide.from, wide.to)
	require.NoError(t, err)
	for _, c := range counts {
		assert.Zero(t, c.N, c.Outcome)
	}
}

func TestCountByOutcomeOverATimeWindow(t *testing.T) {
	f := newStoreFixture(t)
	ticket := f.ticket(t)
	base := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	add := func(o landing.Outcome, at time.Time) {
		r := rec(ticket, o)
		require.NoError(t, f.store.Record(f.ctx, r))
		_, err := f.database.Exec(f.ctx, `UPDATE server_merge_outcomes SET created_at = $1 WHERE id = (SELECT max(id) FROM server_merge_outcomes)`, at)
		require.NoError(t, err)
	}
	add(landing.Merged, base)
	add(landing.Merged, base.Add(time.Hour))
	add(landing.ChecksRed, base.Add(2*time.Hour))
	add(landing.Merged, base.Add(-time.Second))   // before the window
	add(landing.ChecksRed, base.Add(3*time.Hour)) // at the exclusive end

	counts, err := f.store.CountByOutcome(f.ctx, base, base.Add(3*time.Hour))
	require.NoError(t, err)
	got := map[landing.Outcome]int64{}
	for _, c := range counts {
		got[c.Outcome] = c.N
	}
	assert.Len(t, counts, len(landing.All))
	assert.EqualValues(t, 2, got[landing.Merged])
	assert.EqualValues(t, 1, got[landing.ChecksRed])
	assert.EqualValues(t, 0, got[landing.LockTimeout])
}

func TestAnAttemptThatDoesNotExistIsRefused(t *testing.T) {
	f := newStoreFixture(t)
	r := rec(f.ticket(t), landing.Merged)
	r.AttemptNumber = 7
	require.Error(t, f.store.Record(f.ctx, r))
}

func TestListIsNewestFirstWithinTheWindowAndLimit(t *testing.T) {
	f := newStoreFixture(t)
	ticket := f.ticket(t)
	base := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	for i, o := range []landing.Outcome{landing.LockTimeout, landing.Merged, landing.ChecksRed, landing.MainMoved} {
		require.NoError(t, f.store.Record(f.ctx, rec(ticket, o)))
		_, err := f.database.Exec(f.ctx, `UPDATE server_merge_outcomes SET created_at = $1 WHERE id = (SELECT max(id) FROM server_merge_outcomes)`,
			base.Add(time.Duration(i)*time.Hour))
		require.NoError(t, err)
	}
	// The first (hour 0) is before the window; the last (hour 3) is its exclusive end.
	got, err := f.store.List(f.ctx, base.Add(time.Hour), base.Add(3*time.Hour), 10)
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, landing.ChecksRed, got[0].Outcome)
	assert.Equal(t, landing.Merged, got[1].Outcome)

	got, err = f.store.List(f.ctx, base, base.Add(4*time.Hour), 1)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, landing.MainMoved, got[0].Outcome)
}
