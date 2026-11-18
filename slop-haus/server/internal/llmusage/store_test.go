package llmusage_test

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	usagelib "github.com/lowaa/slop-factory/go-llm-usage"
	"github.com/lowaa/slop-factory/go-llm-usage/llmusagetest"
	"github.com/lowaa/slop-factory/go-llm-usage/pricing"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/agent"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/llmusage"
)

// t0 is the start of every test's timeline: the records' and leases' times
// are set relative to it, so attribution never depends on the clock. In the
// past, so that a lease still held -- which expires an hour from now --
// outlasts every window here.
var t0 = time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC)

type fixture struct {
	database *db.DB
	store    *llmusage.PostgresStore
	agents   *agent.Service
	// ctx is tenant A's.
	ctx context.Context
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	database := dbtest.WithDB(t)
	return &fixture{
		database: database,
		store:    llmusage.NewPostgresStore(database, pricing.Default()),
		agents:   agent.NewService(agent.NewPostgresStore(database), nil, nil, slog.DiscardHandler),
		ctx:      db.WithTenant(context.Background(), dbtest.NewTenant(t, database)),
	}
}

func (f *fixture) agent(t *testing.T, ctx context.Context, name string) *agent.Agent {
	t.Helper()
	created, err := f.agents.Create(ctx, name)
	require.NoError(t, err)
	return created
}

func (f *fixture) ticket(t *testing.T, ctx context.Context) int64 {
	t.Helper()
	var id int64
	require.NoError(t, f.database.QueryRow(ctx, `INSERT INTO tickets (title) VALUES ('t') RETURNING id`).Scan(&id))
	return id
}

// lease records a claim of ticket by agentID from acquired to released (a
// zero released is a lease still held), as the ticket store leaves it.
type lease struct {
	ticket, from, to int64 // minutes after t0; to < 0 is still held
	reason           string
	// expires is minutes after t0; defaults to to, or an hour from now for
	// a lease still held.
	expires *int64
	// locked pins the lease with the integration lock.
	locked bool
}

func (f *fixture) lease(t *testing.T, ctx context.Context, agentID uuid.UUID, l lease) int64 {
	t.Helper()
	at := func(m int64) time.Time { return t0.Add(time.Duration(m) * time.Minute) }
	var released *time.Time
	expires := at(l.to)
	if l.to >= 0 {
		r := at(l.to)
		released = &r
	} else {
		expires = time.Now().Add(time.Hour)
	}
	if l.expires != nil {
		expires = at(*l.expires)
	}
	reason := l.reason
	if reason == "" && released != nil {
		reason = "done"
	}
	var releaseReason *string
	if released != nil {
		releaseReason = &reason
	}
	var id int64
	require.NoError(t, f.database.QueryRow(ctx, `
		INSERT INTO leases (ticket_id, attempt_number, agent_id, acquired_at, expires_at, released_at, release_reason)
		VALUES ($1, 1, $2, $3, $4, $5, $6) RETURNING id`, l.ticket, agentID, at(l.from), expires, released, releaseReason).Scan(&id))
	if l.locked {
		_, err := f.database.Exec(ctx, `INSERT INTO locks (name, holder_agent_id, holder_lease_id) VALUES ('integration', $1, $2)`,
			agentID, id)
		require.NoError(t, err)
	}
	return id
}

func transcript(agentID uuid.UUID, key string) *llmusage.Transcript {
	return &llmusage.Transcript{
		AgentID: agentID, Key: usagelib.TranscriptKey(key), Harness: usagelib.HarnessPi,
		SessionID: "session-1", ScopeHint: "main",
	}
}

// spanning is a pi record covering minutes from..to after t0.
func spanning(from, to int64, requests int64) usagelib.Record {
	r := llmusagetest.Sample("anthropic", "claude-opus-5-5", requests)
	r.Harness = usagelib.HarnessPi
	r.StartedAt = t0.Add(time.Duration(from) * time.Minute)
	r.EndedAt = t0.Add(time.Duration(to) * time.Minute)
	return *r
}

func (f *fixture) upsert(t *testing.T, ctx context.Context, tr *llmusage.Transcript, records ...usagelib.Record) *llmusage.Attribution {
	t.Helper()
	_, attribution, err := f.store.Upsert(ctx, tr, records)
	require.NoError(t, err)
	return attribution
}

type storedRow struct {
	Scope    string
	TicketID *int64
}

// stored reads back what the table holds for the transcript, so a test
// checks the row, not just what Upsert said.
func (f *fixture) stored(t *testing.T, ctx context.Context, tr *llmusage.Transcript) []storedRow {
	t.Helper()
	rows, err := f.database.Query(ctx, `SELECT scope, ticket_id FROM llm_usage
		WHERE agent_id = $1 AND transcript_key = $2 ORDER BY model`, tr.AgentID, string(tr.Key))
	require.NoError(t, err)
	defer rows.Close()
	var out []storedRow
	for rows.Next() {
		var r storedRow
		require.NoError(t, rows.Scan(&r.Scope, &r.TicketID))
		out = append(out, r)
	}
	require.NoError(t, rows.Err())
	return out
}

func ticketScope(id int64) *llmusage.Attribution {
	attempt := 1
	return &llmusage.Attribution{Scope: llmusage.ScopeTicket, TicketID: &id, Attempt: &attempt}
}

var overhead = &llmusage.Attribution{Scope: llmusage.ScopeOverhead}

// suiteStore is one agent's transcripts in tenant A, as go-llm-usage's
// Store: what its StoreSuite checks.
type suiteStore struct {
	f     *fixture
	agent uuid.UUID
}

func (s *suiteStore) Upsert(_ context.Context, key usagelib.TranscriptKey, records []usagelib.Record) ([]usagelib.Record, error) {
	previous, _, err := s.f.store.Upsert(s.f.ctx, transcript(s.agent, string(key)), records)
	return previous, err
}

func TestPostgresStorePassesTheSharedStoreSuite(t *testing.T) {
	f := newFixture(t)
	builder := f.agent(t, f.ctx, "builder")
	llmusagetest.StoreSuite(t, func(*testing.T) usagelib.Store { return &suiteStore{f: f, agent: builder.ID} })
}

func TestAttribution(t *testing.T) {
	expired := int64(30)
	for _, tc := range []struct {
		name string
		// leases of the reporting agent, by ticket index (0 or 1)
		leases []lease
		// leases of another agent in the same tenant
		others   []lease
		from, to int64
		// want is the ticket index the transcript belongs to, or -1 for
		// overhead.
		want int
	}{
		{name: "inside one claim", leases: []lease{{ticket: 0, from: 0, to: 60}}, from: 10, to: 50, want: 0},
		{name: "starts before the claim, as a pi session does", leases: []lease{{ticket: 0, from: 10, to: 60}}, from: 0, to: 50, want: 0},
		{name: "runs on after the ticket is handed back", leases: []lease{{ticket: 0, from: 0, to: 40}}, from: 10, to: 50, want: 0},
		{name: "a claim still held", leases: []lease{{ticket: 0, from: 0, to: -1}}, from: 10, to: 50, want: 0},
		{name: "no claim at all", from: 10, to: 50, want: -1},
		{name: "between two claims", leases: []lease{{ticket: 0, from: 0, to: 10}, {ticket: 1, from: 60, to: 90}}, from: 20, to: 50, want: -1},
		{name: "spans two tickets", leases: []lease{{ticket: 0, from: 0, to: 30}, {ticket: 1, from: 40, to: 90}}, from: 10, to: 50, want: -1},
		{name: "one ticket claimed twice", leases: []lease{{ticket: 0, from: 0, to: 20, reason: "released"}, {ticket: 0, from: 30, to: 90}}, from: 10, to: 50, want: 0},
		{name: "another agent's claim is not ours", others: []lease{{ticket: 0, from: 0, to: 60}}, from: 10, to: 50, want: -1},
		{
			// Swept by a later claim at minute 120, long after it ran out at 30.
			name:   "an expired lease ends at its deadline, not its sweep",
			leases: []lease{{ticket: 0, from: 0, to: 120, reason: "expired", expires: &expired}},
			from:   60, to: 90, want: -1,
		},
		{
			// Past its deadline at 30, and no claim has swept it yet.
			name:   "an unreleased lease past its deadline has ended",
			leases: []lease{{ticket: 0, from: 0, to: -1, expires: &expired}},
			from:   60, to: 90, want: -1,
		},
		{
			name:   "unless the integration lock pins it",
			leases: []lease{{ticket: 0, from: 0, to: -1, expires: &expired, locked: true}},
			from:   60, to: 90, want: 0,
		},
		{
			name:   "a released lease ends when it was released",
			leases: []lease{{ticket: 0, from: 0, to: 120, reason: "released", expires: &expired}},
			from:   60, to: 90, want: 0,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			builder, reviewer := f.agent(t, f.ctx, "builder"), f.agent(t, f.ctx, "reviewer")
			tickets := []int64{f.ticket(t, f.ctx), f.ticket(t, f.ctx)}
			for _, l := range tc.leases {
				l.ticket = tickets[l.ticket]
				f.lease(t, f.ctx, builder.ID, l)
			}
			for _, l := range tc.others {
				l.ticket = tickets[l.ticket]
				f.lease(t, f.ctx, reviewer.ID, l)
			}
			want := overhead
			if tc.want >= 0 {
				want = ticketScope(tickets[tc.want])
			}
			tr := transcript(builder.ID, "pi-session")
			assert.Equal(t, want, f.upsert(t, f.ctx, tr, spanning(tc.from, tc.to, 1)))
			assert.Equal(t, []storedRow{{Scope: string(want.Scope), TicketID: want.TicketID}}, f.stored(t, f.ctx, tr))
		})
	}
}

// The window is every record's, not the first's: a second model that ran
// later stretches it.
func TestAttributionCoversEveryRecord(t *testing.T) {
	f := newFixture(t)
	builder := f.agent(t, f.ctx, "builder")
	a, b := f.ticket(t, f.ctx), f.ticket(t, f.ctx)
	f.lease(t, f.ctx, builder.ID, lease{ticket: a, from: 0, to: 30})
	f.lease(t, f.ctx, builder.ID, lease{ticket: b, from: 40, to: 90})
	later := spanning(45, 50, 1)
	later.Model = "claude-sonnet-5-5"
	assert.Equal(t, overhead, f.upsert(t, f.ctx, transcript(builder.ID, "k"), spanning(10, 20, 1), later))
}

// Each report is attributed afresh: a transcript grows, and one that was
// inside a ticket can run on into the next.
func TestAttributionIsWorkedOutAgainOnEveryReport(t *testing.T) {
	f := newFixture(t)
	builder := f.agent(t, f.ctx, "builder")
	a, b := f.ticket(t, f.ctx), f.ticket(t, f.ctx)
	f.lease(t, f.ctx, builder.ID, lease{ticket: a, from: 0, to: 30})
	f.lease(t, f.ctx, builder.ID, lease{ticket: b, from: 40, to: 90})
	tr := transcript(builder.ID, "k")

	assert.Equal(t, ticketScope(a), f.upsert(t, f.ctx, tr, spanning(5, 20, 1)))
	assert.Equal(t, overhead, f.upsert(t, f.ctx, tr, spanning(5, 50, 2)))
	assert.Equal(t, []storedRow{{Scope: "overhead"}}, f.stored(t, f.ctx, tr))
}

// A transcript key is the agent's own: another agent reporting the same
// key -- two harnesses can mint the same session id -- neither sees nor
// replaces the first's totals.
func TestTranscriptsAreKeyedPerAgent(t *testing.T) {
	f := newFixture(t)
	builder, reviewer := f.agent(t, f.ctx, "builder"), f.agent(t, f.ctx, "reviewer")
	first := spanning(0, 10, 3)
	_, _, err := f.store.Upsert(f.ctx, transcript(builder.ID, "same"), []usagelib.Record{first})
	require.NoError(t, err)

	previous, _, err := f.store.Upsert(f.ctx, transcript(reviewer.ID, "same"), []usagelib.Record{spanning(0, 10, 7)})
	require.NoError(t, err)
	assert.Empty(t, previous)

	previous, _, err = f.store.Upsert(f.ctx, transcript(builder.ID, "same"), []usagelib.Record{first})
	require.NoError(t, err)
	require.Len(t, previous, 1)
	assert.Equal(t, int64(3), previous[0].Requests, "the reviewer's report did not replace the builder's")
}

// Row level security: another tenant reads none of these rows, and its own
// report under the same agent id and key starts from nothing.
func TestUsageIsIsolatedBetweenTenants(t *testing.T) {
	f := newFixture(t)
	builder := f.agent(t, f.ctx, "builder")
	other := db.WithTenant(context.Background(), dbtest.NewTenant(t, f.database))
	tr := transcript(builder.ID, "k")
	f.upsert(t, f.ctx, tr, spanning(0, 10, 1))

	var n int
	require.NoError(t, f.database.QueryRow(other, `SELECT count(*) FROM llm_usage`).Scan(&n))
	assert.Zero(t, n)
	require.NoError(t, f.database.QueryRow(other, `SELECT count(*) FROM llm_tool_usage`).Scan(&n))
	assert.Zero(t, n)

	previous, _, err := f.store.Upsert(other, tr, []usagelib.Record{spanning(0, 10, 2)})
	require.NoError(t, err)
	assert.Empty(t, previous, "another tenant sees nothing of this one's transcript")
	assert.Len(t, f.stored(t, f.ctx, tr), 1)
}

// A ticket deleted later leaves the usage it was charged, unattributed.
func TestDeletingATicketKeepsItsUsage(t *testing.T) {
	f := newFixture(t)
	builder := f.agent(t, f.ctx, "builder")
	a := f.ticket(t, f.ctx)
	f.lease(t, f.ctx, builder.ID, lease{ticket: a, from: 0, to: 60})
	tr := transcript(builder.ID, "k")
	f.upsert(t, f.ctx, tr, spanning(10, 20, 1))

	_, err := f.database.Exec(f.ctx, `DELETE FROM tickets WHERE id = $1`, a)
	require.NoError(t, err)
	assert.Equal(t, []storedRow{{Scope: "ticket"}}, f.stored(t, f.ctx, tr))
}
