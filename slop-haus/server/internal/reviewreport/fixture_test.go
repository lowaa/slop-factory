package reviewreport_test

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-review/report"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/agent"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/reviewreport"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/schema"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

func TestMain(m *testing.M) {
	dbtest.Main(m, schema.Migrations)
}

// fixture is the real store over a fresh database with two tenants; ctx is
// tenant A's.
type fixture struct {
	database *db.DB
	store    *reviewreport.PostgresStore
	agents   *agent.Service
	ctx      context.Context
	otherCtx context.Context
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	database := dbtest.WithDB(t)
	return &fixture{
		database: database,
		store:    reviewreport.NewPostgresStore(database),
		agents:   agent.NewService(agent.NewPostgresStore(database), nil, nil, slog.DiscardHandler),
		ctx:      db.WithTenant(context.Background(), dbtest.NewTenant(t, database)),
		otherCtx: db.WithTenant(context.Background(), dbtest.NewTenant(t, database)),
	}
}

func (f *fixture) agent(t *testing.T, ctx context.Context, name string) uuid.UUID {
	t.Helper()
	created, err := f.agents.Create(ctx, name)
	require.NoError(t, err)
	return created.AgentID()
}

// ticket makes a ticket, which comes with attempt 1.
func (f *fixture) ticket(t *testing.T, ctx context.Context) int64 {
	t.Helper()
	var id int64
	require.NoError(t, f.database.QueryRow(ctx, `INSERT INTO tickets (title) VALUES ('t') RETURNING id`).Scan(&id))
	return id
}

// addAttempt makes the ticket's next attempt, as the ticket store numbers one.
func (f *fixture) addAttempt(t *testing.T, ctx context.Context, ticketID int64) int {
	t.Helper()
	var n int
	require.NoError(t, f.database.InTx(ctx, func(q db.Querier) error {
		if err := q.QueryRow(ctx, `UPDATE tickets SET next_attempt_number = next_attempt_number + 1
			 WHERE id = $1 RETURNING next_attempt_number - 1`, ticketID).Scan(&n); err != nil {
			return err
		}
		_, err := q.Exec(ctx, `INSERT INTO attempts (ticket_id, number, status) VALUES ($1, $2, 'queued')`, ticketID, n)
		return err
	}))
	return n
}

// hold gives agentID a live lease on the attempt, and returns its id.
func (f *fixture) hold(t *testing.T, ctx context.Context, agentID uuid.UUID, ticketID int64, attempt int) int64 {
	t.Helper()
	var id int64
	require.NoError(t, f.database.QueryRow(ctx, `
		INSERT INTO leases (ticket_id, attempt_number, agent_id, acquired_at, expires_at)
		VALUES ($1, $2, $3, now(), now() + interval '1 hour') RETURNING id`, ticketID, attempt, agentID).Scan(&id))
	return id
}

func (f *fixture) release(t *testing.T, ctx context.Context, leaseID int64) {
	t.Helper()
	_, err := f.database.Exec(ctx, `UPDATE leases SET released_at = now(), release_reason = 'done' WHERE id = $1`, leaseID)
	require.NoError(t, err)
}

// insert stores run as posted by agentID itself.
func (f *fixture) insert(t *testing.T, ctx context.Context, agentID uuid.UUID, run *report.Run) *reviewreport.Inserted {
	t.Helper()
	return f.insertBy(t, ctx, byAgent(agentID), run)
}

func (f *fixture) insertBy(t *testing.T, ctx context.Context, poster *reviewreport.Poster, run *report.Run) *reviewreport.Inserted {
	t.Helper()
	got, err := f.store.InsertRun(ctx, poster, run)
	require.NoError(t, err)
	return got
}

func byAgent(id uuid.UUID) *reviewreport.Poster {
	return &reviewreport.Poster{Kind: reviewreport.PostedByAgent, ID: id}
}

func bySupervisor(id uuid.UUID) *reviewreport.Poster {
	return &reviewreport.Poster{Kind: reviewreport.PostedBySupervisor, ID: id}
}

// supervisor registers a supervisor, as an agents row of its kind.
func (f *fixture) supervisor(t *testing.T, ctx context.Context, name string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	_, err := f.database.Exec(ctx, `INSERT INTO agents (id, name, kind, slot_cap) VALUES ($1, $2, 'supervisor', 4)`, id, name)
	require.NoError(t, err)
	return id
}

// slot registers a worker the supervisor runs.
func (f *fixture) slot(t *testing.T, ctx context.Context, supervisorID uuid.UUID, name string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	_, err := f.database.Exec(ctx, `INSERT INTO agents (id, name, kind, supervisor_agent_id) VALUES ($1, $2, 'worker', $3)`,
		id, name, supervisorID)
	require.NoError(t, err)
	return id
}

var t0 = time.Date(2026, 9, 25, 10, 0, 0, 0, time.UTC)

const headSHA = "89abcdef0123456789abcdef0123456789abcdef"

// validRun is a completed review of branch that passes report.Validate, with
// one finding per persona given.
func validRun(branch string, findingPersonas ...string) *report.Run {
	r := &report.Run{
		SchemaVersion: report.CompletedSchemaVersion,
		ID:            uuid.New(),
		Kind:          report.KindReview,
		RepoRemote:    "git@github.com:example/widgets.git",
		Branch:        branch,
		BaseRef:       "0123456789abcdef0123456789abcdef01234567",
		HeadSHA:       headSHA,
		HeadAuthor:    "Ada Lovelace <ada@example.com>",
		ToolVersion:   report.ToolVersion{Kind: "git_sha", Value: "c4ca88d"},
		Provider:      "claude",
		Model:         "claude-sonnet-5-5",
		Personas:      []string{"security", "standard"},
		Verdict:       "request_changes",
		Summary:       "merged from 2 sessions",
		Sessions: []report.Session{
			{Persona: "security", Part: 1, Parts: 1, Files: []string{"a.go"}},
			{Persona: "standard", Part: 1, Parts: 1, Files: []string{"a.go"}},
		},
		StartedAt:  t0,
		FinishedAt: t0.Add(time.Minute),
	}
	for i, p := range findingPersonas {
		r.Findings = append(r.Findings, report.Finding{
			ID: uuid.New(), File: "a.go", Line: i + 1, Severity: "major", Summary: "finding " + p, Detail: "d",
			Personas: []string{p},
		})
	}
	return r
}

func attempt(ticketID int64, n int) ticket.AttemptRef {
	return ticket.AttemptRef{TicketID: ticketID, Number: n}
}
