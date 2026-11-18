package reports_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/reports"
)

func at(d time.Duration) time.Time { return now.Add(d) }

func ptr(d time.Duration) *time.Time { t := at(d); return &t }

func span(from time.Duration, to *time.Duration) reports.Span {
	s := reports.Span{Start: at(from)}
	if to != nil {
		s.End = ptr(*to)
	}
	return s
}

func dur(d time.Duration) *time.Duration { return &d }

// A ten hour period with two workers: one busy throughout, one busy for the
// first four hours.
func scopeInput() *reports.ScopeCostInput {
	start := -10 * hour
	return &reports.ScopeCostInput{
		From: at(start), To: now,
		Workers: []reports.Span{span(-30*day, nil), span(-30*day, nil)},
		Busy:    []reports.Span{span(-20*day, nil), span(start, dur(-6*hour))},
	}
}

func TestComputeScopeCost_WaitsAreUnionsAndAnOpenIntervalIsNotZero(t *testing.T) {
	t.Parallel()
	in := scopeInput()
	block := func(ticket, blocker int64, path string, from time.Duration, to *time.Duration, reason string) reports.ScopeBlock {
		return reports.ScopeBlock{Span: span(from, to), TicketID: ticket, TicketTitle: fmt.Sprint("t", ticket),
			BlockerID: blocker, BlockerTitle: fmt.Sprint("t", blocker), Path: path, EndReason: reason}
	}
	in.Blocks = []reports.ScopeBlock{
		// Ticket 10 is held back by two scopes at once: 1h-3h and 2h-5h, so 4h.
		block(10, 1, "a/", -9*hour, dur(-7*hour), "claimed"),
		block(10, 2, "b/", -8*hour, dur(-5*hour), "claimed"),
		// Ticket 11 is still held back, from 8h in: 2h to the period's end.
		block(11, 1, "a/", -2*hour, nil, ""),
		// Ticket 12 began before the period and is clipped to its 1h in it.
		block(12, 1, "a/", -15*hour, dur(-9*hour), "claimed"),
	}

	got := reports.ComputeScopeCost(in)

	assert.EqualValues(t, 3, got.Intervals, "ticket 12's began before the period")
	assert.EqualValues(t, 3, got.Waits.Tickets)
	assert.EqualValues(t, 2, got.Waits.Claimed)
	assert.EqualValues(t, 1, got.Waits.Ongoing)
	assert.Equal(t, (7 * hour).Seconds(), got.Waits.TotalSeconds)
	require.NotNil(t, got.Waits.MedianSeconds)
	assert.Equal(t, (2 * hour).Seconds(), *got.Waits.MedianSeconds)

	byID := map[int64]reports.ScopeWait{}
	for _, w := range got.Tickets {
		byID[w.TicketID] = w
	}
	assert.Equal(t, (4 * hour).Seconds(), byID[10].Seconds)
	assert.Equal(t, []int64{1, 2}, byID[10].BlockerIDs)
	assert.Equal(t, (2 * hour).Seconds(), byID[11].Seconds, "still open counts to the end, not zero")
	assert.True(t, byID[11].Ongoing)
	assert.Equal(t, (1 * hour).Seconds(), byID[12].Seconds)
	assert.Equal(t, int64(10), got.Tickets[0].TicketID, "longest first")

	require.Len(t, got.Blockers, 2)
	assert.Equal(t, reports.ScopeBlockerCost{TicketID: 1, Title: "t1", Seconds: (5 * hour).Seconds(), Tickets: 3}, got.Blockers[0])
	assert.Equal(t, reports.ScopeBlockerCost{TicketID: 2, Title: "t2", Seconds: (3 * hour).Seconds(), Tickets: 1}, got.Blockers[1])
	require.Len(t, got.Paths, 2)
	assert.Equal(t, reports.ScopePathCost{Path: "a/", Seconds: (5 * hour).Seconds(), Tickets: 3}, got.Paths[0])
}

func TestComputeScopeCost_IdleCapacityIsOnlyWhileBlocked(t *testing.T) {
	t.Parallel()
	in := scopeInput()
	in.Blocks = []reports.ScopeBlock{
		// Blocked 1h-3h (both workers busy until 4h): costs nothing.
		{Span: span(-9*hour, dur(-7*hour)), TicketID: 10, BlockerID: 1, Path: "a/"},
		// Blocked 5h-6h and from 8h: the second worker is idle from 4h.
		{Span: span(-5*hour, dur(-4*hour)), TicketID: 11, BlockerID: 1, Path: "a/"},
		{Span: span(-2*hour, nil), TicketID: 12, BlockerID: 1, Path: "a/"},
	}

	got := reports.ComputeScopeCost(in)

	assert.Equal(t, (4*hour).Seconds()+(1*hour).Seconds(), got.BlockedSeconds)
	assert.Equal(t, (3 * hour).Seconds(), got.IdleBlockedSeconds)
}

func TestComputeScopeCost_NothingBlockedIsZeroAndNoMedian(t *testing.T) {
	t.Parallel()
	got := reports.ComputeScopeCost(scopeInput())

	assert.Zero(t, got.BlockedSeconds)
	assert.Zero(t, got.IdleBlockedSeconds)
	assert.Nil(t, got.Waits.MedianSeconds)
	assert.Nil(t, got.Denials.MedianSecondsToResolve)
}

func TestComputeScopeCost_Denials(t *testing.T) {
	t.Parallel()
	in := scopeInput()
	denied := at(-5 * hour)
	after := func(d time.Duration) *time.Time { r := denied.Add(d); return &r }
	in.Denials = []reports.ScopeDenial{
		{Path: "docs/", DeniedAt: denied, ResolvedAt: after(time.Minute), Resolution: reports.DenialGranted},
		{Path: "docs/", DeniedAt: denied, ResolvedAt: after(3 * time.Minute), Resolution: reports.DenialGaveUp},
		{Path: "x/", DeniedAt: denied, ResolvedAt: after(5 * time.Minute), Resolution: reports.DenialNarrowed},
		{Path: "x/", DeniedAt: denied},
	}
	in.Narrowings = 4

	got := reports.ComputeScopeCost(in)

	d := got.Denials
	assert.EqualValues(t, 4, d.Total)
	assert.EqualValues(t, 1, d.Granted)
	assert.EqualValues(t, 1, d.Narrowed)
	assert.EqualValues(t, 1, d.GaveUp)
	assert.EqualValues(t, 1, d.Open, "unresolved is open, not resolved at zero")
	require.NotNil(t, d.MedianSecondsToResolve)
	assert.Equal(t, 180.0, *d.MedianSecondsToResolve, "over the three resolved ones")
	assert.Equal(t, []reports.ScopePathCount{{Path: "docs/", Count: 2}, {Path: "x/", Count: 2}}, d.Paths)
	assert.EqualValues(t, 4, got.Narrowings)
}

func (f *fixture) scopeTicket(t *testing.T, title string) int64 {
	t.Helper()
	var id int64
	require.NoError(t, f.database.QueryRow(f.ctx,
		`INSERT INTO tickets (title, status) VALUES ($1, 'open') RETURNING id`, title).Scan(&id))
	return id
}

func (f *fixture) worker(t *testing.T, name string, created time.Time) uuid.UUID {
	t.Helper()
	id := uuid.New()
	_, err := f.database.Exec(f.ctx, `INSERT INTO agents (id, name, created_at) VALUES ($1, $2, $3)`, id, name, created)
	require.NoError(t, err)
	return id
}

func TestScopeCostRows_ReadsTheRecordedDecisionsOfThePeriod(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	held, wants, other := f.scopeTicket(t, "Holder"), f.scopeTicket(t, "Wants"), f.scopeTicket(t, "Other")
	busy := f.worker(t, "busy", at(-30*day))

	exec := func(sql string, args ...any) {
		t.Helper()
		_, err := f.database.Exec(f.ctx, sql, args...)
		require.NoError(t, err)
	}
	exec(`INSERT INTO leases (ticket_id, attempt_number, agent_id, acquired_at, expires_at, released_at) VALUES ($1, 1, $2, $3, $4, $5)`,
		held, busy, at(-20*hour), at(-19*hour), at(-1*hour))
	// In the period, still open, and one that ended before it began.
	exec(`INSERT INTO scope_blocks (ticket_id, blocking_ticket_id, path, started_at) VALUES ($1, $2, 'a/', $3)`, wants, held, at(-10*hour))
	exec(`INSERT INTO scope_blocks (ticket_id, blocking_ticket_id, path, started_at, ended_at, end_reason) VALUES ($1, $2, 'a/', $3, $4, 'claimed')`,
		other, held, at(-3*day), at(-2*day))
	exec(`INSERT INTO scope_extension_denials (ticket_id, agent_id, path, blocking_ticket_id, blocking_path, denied_at) VALUES ($1, $2, 'a/x', $3, 'a/', $4)`,
		held, busy, wants, at(-10*hour))
	exec(`INSERT INTO scope_extension_denials (ticket_id, agent_id, path, blocking_ticket_id, blocking_path, denied_at) VALUES ($1, $2, 'a/y', $3, 'a/', $4)`,
		held, busy, wants, at(-9*day))
	exec(`INSERT INTO scope_narrowings (ticket_id, agent_id, removed, kept, narrowed_at) VALUES ($1, $2, 1, 0, $3)`, held, busy, at(-2*hour))

	in, err := f.store.ScopeCostRows(f.ctx, at(-24*hour), now)
	require.NoError(t, err)
	got := reports.ComputeScopeCost(in)

	assert.EqualValues(t, 1, got.Waits.Tickets, "the interval that ended before the period is not in it")
	assert.EqualValues(t, 1, got.Waits.Ongoing)
	assert.Equal(t, (10 * hour).Seconds(), got.Waits.TotalSeconds)
	assert.EqualValues(t, 1, got.Denials.Total, "only the denial inside the period")
	assert.EqualValues(t, 1, got.Denials.GaveUp, "its lease ended, so the agent gave up")
	assert.Equal(t, (9 * hour).Seconds(), *got.Denials.MedianSecondsToResolve)
	assert.EqualValues(t, 1, got.Narrowings)
	// Blocked from -10h, and the only worker held its lease until -1h: it
	// was idle for the last hour only.
	assert.Equal(t, (1 * hour).Seconds(), got.IdleBlockedSeconds)
}
