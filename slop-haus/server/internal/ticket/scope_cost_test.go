package ticket_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

type blockRow struct {
	Ticket, Blocker int64
	Path, Reason    string
	Open            bool
}

func (f *fixture) scopeBlocks(t *testing.T) []blockRow {
	t.Helper()
	rows, err := f.database.Query(f.ctx, `
		SELECT ticket_id, blocking_ticket_id, path, COALESCE(end_reason, ''), ended_at IS NULL
		  FROM scope_blocks ORDER BY id`)
	require.NoError(t, err)
	defer rows.Close()
	var out []blockRow
	for rows.Next() {
		var r blockRow
		require.NoError(t, rows.Scan(&r.Ticket, &r.Blocker, &r.Path, &r.Reason, &r.Open))
		out = append(out, r)
	}
	require.NoError(t, rows.Err())
	return out
}

func TestScopeBlocks_OneIntervalAcrossManyPollsClosedByTheClaim(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a, b := f.agent(t, "builder-1"), f.agent(t, "builder-2")
	held := f.ticket(t, "Holder", "server/internal/db/")
	wants := f.ticket(t, "Wants", "server/internal/db/x.go")
	f.claimOnly(t, a, held.ID)

	require.NotNil(t, f.claimOnly(t, b, wants.ID).Blocked)
	started := f.blockStart(t)
	for range 2 {
		require.NotNil(t, f.claimOnly(t, b, wants.ID).Blocked)
	}
	assert.Equal(t, started, f.blockStart(t), "polling does not move the start of the interval")
	assert.Equal(t, []blockRow{{wants.ID, held.ID, "server/internal/db/", "", true}}, f.scopeBlocks(t),
		"three polls are one open interval")

	_, err := f.svc.Release(f.ctx, a, "done", nil)
	require.NoError(t, err)
	require.NotNil(t, f.claimOnly(t, b, wants.ID).Ticket)
	assert.Equal(t, []blockRow{{wants.ID, held.ID, "server/internal/db/", "claimed", false}}, f.scopeBlocks(t))
}

func TestScopeBlocks_StaysOpenUntilTheTicketItselfIsClaimed(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a, b, c := f.agent(t, "builder-1"), f.agent(t, "builder-2"), f.agent(t, "builder-3")
	held := f.ticket(t, "Holder", "a/")
	wants := f.ticket(t, "Wants", "a/x.go")
	f.claimOnly(t, a, held.ID)
	require.NotNil(t, f.claimOnly(t, b, wants.ID).Blocked)

	_, err := f.svc.Release(f.ctx, a, "done", nil)
	require.NoError(t, err)
	_, err = f.database.Exec(f.ctx, `UPDATE tickets SET status = 'cancelled' WHERE id = $1`, held.ID)
	require.NoError(t, err)
	// A directed claim of another ticket does not look at this one.
	other := f.ticket(t, "Other", "b/")
	require.NotNil(t, f.claimOnly(t, c, other.ID).Ticket)

	rows := f.scopeBlocks(t)
	require.Len(t, rows, 1)
	assert.True(t, rows[0].Open)

	result, err := f.svc.Claim(f.ctx, b, nil)
	require.NoError(t, err)
	require.NotNil(t, result.Ticket)
	assert.Equal(t, "claimed", f.scopeBlocks(t)[0].Reason)
}

func TestScopeBlocks_LeavingTheQueueEndsTheInterval(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a, b := f.agent(t, "builder-1"), f.agent(t, "builder-2")
	held := f.ticket(t, "Holder", "a/")
	wants := f.ticket(t, "Wants", "a/x.go")
	f.claimOnly(t, a, held.ID)
	require.NotNil(t, f.claimOnly(t, b, wants.ID).Blocked)

	_, err := f.database.Exec(f.ctx, `UPDATE tickets SET status = 'cancelled' WHERE id = $1`, wants.ID)
	require.NoError(t, err)
	// Any claim sweeps the tickets that have left the queue.
	result, err := f.svc.Claim(f.ctx, b, nil)
	require.NoError(t, err)
	require.Nil(t, result.Ticket)
	rows := f.scopeBlocks(t)
	require.Len(t, rows, 1)
	assert.Equal(t, "cleared", rows[0].Reason)
}

func TestScopeBlocks_ACooldownOrADependencyEndsTheInterval(t *testing.T) {
	t.Parallel()
	for name, settle := range map[string]string{
		"cooldown":   `UPDATE tickets SET blocked_until = now() + interval '1 hour' WHERE id = $1`,
		"dependency": `INSERT INTO ticket_deps (ticket_id, depends_on_ticket_id) SELECT $1, id FROM tickets WHERE id <> $1 AND title = 'Dep'`,
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			a, b := f.agent(t, "builder-1"), f.agent(t, "builder-2")
			held := f.ticket(t, "Holder", "a/")
			wants := f.ticket(t, "Wants", "a/x.go")
			f.ticket(t, "Dep", "z/")
			f.claimOnly(t, a, held.ID)
			require.NotNil(t, f.claimOnly(t, b, wants.ID).Blocked)

			_, err := f.database.Exec(f.ctx, settle, wants.ID)
			require.NoError(t, err)
			_, err = f.svc.Claim(f.ctx, b, nil)
			require.NoError(t, err)

			rows := f.scopeBlocks(t)
			require.Len(t, rows, 1)
			assert.Equal(t, "cleared", rows[0].Reason)
		})
	}
}

func TestScopeBlocks_OnlyBlockedByScopeIsRecorded(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a, b := f.agent(t, "builder-1"), f.agent(t, "builder-2")
	f.ticket(t, "Free", "a/")
	_ = a
	result, err := f.svc.Claim(f.ctx, b, nil)
	require.NoError(t, err)
	require.NotNil(t, result.Ticket)
	assert.Empty(t, f.scopeBlocks(t))
}

func (f *fixture) blockStart(t *testing.T) time.Time {
	t.Helper()
	var at time.Time
	require.NoError(t, f.database.QueryRow(f.ctx, `SELECT started_at FROM scope_blocks ORDER BY id LIMIT 1`).Scan(&at))
	return at
}

func TestScopeBlocks_ABlockedCandidateIsRecordedWhenALaterOneIsGranted(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a, b := f.agent(t, "builder-1"), f.agent(t, "builder-2")
	held := f.ticket(t, "Holder", "a/")
	wants := f.ticket(t, "Wants", "a/x.go")
	free := f.ticket(t, "Free", "z/")
	f.claimOnly(t, a, held.ID)

	result, err := f.svc.Claim(f.ctx, b, nil)
	require.NoError(t, err)
	require.NotNil(t, result.Ticket)
	assert.Equal(t, free.ID, result.Ticket.ID, "the free ticket behind the blocked one is granted")
	assert.Equal(t, []blockRow{{wants.ID, held.ID, "a/", "", true}}, f.scopeBlocks(t))
}

type denialRow struct {
	Ticket, Blocker           int64
	Path, BlockingPath, Resol string
}

func (f *fixture) denials(t *testing.T) []denialRow {
	t.Helper()
	rows, err := f.database.Query(f.ctx, `
		SELECT ticket_id, blocking_ticket_id, path, blocking_path, COALESCE(resolution, '')
		  FROM scope_extension_denials ORDER BY id`)
	require.NoError(t, err)
	defer rows.Close()
	var out []denialRow
	for rows.Next() {
		var r denialRow
		require.NoError(t, rows.Scan(&r.Ticket, &r.Blocker, &r.Path, &r.BlockingPath, &r.Resol))
		out = append(out, r)
	}
	require.NoError(t, rows.Err())
	return out
}

func TestScopeExtensionDenial_RecordedThenResolvedByAGrant(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a, b := f.agent(t, "builder-1"), f.agent(t, "builder-2")
	mine := f.ticket(t, "Mine", "a/")
	theirs := f.ticket(t, "Theirs", "docs/")
	f.claimOnly(t, a, mine.ID)
	f.claimOnly(t, b, theirs.ID)

	res, err := f.svc.ExtendScope(f.ctx, a, []model.Path{{Path: "docs/guide.md"}})
	require.NoError(t, err)
	require.NotNil(t, res.Denied)
	assert.Equal(t, []denialRow{{mine.ID, theirs.ID, "docs/guide.md", "docs/", ""}}, f.denials(t))

	_, err = f.svc.Release(f.ctx, b, "done", nil)
	require.NoError(t, err)
	res, err = f.svc.ExtendScope(f.ctx, a, []model.Path{{Path: "docs/guide.md"}})
	require.NoError(t, err)
	require.True(t, res.Granted)
	assert.Equal(t, "granted", f.denials(t)[0].Resol)
}

func TestScopeExtensionDenial_ADirectoryGrantResolvesTheFileDenied(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a, b := f.agent(t, "builder-1"), f.agent(t, "builder-2")
	mine := f.ticket(t, "Mine", "a/")
	theirs := f.ticket(t, "Theirs", "docs/")
	f.claimOnly(t, a, mine.ID)
	f.claimOnly(t, b, theirs.ID)
	res, err := f.svc.ExtendScope(f.ctx, a, []model.Path{{Path: "docs/guide.md"}})
	require.NoError(t, err)
	require.NotNil(t, res.Denied)

	_, err = f.svc.Release(f.ctx, b, "done", nil)
	require.NoError(t, err)
	res, err = f.svc.ExtendScope(f.ctx, a, []model.Path{{Path: "docs/"}})
	require.NoError(t, err)
	require.True(t, res.Granted)
	assert.Equal(t, "granted", f.denials(t)[0].Resol)
}

func TestScopeExtensionDenial_ANewHoldersGrantDoesNotResolveThePreviousHoldersRefusal(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a, b, c := f.agent(t, "builder-1"), f.agent(t, "builder-2"), f.agent(t, "builder-3")
	mine := f.ticket(t, "Mine", "a/")
	theirs := f.ticket(t, "Theirs", "docs/")
	f.claimOnly(t, a, mine.ID)
	f.claimOnly(t, b, theirs.ID)
	res, err := f.svc.ExtendScope(f.ctx, a, []model.Path{{Path: "docs/guide.md"}})
	require.NoError(t, err)
	require.NotNil(t, res.Denied)

	_, err = f.svc.Release(f.ctx, b, "done", nil)
	require.NoError(t, err)
	_, err = f.svc.Release(f.ctx, a, "gave up", nil)
	require.NoError(t, err)
	f.claimOnly(t, c, mine.ID)
	res, err = f.svc.ExtendScope(f.ctx, c, []model.Path{{Path: "docs/guide.md"}})
	require.NoError(t, err)
	require.True(t, res.Granted)

	assert.Equal(t, "", f.denials(t)[0].Resol, "the first agent's refusal is not the second's grant")
}

func TestScopeExtensionDenial_ResolvedByNarrowingAndNarrowingIsCounted(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a, b := f.agent(t, "builder-1"), f.agent(t, "builder-2")
	mine := f.ticket(t, "Mine", "a/", "c/")
	theirs := f.ticket(t, "Theirs", "docs/")
	f.claimOnly(t, a, mine.ID)
	f.claimOnly(t, b, theirs.ID)

	res, err := f.svc.ExtendScope(f.ctx, a, []model.Path{{Path: "docs/guide.md"}})
	require.NoError(t, err)
	require.NotNil(t, res.Denied)
	_, err = f.svc.NarrowScope(f.ctx, a, []string{"c/"}, nil)
	require.NoError(t, err)

	assert.Equal(t, "narrowed", f.denials(t)[0].Resol)
	var n, removed int
	require.NoError(t, f.database.QueryRow(f.ctx, `SELECT count(*), COALESCE(sum(removed), 0) FROM scope_narrowings WHERE ticket_id = $1`, mine.ID).Scan(&n, &removed))
	assert.Equal(t, 1, n)
	assert.Equal(t, 1, removed)
}
