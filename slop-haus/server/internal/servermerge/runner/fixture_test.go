package runner_test

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/landing"
	"log/slog"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-clock"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/agent"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit/audittest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/change"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost/codehosttest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/servermerge/runner"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/scope"
)

const (
	hostName = "git.example"
	headA    = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	headB    = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

type workspaceFake struct{}

func (workspaceFake) Place(context.Context, []string) (*ticket.Placement, error) {
	return &ticket.Placement{Declared: true, Repos: []scope.WorkspaceRepo{{Path: "repo", Remote: hostName + "/acme/app"}}}, nil
}

type namesFake struct{}

func (namesFake) Agent(context.Context, uuid.UUID) (string, error) { return "someone", nil }
func (namesFake) User(context.Context, uuid.UUID) (string, error)  { return "Alex Approver", nil }

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *syncBuffer) audited(t *testing.T, action audit.Action) []map[string]any {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []map[string]any
	for _, raw := range strings.Split(strings.TrimSpace(s.buf.String()), "\n") {
		if raw == "" {
			continue
		}
		var line map[string]any
		require.NoError(t, json.Unmarshal([]byte(raw), &line))
		if line["action"] == string(action) {
			out = append(out, line)
		}
	}
	return out
}

// changesWrap lets a test stand at a point of an attempt without timing:
// the real change service answers, after the hook has run.
type changesWrap struct {
	svc      *change.Service
	mu       sync.Mutex
	merges   []change.MergeInput
	onChecks func(ctx context.Context)
	onMerge  func(ctx context.Context)
}

func (c *changesWrap) Checks(ctx context.Context, tenant, agent uuid.UUID, sha string) (*change.Report, error) {
	if c.onChecks != nil {
		c.onChecks(ctx)
	}
	return c.svc.Checks(ctx, tenant, agent, sha)
}

func (c *changesWrap) Merge(ctx context.Context, tenant, agent uuid.UUID, in *change.MergeInput) (*change.Merged, error) {
	c.mu.Lock()
	c.merges = append(c.merges, *in)
	c.mu.Unlock()
	if c.onMerge != nil {
		c.onMerge(ctx)
	}
	return c.svc.Merge(ctx, tenant, agent, in)
}

type toolsFake struct{ connected bool }

func (t toolsFake) Connected(context.Context) (bool, error) { return t.connected, nil }

type fixture struct {
	t        *testing.T
	clock    clock.Clock // nil is the wall clock
	database *db.DB
	ctx      context.Context
	tenant   uuid.UUID
	host     *codehosttest.Fake
	tickets  *ticket.Service
	agents   *agent.Service
	changes  *change.Service
	wrap     *changesWrap
	store    *ticket.PostgresStore
	outcomes *landing.Store
	log      *syncBuffer
	approver uuid.UUID
	creator  uuid.UUID
	tools    toolsFake
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	database := dbtest.WithDB(t)
	tenant := dbtest.NewTenant(t, database)
	f := &fixture{
		t: t, database: database, tenant: tenant, ctx: db.WithTenant(context.Background(), tenant),
		host: codehosttest.NewLinkHost(hostName), log: &syncBuffer{}, tools: toolsFake{connected: true},
		approver: dbtest.NewUser(t, database), creator: dbtest.NewUser(t, database),
	}
	f.host.Opened = codehost.ChangeRef{Host: hostName, Owner: "acme", Repo: "app", Number: 7}
	f.host.MergedWith = codehost.Merged{SHA: "mergecommit"}
	f.setChecks(headA, codehost.Check{Name: "ci/build", State: codehost.CheckSuccess})

	f.store = ticket.NewPostgresStore(database, ticket.WithMergeQueue(landing.NewDequeuer()))
	f.tickets = ticket.NewService(f.store, slog.DiscardHandler, ticket.WithCodeHosts(codehosttest.Registry(f.host)))
	f.agents = agent.NewService(agent.NewPostgresStore(database), f.tickets, nil, slog.DiscardHandler)
	svc, err := change.NewService(&change.Options{
		Hosts: codehosttest.Registry(f.host), Tickets: f.tickets, Workspace: workspaceFake{}, Store: change.NewPostgresStore(database),
		Names: namesFake{}, Detect: codehost.LineNumberChangeDetectionStrategy{},
		Events:   audit.New(slog.NewJSONHandler(f.log, nil), audittest.NewCounter()),
		BoardURL: "https://board.example", Logger: slog.DiscardHandler,
	})
	require.NoError(t, err)
	f.changes = svc
	f.wrap = &changesWrap{svc: svc}
	f.outcomes = landing.NewStore(database)
	return f
}

func (f *fixture) setChecks(head string, checks ...codehost.Check) {
	f.host.ChecksOf = map[codehost.ChangeRef]*codehost.Checks{f.host.Opened: codehost.NewChecks(head, "main", checks)}
	f.host.CompareOf = map[codehost.ChangeRef]*codehost.BaseComparison{f.host.Opened: {Base: "main", ContainsTip: true}}
}

func (f *fixture) runner(cfg runner.Config) *runner.Runner { return f.runnerWith(cfg, nil) }

func (f *fixture) runnerWith(cfg runner.Config, routing runner.Routing) *runner.Runner {
	f.t.Helper()
	if cfg.PollInterval == 0 {
		cfg.PollInterval = time.Millisecond
	}
	r, err := runner.New(&runner.Options{
		Tickets: ticket.NewServerAgent(f.tickets), Changes: f.wrap, Tools: f.tools, Outcomes: f.outcomes, Routing: routing,
		Reaper: runner.NewPostgresReaper(f.database), Config: cfg, Logger: slog.DiscardHandler, Clock: f.clock,
	})
	require.NoError(f.t, err)
	f.t.Cleanup(r.Close)
	return r
}

// approved is an author's gated ticket with its change opened, handed over
// at headA and approved by a person: queued for the author, who is owed it.
type approved struct {
	author   uuid.UUID
	ticketID int64
	approval *ticket.Approval
}

func (f *fixture) approvedChange() *approved {
	f.t.Helper()
	author, err := f.agents.Create(f.ctx, "author")
	require.NoError(f.t, err)
	created, err := f.tickets.Create(f.ctx, &ticket.CreateParams{
		Title: "Add the thing", Paths: []model.Path{{Path: "app/server/"}}, Status: ticket.StatusOpen,
		CreatedByUserID: &f.creator, RequiresApproval: true,
	})
	require.NoError(f.t, err)
	claimed, err := f.tickets.Claim(f.ctx, author.ID, &created.ID)
	require.NoError(f.t, err)
	require.NotNil(f.t, claimed.Ticket)
	_, err = f.changes.Open(f.ctx, f.tenant, author.ID, &change.OpenInput{Branch: ticket.BranchName(created.ID, claimed.Attempt.Number), Title: "Add the thing", Body: "It adds the thing."})
	require.NoError(f.t, err)
	_, err = f.changes.Review(f.ctx, f.tenant, author.ID, &ticket.ReviewParams{Reason: "reviewed by a person", PRURL: "https://git.example/acme/app/pull/7", Handoff: "ready"})
	require.NoError(f.t, err)
	_, err = f.tickets.Approve(f.ctx, created.ID, f.approver)
	require.NoError(f.t, err)
	return &approved{author: author.ID, ticketID: created.ID, approval: &ticket.Approval{
		TenantID: f.tenant, TicketID: created.ID, Attempt: claimed.Attempt.Number, ApproverID: f.approver, ApprovedAt: time.Now(),
	}}
}

func (f *fixture) attempt(a *approved, r *runner.Runner) {
	f.t.Helper()
	require.NoError(f.t, r.Approved(context.Background(), a.approval))
	r.Wait()
}

// attemptAdvancing runs the attempt, moving the fake clock by step whenever
// the runner is waiting on it, until the attempt ends.
func (f *fixture) attemptAdvancing(a *approved, r *runner.Runner, fake *clock.Fake, step time.Duration) {
	f.t.Helper()
	require.NoError(f.t, r.Approved(context.Background(), a.approval))
	done := make(chan struct{})
	go func() { r.Wait(); close(done) }()
	for {
		select {
		case <-done:
			return
		default:
		}
		if fake.Waiters() > 0 {
			fake.Advance(step)
		}
		runtime.Gosched()
	}
}

func (f *fixture) outcomesNow() []landing.Record {
	f.t.Helper()
	recs, err := f.outcomes.List(f.ctx, time.Now().Add(-time.Hour), time.Now().Add(time.Hour), 50)
	require.NoError(f.t, err)
	return recs
}

func (f *fixture) lockHolder() *ticket.LockHolder {
	f.t.Helper()
	st, err := f.tickets.IntegrationLockState(f.ctx)
	require.NoError(f.t, err)
	return st.Holder
}

type attemptRow struct {
	status string
	owed   *uuid.UUID
}

func (f *fixture) attemptOf(a *approved) attemptRow {
	f.t.Helper()
	var row attemptRow
	require.NoError(f.t, f.database.QueryRow(f.ctx, `SELECT status, resume_agent_id FROM attempts WHERE ticket_id = $1 AND number = 1`, a.ticketID).Scan(&row.status, &row.owed))
	return row
}

func (f *fixture) comments(a *approved) string {
	f.t.Helper()
	rows, err := f.database.Query(f.ctx, `SELECT body FROM ticket_comments WHERE ticket_id = $1 ORDER BY id`, a.ticketID)
	require.NoError(f.t, err)
	defer rows.Close()
	var all []string
	for rows.Next() {
		var b string
		require.NoError(f.t, rows.Scan(&b))
		all = append(all, b)
	}
	return strings.Join(all, "\n")
}
