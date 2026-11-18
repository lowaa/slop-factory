package reports_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/reports"
)

type shadowRow struct {
	decision, reason string
	decided          time.Duration
	// ci is nil while the checks have no outcome.
	ci *bool
}

func yes() *bool { v := true; return &v }
func no() *bool  { v := false; return &v }

// seedShadow adds a ticket, its change and the rows, returning the ticket id.
func seedShadow(t *testing.T, ctx context.Context, database db.Querier, title string, rows ...shadowRow) int64 {
	t.Helper()
	var ticket, change int64
	require.NoError(t, database.QueryRow(ctx, `INSERT INTO tickets (title, status) VALUES ($1, 'open') RETURNING id`, title).Scan(&ticket))
	agent := uuid.New()
	_, err := database.Exec(ctx, `INSERT INTO agents (id, name, created_at) VALUES ($1, $2, $3)`, agent, title, at(-100*day))
	require.NoError(t, err)
	require.NoError(t, database.QueryRow(ctx, `
INSERT INTO ticket_changes (ticket_id, attempt_number, host, owner, repo, number, head_branch, base_branch, draft, opened_by_agent_id, opened_at)
VALUES ($1, 1, 'github.com', 'acme', 'app', $1, 'b', 'main', false, $2, $3) RETURNING id`, ticket, agent, at(-100*day)).Scan(&change))
	for i, r := range rows {
		head := title + "-head-" + string(rune('a'+i))
		_, err = database.Exec(ctx, `
INSERT INTO merge_drift_decisions (ticket_id, change_id, decision, reason, drift_dirs, head_sha, base_tip_sha, enforced, decided_at,
                                   ci_head_sha, ci_passed, ci_recorded_at)
VALUES ($1, $2, $3, $4, '{billing/}', $5, 'tip', false, $6,
        CASE WHEN $7::boolean IS NULL THEN NULL ELSE $5 || '-ci' END, $7, CASE WHEN $7::boolean IS NULL THEN NULL ELSE $6::timestamptz + interval '1 hour' END)`,
			ticket, change, r.decision, r.reason, head, at(r.decided), r.ci)
		require.NoError(t, err)
	}
	return ticket
}

func TestShadowDecisions_CountsByDecisionReasonAndCIOutcome(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	const ok, bad = "the base moved only in independent apps", "go.work is not in an independent app"
	broken := seedShadow(t, f.ctx, f.database, "Broken",
		shadowRow{"would_accept", ok, -2 * hour, no()},
		shadowRow{"would_accept", ok, -3 * hour, yes()},
		shadowRow{"would_accept", ok, -4 * hour, nil}, // checks not concluded yet
		shadowRow{"would_refuse", bad, -5 * hour, no()},
		// Outside the period, either side.
		shadowRow{"would_accept", ok, -8 * day, no()},
		shadowRow{"would_accept", ok, 1 * hour, no()},
	)
	seedShadow(t, f.ctx, f.database, "Other", shadowRow{"would_refuse", bad, -1 * hour, nil})

	got, err := f.store.ShadowDecisions(f.ctx, at(-7*day), now)
	require.NoError(t, err)

	assert.EqualValues(t, 3, got.WouldAccept)
	assert.EqualValues(t, 2, got.WouldRefuse)
	assert.EqualValues(t, 1, got.AcceptCIPassed)
	assert.EqualValues(t, 1, got.AcceptCIFailed)
	assert.EqualValues(t, 1, got.AcceptAwaitingCI, "pending is counted apart, not as a pass")
	assert.Equal(t, []reports.ShadowReason{
		{Decision: "would_accept", Reason: ok, Count: 3},
		{Decision: "would_refuse", Reason: bad, Count: 2},
	}, got.Reasons)
	require.Len(t, got.Failures, 1, "a refused decision with a failed CI is not a failure of the rule")
	fail := got.Failures[0]
	assert.Equal(t, broken, fail.TicketID)
	assert.Equal(t, "Broken", fail.Title)
	assert.NotZero(t, fail.ChangeID)
	assert.Equal(t, "Broken-head-a", fail.HeadSHA)
	assert.Equal(t, "Broken-head-a-ci", fail.CIHeadSHA)
	assert.Equal(t, []string{"billing/"}, fail.DriftDirs)
	assert.True(t, at(-2*hour).Equal(fail.DecidedAt), fail.DecidedAt)
	assert.True(t, at(-hour).Equal(fail.CIRecordedAt), fail.CIRecordedAt)
}

func TestShadowDecisions_EmptyIsZeroAndEmptyLists(t *testing.T) {
	t.Parallel()
	f := newFixture(t)

	got, err := f.store.ShadowDecisions(f.ctx, at(-7*day), now)
	require.NoError(t, err)

	assert.Zero(t, got.WouldAccept+got.WouldRefuse+got.AcceptAwaitingCI)
	assert.NotNil(t, got.Reasons)
	assert.NotNil(t, got.Failures)
}

func TestShadowDecisions_ListsTheMostRecentFailuresUpToTheCap(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	rows := make([]shadowRow, reports.MaxShadowFailures+2)
	for i := range rows {
		rows[i] = shadowRow{"would_accept", "r", -time.Duration(i+1) * time.Minute, no()}
	}
	seedShadow(t, f.ctx, f.database, "Many", rows...)

	got, err := f.store.ShadowDecisions(f.ctx, at(-7*day), now)
	require.NoError(t, err)

	assert.EqualValues(t, reports.MaxShadowFailures+2, got.AcceptCIFailed, "the count is of all, the list is capped")
	require.Len(t, got.Failures, reports.MaxShadowFailures)
	assert.Equal(t, "Many-head-a", got.Failures[0].HeadSHA, "newest first")
}

func TestShadowDecisions_TenantIsolation(t *testing.T) {
	t.Parallel()
	app, owner := dbtest.WithOwnerDB(t)
	a := db.WithTenant(context.Background(), dbtest.NewTenant(t, app))
	b := db.WithTenant(context.Background(), dbtest.NewTenant(t, app))
	store := reports.NewPostgresStore(app, 0)
	seedShadow(t, a, app, "A", shadowRow{"would_accept", "r", -hour, no()})
	seedShadow(t, b, app, "B", shadowRow{"would_accept", "r", -hour, no()}, shadowRow{"would_refuse", "x", -hour, nil})

	var everyone int
	require.NoError(t, owner.Global().QueryRow(context.Background(), `SELECT count(*) FROM merge_drift_decisions`).Scan(&everyone))
	require.Equal(t, 3, everyone)

	got, err := store.ShadowDecisions(a, at(-7*day), now)
	require.NoError(t, err)

	assert.EqualValues(t, 1, got.WouldAccept)
	assert.Zero(t, got.WouldRefuse)
	assert.EqualValues(t, 1, got.AcceptCIFailed)
	require.Len(t, got.Failures, 1)
	assert.Equal(t, "A", got.Failures[0].Title)
	require.Len(t, got.Reasons, 1)
}
