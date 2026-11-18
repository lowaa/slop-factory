package usage_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/attemptsignals/usage"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

type fixture struct {
	database *db.DB
	ctx      context.Context
	agent    uuid.UUID
	t        *testing.T
}

func newFixture(t *testing.T, database *db.DB) *fixture {
	t.Helper()
	f := &fixture{database: database, t: t, ctx: db.WithTenant(context.Background(), dbtest.NewTenant(t, database)), agent: uuid.New()}
	f.exec(`INSERT INTO agents (id, name) VALUES ($1, 'a')`, f.agent)
	return f
}

func (f *fixture) exec(sql string, args ...any) {
	f.t.Helper()
	_, err := f.database.Exec(f.ctx, sql, args...)
	require.NoError(f.t, err)
}

func (f *fixture) ticket() int64 {
	f.t.Helper()
	var id int64
	require.NoError(f.t, f.database.QueryRow(f.ctx, `INSERT INTO tickets (title, status) VALUES ('t', 'open') RETURNING id`).Scan(&id))
	return id
}

func (f *fixture) attempt(ticketID int64, number int) {
	f.exec(`INSERT INTO attempts (ticket_id, number, status) VALUES ($1, $2, 'queued')`, ticketID, number)
}

// lease inserts an ended lease, or a live one when end is nil. The one-live-
// lease-per-agent rule is why callers use a fresh agent for a live lease.
func (f *fixture) lease(ticketID int64, attempt int, agent uuid.UUID, start time.Time, end *time.Time) {
	f.exec(`INSERT INTO leases (ticket_id, attempt_number, agent_id, acquired_at, expires_at, released_at, release_reason)
	        VALUES ($1, $2, $3, $4::timestamptz, CASE WHEN $5::timestamptz IS NULL THEN now() + interval '1 day' ELSE $4::timestamptz + interval '1 day' END, $5, 'x')`, ticketID, attempt, agent, start, end)
}

func (f *fixture) usage(ticketID int64, attempt int, key string, requests int64, est int64) {
	f.exec(`
INSERT INTO llm_usage (agent_id, transcript_key, harness, session_id, scope_hint, scope, ticket_id, attempt_number, provider, model,
    billing_mode, requests, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, reasoning_tokens,
    estimated_cost_microdollars, price_version, unpriced, started_at, ended_at)
VALUES ($1, $2, 'pi', 's', 'main', 'ticket', $3, $4, 'p', 'm1', 'subscription', $5, 1, 1, 0, 0, 0, $6, 'v', false, now(), now())`,
		f.agent, key, ticketID, attempt, requests, est)
}

func (f *fixture) signals(refs ...ticket.AttemptRef) map[ticket.AttemptRef]*usage.Signals {
	f.t.Helper()
	got, err := usage.NewStore(f.database).Signals(f.ctx, refs)
	require.NoError(f.t, err)
	return got
}

var t0 = time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)

func at(d time.Duration) *time.Time { v := t0.Add(d); return &v }

func TestSignalsMeasureOneAttemptAlone(t *testing.T) {
	f := newFixture(t, dbtest.WithDB(t))
	tk := f.ticket()
	f.attempt(tk, 2)
	other := uuid.New()
	f.exec(`INSERT INTO agents (id, name) VALUES ($1, 'b')`, other)

	f.lease(tk, 1, f.agent, t0, at(time.Hour))
	f.lease(tk, 1, f.agent, t0.Add(2*time.Hour), at(3*time.Hour))
	f.lease(tk, 2, other, t0.Add(10*time.Hour), at(11*time.Hour))
	f.usage(tk, 1, "a", 5, 40)
	f.usage(tk, 1, "b", 2, 2)
	f.usage(tk, 2, "c", 100, 999)

	// Attempt 1: one discussion, one blocked handback and one refused
	// extension, each inside its leases. Attempt 2's agent also discussed.
	f.exec(`INSERT INTO attempt_status_changes (ticket_id, attempt_number, from_status, to_status, priority, actor_kind, actor_id)
	        VALUES ($1, 1, 'in_progress', 'discussion', 'normal', 'agent', $2),
	               ($1, 1, 'in_progress', 'discussion', 'normal', 'user', NULL),
	               ($1, 2, 'in_progress', 'discussion', 'normal', 'agent', $3)`, tk, f.agent, other)
	f.exec(`INSERT INTO ticket_status_changes (ticket_id, from_status, to_status, priority, actor_kind, actor_id, changed_at)
	        VALUES ($1, 'in_progress', 'blocked', 'normal', 'agent', $2, $3),
	               ($1, 'in_progress', 'blocked', 'normal', 'agent', $4, $5),
	               ($1, 'in_progress', 'blocked', 'normal', 'agent', $2, $6)`,
		tk, f.agent, t0.Add(30*time.Minute), other, t0.Add(10*time.Hour+time.Minute), t0.Add(5*time.Hour))
	f.exec(`INSERT INTO tickets (title, status) VALUES ('blocker', 'open')`)
	var blocker int64
	require.NoError(t, f.database.QueryRow(f.ctx, `SELECT max(id) FROM tickets`).Scan(&blocker))
	f.exec(`INSERT INTO scope_extension_denials (ticket_id, agent_id, path, blocking_ticket_id, blocking_path, denied_at)
	        VALUES ($1, $2, 'p', $3, 'p', $4), ($1, $2, 'q', $3, 'q', $5)`,
		tk, f.agent, blocker, t0.Add(2*time.Hour+time.Minute), t0.Add(6*time.Hour))

	got := f.signals(ticket.AttemptRef{TicketID: tk, Number: 1}, ticket.AttemptRef{TicketID: tk, Number: 2})
	one := got[ticket.AttemptRef{TicketID: tk, Number: 1}]
	require.NotNil(t, one)
	assert.EqualValues(t, 2, *one.Runs)
	assert.EqualValues(t, 7, *one.Turns)
	require.Len(t, one.Cost, 1)
	assert.EqualValues(t, 42, *one.Cost[0].CostMicros)
	assert.Equal(t, t0, one.FirstClaimAt.UTC())
	assert.Equal(t, 3*time.Hour, *one.WallClock)
	assert.Equal(t, *at(3 * time.Hour), one.LastRunEndedAt.UTC())
	assert.EqualValues(t, 1, *one.Discussions, "a person's move into discussion is not the agent's")
	assert.EqualValues(t, 1, *one.BlockedHandbacks, "the move outside every lease is no handback")
	assert.EqualValues(t, 1, *one.ScopeExtensionDenials)

	two := got[ticket.AttemptRef{TicketID: tk, Number: 2}]
	assert.EqualValues(t, 1, *two.Runs)
	assert.EqualValues(t, 100, *two.Turns)
	assert.Equal(t, time.Hour, *two.WallClock)
	assert.EqualValues(t, 1, *two.Discussions)
	assert.EqualValues(t, 1, *two.BlockedHandbacks)
	assert.EqualValues(t, 0, *two.ScopeExtensionDenials)
}

func TestSignalsAreUnknownWhereNothingWasRecorded(t *testing.T) {
	f := newFixture(t, dbtest.WithDB(t))
	never, live, spentOnly := f.ticket(), f.ticket(), f.ticket()
	f.lease(live, 1, f.agent, t0, nil)
	f.usage(spentOnly, 1, "u", 3, 1)

	got := f.signals(ticket.AttemptRef{TicketID: never, Number: 1},
		ticket.AttemptRef{TicketID: live, Number: 1}, ticket.AttemptRef{TicketID: spentOnly, Number: 1})

	assert.NotContains(t, got, ticket.AttemptRef{TicketID: never, Number: 1}, "no run and no usage: nothing to report")

	l := got[ticket.AttemptRef{TicketID: live, Number: 1}]
	assert.EqualValues(t, 1, *l.Runs)
	assert.Nil(t, l.WallClock, "a live lease has no end yet")
	assert.Nil(t, l.Turns, "no usage reported is not zero turns")
	assert.Empty(t, l.Cost)

	s := got[ticket.AttemptRef{TicketID: spentOnly, Number: 1}]
	assert.EqualValues(t, 3, *s.Turns)
	assert.Nil(t, s.Runs)
	assert.Nil(t, s.Discussions, "an attempt that never ran has no autonomy figures")
}

func TestSignalsNeverReadAnotherTenant(t *testing.T) {
	database := dbtest.WithDB(t)
	mine, theirs := newFixture(t, database), newFixture(t, database)
	tk := theirs.ticket()
	theirs.lease(tk, 1, theirs.agent, t0, at(time.Hour))

	got, err := usage.NewStore(database).Signals(mine.ctx, []ticket.AttemptRef{{TicketID: tk, Number: 1}})
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestSignalsOfNoRefsIsEmpty(t *testing.T) {
	f := newFixture(t, dbtest.WithDB(t))
	assert.Empty(t, f.signals())
}

// A lease nobody released stopped at its expiry, and one released late by the
// sweep did too; a live lease beside ended ones leaves the end unknown.
func TestSignalsEndAnExpiredLeaseAtItsExpiry(t *testing.T) {
	f := newFixture(t, dbtest.WithDB(t))
	tk := f.ticket()
	f.attempt(tk, 2)
	live := uuid.New()
	f.exec(`INSERT INTO agents (id, name) VALUES ($1, 'b')`, live)

	f.exec(`INSERT INTO leases (ticket_id, attempt_number, agent_id, acquired_at, expires_at, released_at)
	        VALUES ($1, 1, $2, $3::timestamptz, $3::timestamptz + interval '1 hour', NULL)`, tk, f.agent, t0)
	f.lease(tk, 2, f.agent, t0, at(5*time.Hour))
	f.exec(`UPDATE leases SET expires_at = $2 WHERE ticket_id = $1 AND attempt_number = 2`, tk, t0.Add(time.Hour))
	f.exec(`INSERT INTO leases (ticket_id, attempt_number, agent_id, acquired_at, expires_at)
	        VALUES ($1, 2, $2, now(), now() + interval '1 day')`, tk, live)

	got := f.signals(ticket.AttemptRef{TicketID: tk, Number: 1}, ticket.AttemptRef{TicketID: tk, Number: 2})
	expired := got[ticket.AttemptRef{TicketID: tk, Number: 1}]
	assert.Equal(t, time.Hour, *expired.WallClock)

	mixed := got[ticket.AttemptRef{TicketID: tk, Number: 2}]
	assert.EqualValues(t, 2, *mixed.Runs)
	assert.Nil(t, mixed.WallClock)
	assert.Nil(t, mixed.LastRunEndedAt)
}
