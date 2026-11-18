package prune_test

import (
	"context"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/agent"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/prune"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/session"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

type fixture struct {
	store    *prune.PostgresStore
	tickets  *ticket.Service
	sessions *session.Service
	agents   *agent.Service
	database *db.DB
	ctx      context.Context
	// human is a real global_users row, for the one test that attributes a
	// ticket comment to a person -- ticket_comments.author_user_id carries
	// a real foreign key to it.
	human uuid.UUID
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	database := dbtest.WithDB(t)
	tickets := ticket.NewService(ticket.NewPostgresStore(database), slog.DiscardHandler)
	// A real global_tenants row: RunOnce discovers what to prune by listing
	// global_tenants, so a fixture with no real row there would have
	// nothing for it to iterate -- and an OAuth grant's foreign key needs
	// one too.
	tenantID := dbtest.NewTenant(t, database)
	return &fixture{
		store:    prune.NewPostgresStore(database, slog.DiscardHandler),
		tickets:  tickets,
		sessions: session.NewService(session.NewPostgresStore(database), slog.DiscardHandler),
		agents:   agent.NewService(agent.NewPostgresStore(database), tickets, nil, slog.DiscardHandler),
		database: database,
		ctx:      db.WithTenant(context.Background(), tenantID),
		human:    dbtest.NewUser(t, database),
	}
}

func (f *fixture) agent(t *testing.T, name string) uuid.UUID {
	t.Helper()
	created, err := f.agents.Create(f.ctx, name)
	require.NoError(t, err)
	return created.ID
}

// sweep runs one pass of cfg's rules over every tenant and the global
// tables, and fails the test on an error.
func (f *fixture) sweep(t *testing.T, cfg prune.Config) prune.Result {
	t.Helper()
	result, err := prune.NewService(f.store, cfg, slog.DiscardHandler).RunOnce(context.Background())
	require.NoError(t, err)
	return result
}

// age backdates a row so retention can be tested without sleeping.
func (f *fixture) age(t *testing.T, table, column string, id int64, d time.Duration) {
	t.Helper()
	_, err := f.database.Exec(f.ctx,
		fmt.Sprintf("UPDATE %s SET %s = now() - $2::interval WHERE id = $1", table, column),
		id, d.String())
	require.NoError(t, err)
}

// ids returns the surviving primary keys, so a test can assert *which*
// rows were kept rather than how many. A count alone passes an
// implementation that deletes exactly the wrong row.
func (f *fixture) ids(t *testing.T, table string) []int64 {
	t.Helper()
	rows, err := f.database.Query(f.ctx, "SELECT id FROM "+table+" ORDER BY id")
	require.NoError(t, err)
	defer rows.Close()

	out := []int64{}
	for rows.Next() {
		var id int64
		require.NoError(t, rows.Scan(&id))
		out = append(out, id)
	}
	require.NoError(t, rows.Err())
	return out
}

func (f *fixture) count(t *testing.T, table string) int {
	t.Helper()
	var n int
	require.NoError(t, f.database.QueryRow(f.ctx, "SELECT count(*) FROM "+table).Scan(&n))
	return n
}

// An agent left running for a month is working, not stale.
func TestPruneSessions_NeverTouchesACurrentSession(t *testing.T) {
	f := newFixture(t)
	agentID := f.agent(t, "long-runner")

	started, err := f.sessions.Start(f.ctx, agentID, "claude-1", "/srv/ws/1")
	require.NoError(t, err)
	f.age(t, "sessions", "started_at", started.Session.ID, 90*24*time.Hour)

	deleted := f.sweep(t, prune.Config{SessionRetention: 30 * 24 * time.Hour}).Sessions
	assert.Zero(t, deleted)
	assert.Equal(t, []int64{started.Session.ID}, f.ids(t, "sessions"))
}

func TestPruneSessions_DeletesOldEndedOnes(t *testing.T) {
	f := newFixture(t)
	agentID := f.agent(t, "restarter")

	first, err := f.sessions.Start(f.ctx, agentID, "claude-1", "/srv/ws/1")
	require.NoError(t, err)
	_, err = f.sessions.Start(f.ctx, agentID, "claude-2", "/srv/ws/1")
	require.NoError(t, err)

	f.age(t, "sessions", "ended_at", first.Session.ID, 90*24*time.Hour)

	deleted := f.sweep(t, prune.Config{SessionRetention: 30 * 24 * time.Hour}).Sessions
	assert.Equal(t, int64(1), deleted)

	surviving := f.ids(t, "sessions")
	require.Len(t, surviving, 1)
	assert.NotEqual(t, first.Session.ID, surviving[0],
		"the *old, ended* session is the one that should have gone")
}

// A lease recording a pruned session keeps its row with a null reference.
// Without ON DELETE SET NULL this fails on the foreign key, which is every
// session that ever did any work.
func TestPruneSessions_LeavesLeasesThatReferencedThem(t *testing.T) {
	f := newFixture(t)
	agentID := f.agent(t, "worker")

	started, err := f.sessions.Start(f.ctx, agentID, "claude-1", "/srv/ws/1")
	require.NoError(t, err)

	created, err := f.tickets.Create(f.ctx, &ticket.CreateParams{Title: "Some work"})
	require.NoError(t, err)
	_, err = f.tickets.Claim(f.ctx, agentID, &created.ID)
	require.NoError(t, err)
	_, err = f.database.Exec(f.ctx,
		`UPDATE leases SET acquired_by_session_id = $1 WHERE agent_id = $2`,
		started.Session.ID, agentID)
	require.NoError(t, err)

	_, err = f.sessions.Start(f.ctx, agentID, "claude-2", "/srv/ws/1")
	require.NoError(t, err)
	f.age(t, "sessions", "ended_at", started.Session.ID, 90*24*time.Hour)

	f.sweep(t, prune.Config{SessionRetention: 30 * 24 * time.Hour})

	assert.Equal(t, 1, f.count(t, "leases"), "the lease survives its session")
	var ref *int64
	require.NoError(t, f.database.QueryRow(f.ctx,
		`SELECT acquired_by_session_id FROM leases LIMIT 1`).Scan(&ref))
	assert.Nil(t, ref)
}

// An unreleased lease is live or pinned by the integration lock. Deleting
// one would hand a held ticket to somebody else, and cascade its lock away
// -- exactly the state the pin exists to prevent.
func TestPruneLeases_NeverTouchesAHeldLease(t *testing.T) {
	f := newFixture(t)
	agentID := f.agent(t, "holder")

	created, err := f.tickets.Create(f.ctx, &ticket.CreateParams{Title: "Held for a long time"})
	require.NoError(t, err)
	_, err = f.tickets.Claim(f.ctx, agentID, &created.ID)
	require.NoError(t, err)
	require.NoError(t, f.tickets.AcquireIntegrationLock(f.ctx, agentID))

	_, err = f.database.Exec(f.ctx,
		`UPDATE leases SET acquired_at = now() - interval '90 days'`)
	require.NoError(t, err)

	deleted := f.sweep(t, prune.Config{LeaseRetention: 30 * 24 * time.Hour}).Leases
	assert.Zero(t, deleted)
	assert.Equal(t, 1, f.count(t, "leases"))
	assert.Equal(t, 1, f.count(t, "locks"), "and its lock is still held")
}

func TestPruneLeases_DeletesOldReleasedOnes(t *testing.T) {
	f := newFixture(t)
	agentID := f.agent(t, "worker")

	created, err := f.tickets.Create(f.ctx, &ticket.CreateParams{Title: "Finished long ago"})
	require.NoError(t, err)
	_, err = f.tickets.Claim(f.ctx, agentID, &created.ID)
	require.NoError(t, err)
	_, err = f.tickets.Done(f.ctx, agentID, &ticket.DoneParams{Summary: "done"})
	require.NoError(t, err)

	_, err = f.database.Exec(f.ctx, `UPDATE leases SET released_at = now() - interval '90 days'`)
	require.NoError(t, err)

	deleted := f.sweep(t, prune.Config{LeaseRetention: 30 * 24 * time.Hour}).Leases
	assert.Equal(t, int64(1), deleted)

	// The ticket itself is the work record and is never pruned.
	assert.Equal(t, 1, f.count(t, "tickets"))
	assert.Equal(t, 1, f.count(t, "ticket_comments"), "its summary survives too")
}

// Tickets are the thing the human is keeping. Nothing here deletes them,
// however old they are.
func TestPrune_NeverDeletesTheWorkRecord(t *testing.T) {
	f := newFixture(t)

	created, err := f.tickets.Create(f.ctx, &ticket.CreateParams{
		Title: "Ancient but still the record",
		Paths: []model.Path{{Path: "server/"}},
	})
	require.NoError(t, err)
	_, err = f.tickets.AddComment(f.ctx, created.ID, ticket.UserAuthor(f.human), "a decision from long ago")
	require.NoError(t, err)

	_, err = f.database.Exec(f.ctx, `
		UPDATE tickets SET created_at = now() - interval '365 days',
		                   updated_at = now() - interval '365 days'`)
	require.NoError(t, err)
	_, err = f.database.Exec(f.ctx,
		`UPDATE ticket_comments SET created_at = now() - interval '365 days'`)
	require.NoError(t, err)

	svc := prune.NewService(f.store, prune.Config{
		SessionRetention: time.Hour,
		LeaseRetention:   time.Hour,
	}, slog.DiscardHandler)
	_, err = svc.RunOnce(f.ctx)
	require.NoError(t, err)

	assert.Equal(t, 1, f.count(t, "tickets"))
	assert.Equal(t, 1, f.count(t, "ticket_comments"))
	assert.Equal(t, 1, f.count(t, "ticket_paths"))
}

// A retention of zero disables that prune, which is how you turn one off
// without inventing a large number.
func TestRunOnce_ZeroRetentionDisablesAPrune(t *testing.T) {
	f := newFixture(t)
	agentID := f.agent(t, "restarter")
	first, err := f.sessions.Start(f.ctx, agentID, "claude-1", "/srv/ws/1")
	require.NoError(t, err)
	_, err = f.sessions.Start(f.ctx, agentID, "claude-2", "/srv/ws/1")
	require.NoError(t, err)
	f.age(t, "sessions", "ended_at", first.Session.ID, 365*24*time.Hour)

	svc := prune.NewService(f.store, prune.Config{}, slog.DiscardHandler)
	result, err := svc.RunOnce(f.ctx)
	require.NoError(t, err)
	assert.True(t, result.Empty())
	assert.Equal(t, 2, f.count(t, "sessions"))
}

// The background loop has no request to inherit a tenant from, so it has
// to discover tenants for itself and run each one's prune on its own
// scoped context. This is the test that would fail if RunOnce pruned only
// whatever tenant happened to be on the context it was started with (or
// worse, ran unscoped and swept every tenant's rows together): each
// tenant's old ended session must go -- so both tenants were visited -- and
// tenant B's recent one must survive.
func TestRunOnce_PrunesEachTenantIndependently(t *testing.T) {
	database := dbtest.WithDB(t)
	tenantA := dbtest.NewTenant(t, database)
	tenantB := dbtest.NewTenant(t, database)
	ctxA := db.WithTenant(context.Background(), tenantA)
	ctxB := db.WithTenant(context.Background(), tenantB)

	tickets := ticket.NewService(ticket.NewPostgresStore(database), slog.DiscardHandler)
	agents := agent.NewService(agent.NewPostgresStore(database), tickets, nil, slog.DiscardHandler)
	sessions := session.NewService(session.NewPostgresStore(database), slog.DiscardHandler)
	// endedSession leaves an ended session behind, ended `ago` ago.
	endedSession := func(ctx context.Context, name string, ago string) {
		t.Helper()
		a, err := agents.Create(ctx, name)
		require.NoError(t, err)
		_, err = sessions.Start(ctx, a.ID, "claude-1", "/srv/ws/1")
		require.NoError(t, err)
		_, err = sessions.Start(ctx, a.ID, "claude-2", "/srv/ws/1")
		require.NoError(t, err)
		_, err = database.Exec(ctx,
			`UPDATE sessions SET ended_at = now() - $2::interval WHERE agent_id = $1 AND claude_session_id = 'claude-1'`, a.ID, ago)
		require.NoError(t, err)
	}
	endedSession(ctxA, "old-in-a", "60 days")
	endedSession(ctxB, "old-in-b", "60 days")
	endedSession(ctxB, "recent-in-b", "1 hour")

	svc := prune.NewService(prune.NewPostgresStore(database, slog.DiscardHandler),
		prune.Config{SessionRetention: 30 * 24 * time.Hour}, slog.DiscardHandler)
	// No tenant on this context at all: RunOnce has to find tenantA and
	// tenantB by itself, through the global handle, the same way the real
	// background loop does.
	result, err := svc.RunOnce(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int64(2), result.Sessions, "each tenant's old ended session, and nothing else")

	var countA, countB int
	require.NoError(t, database.QueryRow(ctxA, `SELECT count(*) FROM sessions WHERE ended_at IS NOT NULL`).Scan(&countA))
	require.NoError(t, database.QueryRow(ctxB, `SELECT count(*) FROM sessions WHERE ended_at IS NOT NULL`).Scan(&countB))
	assert.Zero(t, countA, "tenant A's old session is gone")
	assert.Equal(t, 1, countB, "tenant B was pruned too, and its recent session survived")
}

// More rows than one batch, to prove the loop terminates and deletes
// everything rather than stopping after the first pass.
func TestPruneSessions_BatchesThroughALargeBacklog(t *testing.T) {
	f := newFixture(t)
	agentID := f.agent(t, "restarter")

	const total = 2500
	for i := range total {
		_, err := f.database.Exec(f.ctx, `
			INSERT INTO sessions (agent_id, claude_session_id, ended_at)
			VALUES ($1, $2, now() - interval '90 days')`, agentID, fmt.Sprintf("old-%d", i))
		require.NoError(t, err)
	}

	deleted := f.sweep(t, prune.Config{SessionRetention: 30 * 24 * time.Hour}).Sessions
	assert.Equal(t, int64(total), deleted, "the batch loop must run until the backlog is gone")
	assert.Zero(t, f.count(t, "sessions"))
}

// Each credential table keeps what is live and loses what is dead: expired
// past the grace, or -- for invites and grants -- revoked. An accepted
// invitation is kept as the record of who invited whom.
func TestPruneExpiredCredentials(t *testing.T) {
	f := newFixture(t)
	tenantID, err := db.TenantFrom(f.ctx)
	require.NoError(t, err)
	agentID := f.agent(t, "builder")
	g := f.database.Global()
	exec := func(sql string, args ...any) {
		t.Helper()
		_, err := g.Exec(context.Background(), sql, args...)
		require.NoError(t, err)
	}
	old, live := "now() - interval '2 days'", "now() + interval '1 hour'"

	for i, expires := range []string{old, live} {
		exec(`INSERT INTO global_auth_tokens (id, user_id, token_hash, purpose, expires_at)
			VALUES ($1, $2, $3, 'activation', `+expires+`)`, uuid.New(), f.human, fmt.Sprintf("auth-%d", i))
		exec(`INSERT INTO global_oauth_device_codes (id, device_code_hash, user_code, client_id, resource, expires_at)
			VALUES ($1, $2, $3, 'slop-haus-workspace', 'r', `+expires+`)`, uuid.New(), fmt.Sprintf("dev-%d", i), fmt.Sprintf("CODE-000%d", i))
	}
	liveGrant, deadGrant := uuid.New(), uuid.New()
	exec(`INSERT INTO global_oauth_grants (id, tenant, agent_id, client_id, resource) VALUES ($1, $2, $3, 'slop-haus-workspace', 'r')`, liveGrant, tenantID, agentID)
	exec(`INSERT INTO global_oauth_grants (id, tenant, agent_id, client_id, resource, revoked_at) VALUES ($1, $2, $3, 'slop-haus-workspace', 'r', now() - interval '2 days')`, deadGrant, tenantID, agentID)
	exec(`INSERT INTO global_oauth_tokens (token_hash, grant_id, kind, expires_at) VALUES ('tok-old', $1, 'refresh', `+old+`)`, liveGrant)
	exec(`INSERT INTO global_oauth_tokens (token_hash, grant_id, kind, expires_at) VALUES ('tok-live', $1, 'access', `+live+`)`, liveGrant)
	exec(`INSERT INTO global_oauth_tokens (token_hash, grant_id, kind, expires_at) VALUES ('tok-dead-grant', $1, 'access', `+live+`)`, deadGrant)
	// A just-expired token, inside the grace: kept, so a replay is still
	// recognised as one.
	exec(`INSERT INTO global_oauth_tokens (token_hash, grant_id, kind, expires_at, used_at) VALUES ('tok-grace', $1, 'refresh', now() - interval '1 hour', now())`, liveGrant)
	exec(`INSERT INTO global_invites (id, token_hash, email, kind, invited_by, expires_at) VALUES ($1, 'inv-old', 'a@x', 'new_tenant', $2, `+old+`)`, uuid.New(), f.human)
	exec(`INSERT INTO global_invites (id, token_hash, email, kind, invited_by, expires_at, revoked_at) VALUES ($1, 'inv-revoked', 'd@x', 'new_tenant', $2, `+live+`, now() - interval '2 days')`, uuid.New(), f.human)
	for i, expires := range []string{old, live} {
		exec(`INSERT INTO global_oauth_auth_requests (id, client_id, redirect_uri, code_challenge, resource, expires_at)
			VALUES ($1, 'slop-haus-workspace', 'http://localhost/cb', 'c', 'r', `+expires+`)`, uuid.MustParse(fmt.Sprintf("00000000-0000-0000-0000-00000000000%d", i+1)))
		exec(`INSERT INTO global_oauth_codes (code_hash, grant_id, redirect_uri, code_challenge, expires_at)
			VALUES ($1, $2, 'http://localhost/cb', 'c', `+expires+`)`, fmt.Sprintf("code-%d", i), liveGrant)
	}
	exec(`INSERT INTO global_invites (id, token_hash, email, kind, invited_by, expires_at, accepted_at) VALUES ($1, 'inv-accepted', 'b@x', 'new_tenant', $2, `+old+`, now() - interval '3 days')`, uuid.New(), f.human)
	exec(`INSERT INTO global_invites (id, token_hash, email, kind, invited_by, expires_at) VALUES ($1, 'inv-live', 'c@x', 'new_tenant', $2, `+live+`)`, uuid.New(), f.human)

	// Registered clients: one never authorized for over an hour goes; one
	// with a grant, a new one, and the seeded workspace client stay.
	exec(`INSERT INTO global_oauth_clients (id, grant_types, created_at) VALUES ('c-abandoned', '{authorization_code}', now() - interval '2 hours')`)
	exec(`INSERT INTO global_oauth_clients (id, grant_types, created_at) VALUES ('c-recent', '{authorization_code}', now() - interval '30 minutes')`)
	exec(`INSERT INTO global_oauth_clients (id, grant_types, created_at) VALUES ('c-in-use', '{authorization_code}', now() - interval '2 days')`)
	inUseGrant := uuid.New()
	exec(`INSERT INTO global_oauth_grants (id, tenant, agent_id, client_id, resource) VALUES ($1, $2, $3, 'c-in-use', 'r')`, inUseGrant, tenantID, agentID)

	deleted := f.sweep(t, prune.Config{}).Credentials

	remaining := func(table, key string) []string {
		t.Helper()
		rows, err := g.Query(context.Background(), `SELECT `+key+`::text FROM `+table+` ORDER BY 1`)
		require.NoError(t, err)
		defer rows.Close()
		var out []string
		for rows.Next() {
			var k string
			require.NoError(t, rows.Scan(&k))
			out = append(out, k)
		}
		return out
	}
	assert.Equal(t, []string{"auth-1"}, remaining("global_auth_tokens", "token_hash"))
	assert.Equal(t, []string{"dev-1"}, remaining("global_oauth_device_codes", "device_code_hash"))
	assert.Equal(t, []string{"tok-grace", "tok-live"}, remaining("global_oauth_tokens", "token_hash"))
	assert.ElementsMatch(t, []string{liveGrant.String(), inUseGrant.String()}, remaining("global_oauth_grants", "id"))
	assert.Equal(t, []string{"inv-accepted", "inv-live"}, remaining("global_invites", "token_hash"), "expired and revoked invites go")
	assert.Equal(t, []string{"00000000-0000-0000-0000-000000000002"}, remaining("global_oauth_auth_requests", "id"))
	assert.Equal(t, []string{"code-1"}, remaining("global_oauth_codes", "code_hash"))
	assert.Equal(t, []string{"c-in-use", "c-recent", "slop-haus-workspace"}, remaining("global_oauth_clients", "id"))
	assert.Equal(t, int64(9), deleted, "old auth token, device code, token, auth request and code; dead grant; old and revoked invites; abandoned client")
}

// delivery records a GitHub webhook delivery received age ago, in ctx's
// tenant.
func (f *fixture) delivery(t *testing.T, ctx context.Context, id string, age time.Duration) {
	t.Helper()
	_, err := f.database.Exec(ctx, `
		INSERT INTO github_deliveries (delivery_id, installation_id, event, outcome, received_at)
		VALUES ($1, 1, 'ping', 'applied', now() - $2::interval)`, id, age.String())
	require.NoError(t, err)
}

func (f *fixture) deliveryIDs(t *testing.T, ctx context.Context) []string {
	t.Helper()
	rows, err := f.database.Query(ctx, `SELECT delivery_id FROM github_deliveries ORDER BY delivery_id`)
	require.NoError(t, err)
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var id string
		require.NoError(t, rows.Scan(&id))
		out = append(out, id)
	}
	require.NoError(t, rows.Err())
	return out
}

func TestPruneDeliveries_DeletesEveryTenantsOldOnes(t *testing.T) {
	f := newFixture(t)
	other := db.WithTenant(context.Background(), dbtest.NewTenant(t, f.database))
	f.delivery(t, f.ctx, "old", 40*24*time.Hour)
	f.delivery(t, f.ctx, "recent", time.Hour)
	f.delivery(t, other, "other-old", 40*24*time.Hour)

	deleted := f.sweep(t, prune.Config{DeliveryRetention: 30 * 24 * time.Hour}).Deliveries
	assert.Equal(t, int64(2), deleted)
	assert.Equal(t, []string{"recent"}, f.deliveryIDs(t, f.ctx))
	assert.Empty(t, f.deliveryIDs(t, other), "another tenant's old one goes in its own scope")
}

func TestPruneDeliveries_AlsoPrunesIntakeDeliveries(t *testing.T) {
	f := newFixture(t)
	other := db.WithTenant(context.Background(), dbtest.NewTenant(t, f.database))
	insert := func(ctx context.Context, delivery string, age time.Duration) {
		source := uuid.New()
		_, err := f.database.Exec(ctx, `
			INSERT INTO intake_sources (id, name, adapter, trust, auth_kind, default_status, default_priority, max_priority)
			VALUES ($1, $1::uuid::text, 'native', 'untrusted', 'bearer', 'preparing', 'normal', 'normal')`, source)
		require.NoError(t, err)
		_, err = f.database.Exec(ctx, `
			INSERT INTO intake_deliveries (source_id, delivery_id, outcome, received_at)
			VALUES ($1, $2, 'applied', now() - $3::interval)`, source, delivery, age.String())
		require.NoError(t, err)
	}
	remaining := func(ctx context.Context) []string {
		rows, err := f.database.Query(ctx, `SELECT delivery_id FROM intake_deliveries ORDER BY delivery_id`)
		require.NoError(t, err)
		defer rows.Close()
		var ids []string
		for rows.Next() {
			var id string
			require.NoError(t, rows.Scan(&id))
			ids = append(ids, id)
		}
		return ids
	}
	insert(f.ctx, "old", 40*24*time.Hour)
	insert(f.ctx, "recent", time.Hour)
	insert(other, "other-old", 40*24*time.Hour)

	deleted := f.sweep(t, prune.Config{DeliveryRetention: 30 * 24 * time.Hour}).Deliveries
	assert.Equal(t, int64(2), deleted)
	assert.Equal(t, []string{"recent"}, remaining(f.ctx))
	assert.Empty(t, remaining(other), "another tenant's old one goes in its own scope")
}

// usage inserts one transcript's usage row and a tool row under it, last
// reported age ago.
func (f *fixture) usage(t *testing.T, ctx context.Context, agentID uuid.UUID, key string, age time.Duration) {
	t.Helper()
	_, err := f.database.Exec(ctx, `
		INSERT INTO llm_usage (agent_id, transcript_key, harness, session_id, scope_hint, scope,
			provider, model, billing_mode, requests, input_tokens, output_tokens, cache_read_tokens,
			cache_write_tokens, reasoning_tokens, price_version, unpriced, started_at, ended_at, updated_at)
		VALUES ($1, $2, 'pi', 's', 'main', 'overhead', 'anthropic', 'claude-opus-5-5', 'subscription',
			1, 1, 1, 0, 0, 0, '', true, now() - $3::interval, now() - $3::interval, now() - $3::interval)`,
		agentID, key, age.String())
	require.NoError(t, err)
	_, err = f.database.Exec(ctx, `
		INSERT INTO llm_tool_usage (agent_id, transcript_key, provider, model, harness, billing_mode,
			category, tool, calls, errors, duration_ms, result_bytes, ended_at)
		VALUES ($1, $2, 'anthropic', 'claude-opus-5-5', 'pi', 'subscription', 'bash', 'bash', 1, 0, 1, 1, now() - $3::interval)`,
		agentID, key, age.String())
	require.NoError(t, err)
}

func (f *fixture) usageKeys(t *testing.T, ctx context.Context, table string) []string {
	t.Helper()
	rows, err := f.database.Query(ctx, "SELECT transcript_key FROM "+table+" ORDER BY transcript_key")
	require.NoError(t, err)
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var key string
		require.NoError(t, rows.Scan(&key))
		out = append(out, key)
	}
	require.NoError(t, rows.Err())
	return out
}

func TestPruneLLMUsage_DeletesEveryTenantsOldTranscripts(t *testing.T) {
	f := newFixture(t)
	otherTenant := dbtest.NewTenant(t, f.database)
	other := db.WithTenant(context.Background(), otherTenant)
	otherAgent, err := agent.NewService(agent.NewPostgresStore(f.database), f.tickets, nil, slog.DiscardHandler).Create(other, "other")
	require.NoError(t, err)
	builder := f.agent(t, "builder")
	f.usage(t, f.ctx, builder, "old", 400*24*time.Hour)
	f.usage(t, f.ctx, builder, "recent", time.Hour)
	f.usage(t, other, otherAgent.ID, "other-old", 400*24*time.Hour)

	deleted := f.sweep(t, prune.Config{LLMUsageRetention: 365 * 24 * time.Hour}).LLMUsage
	assert.Equal(t, int64(2), deleted)
	assert.Equal(t, []string{"recent"}, f.usageKeys(t, f.ctx, "llm_usage"))
	assert.Equal(t, []string{"recent"}, f.usageKeys(t, f.ctx, "llm_tool_usage"), "tool rows go with their transcript")
	assert.Empty(t, f.usageKeys(t, other, "llm_usage"), "another tenant's old one goes in its own scope")
}

func TestPruneLLMUsage_PrunesTheDailyRollupByDay(t *testing.T) {
	f := newFixture(t)
	builder := f.agent(t, "builder")
	// The day holding the cutoff is partly inside the retention: it stays.
	for _, age := range []string{"400 days", "365 days", "2 days"} {
		_, err := f.database.Exec(f.ctx, `
			INSERT INTO llm_usage_daily (agent_id, day, provider, model, billing_mode, records, requests)
			VALUES ($1, date_trunc('day', (now() - $2::interval) AT TIME ZONE 'UTC') AT TIME ZONE 'UTC', 'p', 'm', 'metered', 1, 1)`,
			builder, age)
		require.NoError(t, err)
	}

	deleted := f.sweep(t, prune.Config{LLMUsageRetention: 365 * 24 * time.Hour}).LLMUsage

	assert.Equal(t, int64(1), deleted)
	var left int
	require.NoError(t, f.database.QueryRow(f.ctx, `SELECT count(*) FROM llm_usage_daily`).Scan(&left))
	assert.Equal(t, 2, left, "the day holding the cutoff and the recent day stay")
}

func TestPruneLLMUsage_PrunesGuidanceBesideIt(t *testing.T) {
	f := newFixture(t)
	builder := f.agent(t, "builder")
	var ticketID int64
	require.NoError(t, f.database.QueryRow(f.ctx, `INSERT INTO tickets (title) VALUES ('t') RETURNING id`).Scan(&ticketID))
	for _, key := range []string{"old", "recent"} {
		age := map[string]string{"old": "400 days", "recent": "1 hour"}[key]
		_, err := f.database.Exec(f.ctx, `
			INSERT INTO llm_usage_marks (agent_id, transcript_key, provider, model, harness, billing_mode, requests, input_tokens,
				output_tokens, cache_read_tokens, cache_write_tokens, reasoning_tokens, updated_at)
			VALUES ($1, $2, 'p', 'm', 'pi', 'metered', 1, 1, 1, 0, 0, 0, now() - $3::interval)`, builder, key, age)
		require.NoError(t, err)
		_, err = f.database.Exec(f.ctx, `
			INSERT INTO llm_guidance_usage (agent_id, transcript_key, ticket_id, provider, model, harness, billing_mode, requests,
				input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, reasoning_tokens, price_version, unpriced, ended_at, updated_at)
			VALUES ($1, $2, $4, 'p', 'm', 'pi', 'metered', 1, 1, 1, 0, 0, 0, 'v', false, now(), now() - $3::interval)`, builder, key, age, ticketID)
		require.NoError(t, err)
	}
	// Two declarations of one scope, both old: the later one is what the
	// next report is split by, and stays. Another agent's later declaration
	// does not supersede them, nor does a later one of the same agent in a
	// conversation of its own.
	for range 2 {
		_, err := f.database.Exec(f.ctx, `INSERT INTO usage_declarations (agent_id, ticket_id, declared_at)
			VALUES ($1, $2, now() - interval '400 days')`, builder, ticketID)
		require.NoError(t, err)
	}
	laterAgent := f.agent(t, "later")
	_, err := f.database.Exec(f.ctx, `INSERT INTO usage_declarations (agent_id, ticket_id, declared_at)
		VALUES ($1, $2, now() - interval '400 days')`, laterAgent, ticketID)
	require.NoError(t, err)

	deleted := f.sweep(t, prune.Config{LLMUsageRetention: 365 * 24 * time.Hour}).LLMUsage

	assert.Equal(t, int64(3), deleted, "an old mark, old guidance and the superseded declaration")
	assert.Equal(t, []string{"recent"}, f.usageKeys(t, f.ctx, "llm_usage_marks"))
	assert.Equal(t, []string{"recent"}, f.usageKeys(t, f.ctx, "llm_guidance_usage"))
	var left int
	require.NoError(t, f.database.QueryRow(f.ctx, `SELECT count(*) FROM usage_declarations`).Scan(&left))
	assert.Equal(t, 2, left, "the latest of each agent's scope")
}

// pendingAction inserts a proposal directly: what is under test is what
// prune does to a row in a given state, not how it got there.
func (f *fixture) pendingAction(t *testing.T, ctx context.Context, agentID uuid.UUID, ticketID int64, state string, decidedAgo, expiresIn time.Duration) uuid.UUID {
	t.Helper()
	id := uuid.New()
	var decided any
	if state != "pending" {
		decided = time.Now().Add(-decidedAgo)
	}
	_, err := f.database.Exec(ctx, `
		INSERT INTO pending_actions (id, ticket_id, agent_id, kind, payload, base_status, base_revision, status,
		                             created_at, expires_at, decided_at)
		VALUES ($1, $2, $3, 'ready', '{}', 'preparing', 0, $4, now() - interval '200 days', $5, $6)`,
		id, ticketID, agentID, state, time.Now().Add(expiresIn), decided)
	require.NoError(t, err)
	return id
}

func (f *fixture) pendingActionStates(t *testing.T, ctx context.Context) map[uuid.UUID]string {
	t.Helper()
	rows, err := f.database.Query(ctx, `SELECT id, status FROM pending_actions`)
	require.NoError(t, err)
	defer rows.Close()
	out := map[uuid.UUID]string{}
	for rows.Next() {
		var id uuid.UUID
		var status string
		require.NoError(t, rows.Scan(&id, &status))
		out[id] = status
	}
	require.NoError(t, rows.Err())
	return out
}

func TestPendingActions_ExpireThePastDueAndDeleteOnlyOldDecidedOnes(t *testing.T) {
	f := newFixture(t)
	console := f.agent(t, "console")
	other := dbtest.NewTenant(t, f.database)
	otherCtx := db.WithTenant(context.Background(), other)
	otherAgent, err := f.agents.Create(otherCtx, "console")
	require.NoError(t, err)
	mk := func(ctx context.Context, title string) int64 {
		created, err := f.tickets.Create(ctx, &ticket.CreateParams{Title: title})
		require.NoError(t, err)
		return created.ID
	}
	t1, t2, t3, t4, theirs := mk(f.ctx, "1"), mk(f.ctx, "2"), mk(f.ctx, "3"), mk(f.ctx, "4"), mk(otherCtx, "theirs")

	day := 24 * time.Hour
	oldDecided := f.pendingAction(t, f.ctx, console, t1, "approved", 100*day, 0)
	recentDecided := f.pendingAction(t, f.ctx, console, t2, "rejected", 10*day, 0)
	pastDue := f.pendingAction(t, f.ctx, console, t3, "pending", 0, -time.Hour)
	live := f.pendingAction(t, f.ctx, console, t4, "pending", 0, day)
	_ = f.pendingAction(t, otherCtx, otherAgent.ID, theirs, "approved", 100*day, 0)

	require.NoError(t, f.store.ExpirePendingActions(f.ctx, time.Now()))
	assert.Equal(t, "expired", f.pendingActionStates(t, f.ctx)[pastDue])
	assert.Equal(t, "pending", f.pendingActionStates(t, f.ctx)[live], "not yet due")

	deleted := f.sweep(t, prune.Config{PendingActionRetention: 90 * day}).PendingActions
	assert.Equal(t, int64(2), deleted)
	assert.NotContains(t, f.pendingActionStates(t, f.ctx), oldDecided)
	assert.Equal(t, map[uuid.UUID]string{recentDecided: "rejected", pastDue: "expired", live: "pending"},
		f.pendingActionStates(t, f.ctx), "the old decided one goes; a pending one never does")
	assert.Empty(t, f.pendingActionStates(t, otherCtx), "another tenant's old decided one goes in its own scope")
}

// conductorWithConversations is a conductor in the context's tenant with a
// conversation in each state, last touched age ago, each with a transcript
// event and a queued item.
func (f *fixture) conversation(t *testing.T, ctx context.Context, conductor, owner uuid.UUID, state string, age time.Duration) uuid.UUID {
	t.Helper()
	id := uuid.New()
	_, err := f.database.Exec(ctx, `
		INSERT INTO conductor_conversations (id, conductor_id, owner_user_id, state, next_seq, updated_at)
		VALUES ($1, $2, $3, $4, 1, now() - $5::interval)`,
		id, conductor, owner, state, age.String())
	require.NoError(t, err)
	_, err = f.database.Exec(ctx, `INSERT INTO conductor_conversation_events (conversation_id, seq, kind, body) VALUES ($1, 1, 'user', 'hello')`, id)
	require.NoError(t, err)
	return id
}

func (f *fixture) conversationIDs(t *testing.T, ctx context.Context) map[uuid.UUID]string {
	t.Helper()
	rows, err := f.database.Query(ctx, `SELECT id, state FROM conductor_conversations`)
	require.NoError(t, err)
	defer rows.Close()
	out := map[uuid.UUID]string{}
	for rows.Next() {
		var id uuid.UUID
		var state string
		require.NoError(t, rows.Scan(&id, &state))
		out[id] = state
	}
	require.NoError(t, rows.Err())
	return out
}

func (f *fixture) conductorOf(t *testing.T, ctx context.Context, tenant uuid.UUID, name string) (conductor, owner uuid.UUID) {
	t.Helper()
	owner = dbtest.NewUser(t, f.database)
	_, err := f.database.Global().Exec(context.Background(),
		`INSERT INTO global_tenant_memberships (id, tenant, user_id, role) VALUES ($1, $2, $3, 'member')`, uuid.New(), tenant, owner)
	require.NoError(t, err)
	conductor = uuid.New()
	_, err = f.database.Exec(ctx, `INSERT INTO agents (id, name, kind, owner_user_id) VALUES ($1, $2, 'conductor', $3)`, conductor, name, owner)
	require.NoError(t, err)
	return conductor, owner
}

func TestConversations_DeleteOnlyOldEndedOnesWithTheirTranscripts(t *testing.T) {
	f := newFixture(t)
	tenant, err := db.TenantFrom(f.ctx)
	require.NoError(t, err)
	conductor, owner := f.conductorOf(t, f.ctx, tenant, "c")
	otherTenant := dbtest.NewTenant(t, f.database)
	otherCtx := db.WithTenant(context.Background(), otherTenant)
	foreignConductor, foreignOwner := f.conductorOf(t, otherCtx, otherTenant, "c")

	day := 24 * time.Hour
	oldStopped := f.conversation(t, f.ctx, conductor, owner, "stopped", 100*day)
	oldFailed := f.conversation(t, f.ctx, conductor, owner, "failed", 100*day)
	recentStopped := f.conversation(t, f.ctx, conductor, owner, "stopped", 10*day)
	oldLive := f.conversation(t, f.ctx, conductor, owner, "ready", 100*day)
	foreign := f.conversation(t, otherCtx, foreignConductor, foreignOwner, "stopped", 100*day)

	deleted := f.sweep(t, prune.Config{ConversationRetention: 90 * day}).Conversations

	assert.Equal(t, int64(3), deleted)
	assert.Equal(t, map[uuid.UUID]string{recentStopped: "stopped", oldLive: "ready"}, f.conversationIDs(t, f.ctx),
		"old ended ones go; a recent one and a live one, however old, stay")
	assert.NotContains(t, f.conversationIDs(t, f.ctx), oldStopped)
	assert.NotContains(t, f.conversationIDs(t, f.ctx), oldFailed)
	assert.NotContains(t, f.conversationIDs(t, otherCtx), foreign, "another tenant's old ended one goes in its own scope")
	var events int
	require.NoError(t, f.database.QueryRow(f.ctx, `SELECT count(*) FROM conductor_conversation_events`).Scan(&events))
	assert.Equal(t, 2, events, "transcripts go with their conversations")
}

// A host that was away when its conversation was stopped is still told: the
// stop it has not acknowledged keeps the conversation from being pruned.
func TestConversations_KeepAnEndedOneWhoseWorkTheHostHasNotAcknowledged(t *testing.T) {
	f := newFixture(t)
	tenant, err := db.TenantFrom(f.ctx)
	require.NoError(t, err)
	conductor, owner := f.conductorOf(t, f.ctx, tenant, "c")
	day := 24 * time.Hour
	waiting := f.conversation(t, f.ctx, conductor, owner, "stopped", 100*day)
	acknowledged := f.conversation(t, f.ctx, conductor, owner, "stopped", 100*day)
	_, err = f.database.Exec(f.ctx, `INSERT INTO conductor_queues (conductor_id, next_seq, acked_seq) VALUES ($1, 1, 0)`, conductor)
	require.NoError(t, err)
	_, err = f.database.Exec(f.ctx, `INSERT INTO conductor_work (conductor_id, seq, kind, conversation_id) VALUES ($1, 1, 'stop', $2)`, conductor, waiting)
	require.NoError(t, err)

	deleted := f.sweep(t, prune.Config{ConversationRetention: 90 * day}).Conversations

	assert.Equal(t, int64(1), deleted)
	assert.Equal(t, map[uuid.UUID]string{waiting: "stopped"}, f.conversationIDs(t, f.ctx))
	assert.NotContains(t, f.conversationIDs(t, f.ctx), acknowledged)
}

type activityRows struct{ blocks, denials, narrowings, holds int64 }

// activity inserts one row per lease-activity table, started 100 days ago.
// endedAgo is how long ago it ended, or nil for one still live; a narrowing
// has no live state, so for it endedAgo is its age.
func (f *fixture) activity(t *testing.T, ticketID, blockerID int64, agentID uuid.UUID, path string, endedAgo *time.Duration) activityRows {
	t.Helper()
	var ago *string
	if endedAgo != nil {
		s := endedAgo.String()
		ago = &s
	}
	var r activityRows
	require.NoError(t, f.database.QueryRow(f.ctx, `
		INSERT INTO scope_blocks (ticket_id, blocking_ticket_id, path, started_at, ended_at, end_reason)
		VALUES ($1, $2, $3, now() - interval '100 days', now() - $4::interval,
		        CASE WHEN $4::interval IS NULL THEN NULL ELSE 'cleared' END) RETURNING id`,
		ticketID, blockerID, path, ago).Scan(&r.blocks))
	require.NoError(t, f.database.QueryRow(f.ctx, `
		INSERT INTO scope_extension_denials (ticket_id, agent_id, path, blocking_ticket_id, blocking_path, denied_at, resolved_at, resolution)
		VALUES ($1, $2, $3, $4, $3, now() - interval '100 days', now() - $5::interval,
		        CASE WHEN $5::interval IS NULL THEN NULL ELSE 'granted' END) RETURNING id`,
		ticketID, agentID, path, blockerID, ago).Scan(&r.denials))
	narrowedAgo := "100 days"
	if ago != nil {
		narrowedAgo = *ago
	}
	require.NoError(t, f.database.QueryRow(f.ctx, `
		INSERT INTO scope_narrowings (ticket_id, agent_id, removed, kept, narrowed_at)
		VALUES ($1, $2, 1, 1, now() - $3::interval) RETURNING id`, ticketID, agentID, narrowedAgo).Scan(&r.narrowings))
	require.NoError(t, f.database.QueryRow(f.ctx, `
		INSERT INTO lock_holds (name, ticket_id, agent_id, acquired_at, released_at)
		VALUES ($1, $2, $3, now() - interval '100 days', now() - $4::interval) RETURNING id`,
		path, ticketID, agentID, ago).Scan(&r.holds))
	return r
}

func TestPruneLeaseActivity_DeletesOnlyOldEndedRows(t *testing.T) {
	f := newFixture(t)
	agentID := f.agent(t, "worker")
	mk := func(title string) int64 {
		created, err := f.tickets.Create(f.ctx, &ticket.CreateParams{Title: title})
		require.NoError(t, err)
		return created.ID
	}
	// No lease is ever taken on these tickets, so an open denial is dead.
	a, b := mk("a"), mk("b")
	old, recent := 40*24*time.Hour, 5*24*time.Hour

	oldEnded := f.activity(t, a, b, agentID, "old", &old)
	recentEnded := f.activity(t, a, b, agentID, "recent", &recent)
	live := f.activity(t, a, b, agentID, "live", nil)

	deleted := f.sweep(t, prune.Config{LeaseRetention: 30 * 24 * time.Hour}).LeaseActivity
	// Four tables' old rows, plus the live row of the two tables whose live
	// state is dead: an open denial of a ticket with no lease, and the
	// narrowing, which has no live state and is 100 days old.
	assert.Equal(t, int64(4+2), deleted)

	assert.Equal(t, []int64{recentEnded.blocks, live.blocks}, f.ids(t, "scope_blocks"))
	assert.Equal(t, []int64{recentEnded.holds, live.holds}, f.ids(t, "lock_holds"))
	assert.Equal(t, []int64{recentEnded.narrowings}, f.ids(t, "scope_narrowings"))
	assert.Equal(t, []int64{recentEnded.denials}, f.ids(t, "scope_extension_denials"))
	assert.NotEqual(t, oldEnded.blocks, live.blocks)
}

// An open denial of a ticket whose lease is live can still be granted or
// narrowed away, so it stays however old.
func TestPruneLeaseActivity_KeepsAnOpenDenialWhileTheTicketIsLeased(t *testing.T) {
	f := newFixture(t)
	agentID := f.agent(t, "holder")
	held, err := f.tickets.Create(f.ctx, &ticket.CreateParams{Title: "held"})
	require.NoError(t, err)
	other, err := f.tickets.Create(f.ctx, &ticket.CreateParams{Title: "other"})
	require.NoError(t, err)
	_, err = f.tickets.Claim(f.ctx, agentID, &held.ID)
	require.NoError(t, err)

	// The denial was refused under the lease, so it began after it.
	_, err = f.database.Exec(f.ctx, `UPDATE leases SET acquired_at = now() - interval '101 days'`)
	require.NoError(t, err)
	rows := f.activity(t, held.ID, other.ID, agentID, "p", nil)

	f.sweep(t, prune.Config{LeaseRetention: 30 * 24 * time.Hour})
	assert.Equal(t, []int64{rows.denials}, f.ids(t, "scope_extension_denials"))
	assert.Equal(t, []int64{rows.blocks}, f.ids(t, "scope_blocks"))
	assert.Equal(t, []int64{rows.holds}, f.ids(t, "lock_holds"))
}

func TestPruneLeaseActivity_DeletesEveryTenantsRows(t *testing.T) {
	f := newFixture(t)
	otherTenant := *f
	otherTenant.ctx = db.WithTenant(context.Background(), dbtest.NewTenant(t, f.database))
	other := &otherTenant
	agentID := f.agent(t, "worker")
	mk := func(f *fixture) int64 {
		created, err := f.tickets.Create(f.ctx, &ticket.CreateParams{Title: "t"})
		require.NoError(t, err)
		return created.ID
	}
	old := 40 * 24 * time.Hour
	f.activity(t, mk(f), mk(f), agentID, "p", &old)
	otherAgent := other.agent(t, "worker")
	other.activity(t, mk(other), mk(other), otherAgent, "p", &old)

	f.sweep(t, prune.Config{LeaseRetention: 30 * 24 * time.Hour})
	assert.Zero(t, f.count(t, "lock_holds"))
	assert.Zero(t, other.count(t, "lock_holds"))
}

// A denial left open by an earlier lease is not kept alive by a later claim
// of the same ticket, which the report cannot resolve it with.
func TestPruneLeaseActivity_DeletesAnOpenDenialOfAnEarlierLeaseDespiteAReclaim(t *testing.T) {
	f := newFixture(t)
	agentID := f.agent(t, "holder")
	held, err := f.tickets.Create(f.ctx, &ticket.CreateParams{Title: "held"})
	require.NoError(t, err)
	other, err := f.tickets.Create(f.ctx, &ticket.CreateParams{Title: "other"})
	require.NoError(t, err)
	_, err = f.tickets.Claim(f.ctx, agentID, &held.ID)
	require.NoError(t, err)

	f.activity(t, held.ID, other.ID, agentID, "p", nil)

	f.sweep(t, prune.Config{LeaseRetention: 30 * 24 * time.Hour})
	assert.Zero(t, f.count(t, "scope_extension_denials"))
}
