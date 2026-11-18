package change_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/agent"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit/audittest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/change"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost/codehosttest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/scope"
)

const (
	hostName = "git.example"
	headA    = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	headB    = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	branch   = "ticket-1-thing"
)

var appRepo = codehost.Repo{Owner: "acme", Name: "app"}

// workspaceFake is the manifest as the test says it is: it answers Place
// with whatever placement it was given and decides nothing.
type workspaceFake struct {
	mu        sync.Mutex
	placement *ticket.Placement
	// byScope, when set, answers a ticket whose first scope starts with the
	// key instead of placement.
	byScope map[string]*ticket.Placement
}

func (w *workspaceFake) Place(_ context.Context, scopes []string) (*ticket.Placement, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for prefix, pl := range w.byScope {
		if len(scopes) > 0 && strings.HasPrefix(scopes[0], prefix) {
			return pl, nil
		}
	}
	return w.placement, nil
}

func (w *workspaceFake) set(p *ticket.Placement) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.placement = p
}

func placedIn(remotes ...string) *ticket.Placement {
	pl := &ticket.Placement{Declared: true}
	for _, r := range remotes {
		pl.Repos = append(pl.Repos, scope.WorkspaceRepo{Path: "repo", Remote: r})
	}
	return pl
}

type namesFake struct {
	agents map[uuid.UUID]string
	users  map[uuid.UUID]string
}

func (n namesFake) Agent(_ context.Context, id uuid.UUID) (string, error) {
	return n.agents[id], nil
}

func (n namesFake) User(_ context.Context, id uuid.UUID) (string, error) {
	return n.users[id], nil
}

// syncBuffer is a log the audit recorder writes and the test reads.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

// audited is the audit lines of one action, as the log holds them.
func (s *syncBuffer) audited(t *testing.T, action audit.Action) []map[string]any {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	var lines []map[string]any
	for _, raw := range strings.Split(strings.TrimSpace(s.buf.String()), "\n") {
		if raw == "" {
			continue
		}
		var line map[string]any
		require.NoError(t, json.Unmarshal([]byte(raw), &line), raw)
		if line["action"] == string(action) {
			lines = append(lines, line)
		}
	}
	return lines
}

type fixture struct {
	t        *testing.T
	database *db.DB
	ctx      context.Context
	tenant   uuid.UUID
	host     *codehosttest.Fake
	tickets  *ticket.Service
	// settled is the closing rule a test wires to the ticket transitions, as
	// the server does; the service it belongs to is built from the tickets.
	settled  ticket.SettledHook
	agents   *agent.Service
	ws       *workspaceFake
	store    *change.PostgresStore
	svc      *change.Service
	detect   codehost.ChangeDetectionStrategy
	log      *syncBuffer
	counter  *audittest.Counter
	names    namesFake
	creator  uuid.UUID
	approver uuid.UUID
	// baseBranch is the service's configured base; empty is its default.
	baseBranch string
	// trees is the tree reader a batch merge needs; nil leaves it unset.
	trees change.TreeReader
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	database := dbtest.WithDB(t)
	tenant := dbtest.NewTenant(t, database)
	f := &fixture{
		t: t, database: database, tenant: tenant, detect: codehost.LineNumberChangeDetectionStrategy{},
		ctx:  db.WithTenant(context.Background(), tenant),
		host: codehosttest.NewLinkHost(hostName),
		ws:   &workspaceFake{placement: placedIn(hostName + "/acme/app")},
		log:  &syncBuffer{}, counter: audittest.NewCounter(),
		creator: dbtest.NewUser(t, database), approver: dbtest.NewUser(t, database),
	}
	f.names = namesFake{users: map[uuid.UUID]string{f.creator: "Casey Creator", f.approver: "Alex Approver"}, agents: map[uuid.UUID]string{}}
	f.host.Opened = codehost.ChangeRef{Host: hostName, Owner: "acme", Repo: "app", Number: 7}
	f.host.MergedWith = codehost.Merged{SHA: "mergecommit"}
	f.setChecks(headA, codehost.Check{Name: "ci/build", State: codehost.CheckSuccess})

	f.tickets = ticket.NewService(f.ticketStore(), slog.DiscardHandler, ticket.WithCodeHosts(codehosttest.Registry(f.host)))
	f.agents = agent.NewService(agent.NewPostgresStore(database), f.tickets, nil, slog.DiscardHandler)
	f.store = change.NewPostgresStore(database)
	f.svc = f.serviceWith(f.host)
	return f
}

// serviceWith is the service over a registry holding host, sharing the rest
// of the fixture.
func (f *fixture) ticketStore() *ticket.PostgresStore {
	return ticket.NewPostgresStore(f.database, ticket.WithSettledHook(func(ctx context.Context, ids []int64) {
		if f.settled != nil {
			f.settled(ctx, ids)
		}
	}))
}

func (f *fixture) serviceWith(host *codehosttest.Fake) *change.Service {
	return f.serviceOver(host, f.tickets)
}

// serviceOver is serviceWith with the ticket side stood in for.
func (f *fixture) serviceOver(host *codehosttest.Fake, tickets change.Tickets) *change.Service {
	svc, err := change.NewService(&change.Options{
		Hosts: codehosttest.Registry(host), Tickets: tickets, Workspace: f.ws, Store: f.store,
		Names: f.names, Detect: f.detect, BaseBranch: f.baseBranch, Trees: f.trees,
		Events:   audit.New(slog.NewJSONHandler(f.log, nil), f.counter),
		BoardURL: "https://board.example", Logger: slog.DiscardHandler,
	})
	require.NoError(f.t, err)
	return svc
}

func (f *fixture) setChecks(head string, checks ...codehost.Check) {
	f.host.ChecksOf = map[codehost.ChangeRef]*codehost.Checks{f.host.Opened: codehost.NewChecks(head, "main", checks)}
	f.host.CompareOf = map[codehost.ChangeRef]*codehost.BaseComparison{f.host.Opened: {Base: "main", ContainsTip: true}}
}

// setBase is the base the host says the change targets now.
func (f *fixture) setBase(base string) {
	f.host.ChecksOf[f.host.Opened].Base = base
}

func (f *fixture) newAgent(name string) uuid.UUID {
	f.t.Helper()
	created, err := f.agents.Create(f.ctx, name)
	require.NoError(f.t, err)
	f.names.agents[created.ID] = name
	return created.ID
}

// holder is an agent holding a ticket in the repo's scope.
type holder struct {
	agent  uuid.UUID
	ticket *ticket.Ticket
	// attempt is the attempt the agent holds.
	attempt int
}

// recordUnderOldName renames the head of the attempt's current change to one
// from before attempt branch names were enforced, which is what a change
// opened at an earlier deploy looks like.
func (f *fixture) recordUnderOldName(h *holder) {
	f.t.Helper()
	_, err := f.database.Exec(f.ctx, `UPDATE ticket_changes SET head_branch = 'ticket-old-name' WHERE ticket_id = $1 AND attempt_number = $2 AND superseded_at IS NULL`, h.ticket.ID, h.attempt)
	require.NoError(f.t, err)
}

// branchOf is the branch slop-haus names for the holder's attempt.
func (f *fixture) branchOf(h *holder) string { return ticket.BranchName(h.ticket.ID, h.attempt) }

// hold files a ticket in the app repo and has a new agent claim it.
func (f *fixture) hold(requiresApproval bool) *holder {
	f.t.Helper()
	a := f.newAgent("builder-1")
	created, err := f.tickets.Create(f.ctx, &ticket.CreateParams{
		Title: "Add the thing", Paths: []model.Path{{Path: "repo/app/server/"}}, Status: ticket.StatusOpen,
		CreatedByUserID: &f.creator, RequiresApproval: requiresApproval,
	})
	require.NoError(f.t, err)
	claimed, err := f.tickets.Claim(f.ctx, a, &created.ID)
	require.NoError(f.t, err)
	require.NotNil(f.t, claimed.Ticket)
	return &holder{agent: a, ticket: claimed.Ticket, attempt: claimed.Attempt.Number}
}

// hold2 is a second holder, on a ticket of its own.
func (f *fixture) hold2() *holder {
	f.t.Helper()
	a := f.newAgent("builder-2")
	created, err := f.tickets.Create(f.ctx, &ticket.CreateParams{
		Title: "Another", Paths: []model.Path{{Path: "app/other/"}}, Status: ticket.StatusOpen,
	})
	require.NoError(f.t, err)
	claimed, err := f.tickets.Claim(f.ctx, a, &created.ID)
	require.NoError(f.t, err)
	require.NotNil(f.t, claimed.Ticket)
	return &holder{agent: a, ticket: claimed.Ticket, attempt: claimed.Attempt.Number}
}

func (f *fixture) lock(h *holder) {
	f.t.Helper()
	require.NoError(f.t, f.tickets.AcquireIntegrationLock(f.ctx, h.agent))
}

// approve is a human's approval of the held ticket's change. Written
// directly: what is under test is what the gate makes of the approval, not
// how the review flow reaches it (the ticket package tests that).
func (f *fixture) approve(h *holder) {
	f.t.Helper()
	_, err := f.database.Exec(f.ctx, `UPDATE attempts SET approved_at = now(), approved_by_user_id = $2 WHERE ticket_id = $1 AND number = 1`, h.ticket.ID, f.approver)
	require.NoError(f.t, err)
}

// handOver is the agent's ticket_finish of the change it opened, and the
// human's approval of it, so the approval is of whatever head the host has
// at the hand-over. The agent then claims the ticket again and takes the
// lock, as it does to merge. The ticket must require approval.
func (f *fixture) handOverAndApprove(h *holder) *holder {
	f.t.Helper()
	_, err := f.svc.Review(f.ctx, f.tenant, h.agent, &ticket.ReviewParams{Reason: "reviewed by a person", PRURL: "https://git.example/acme/app/pull/7", Handoff: "ready"})
	require.NoError(f.t, err)
	_, err = f.tickets.Approve(f.ctx, h.ticket.ID, f.approver)
	require.NoError(f.t, err)
	claimed, err := f.tickets.Claim(f.ctx, h.agent, &h.ticket.ID)
	require.NoError(f.t, err)
	require.NotNil(f.t, claimed.Ticket)
	f.lock(h)
	return h
}

func (f *fixture) open(h *holder, draft bool) *change.Opened {
	f.t.Helper()
	opened, err := f.svc.Open(f.ctx, f.tenant, h.agent, &change.OpenInput{Branch: f.branchOf(h), Title: "Add the thing", Body: "It adds the thing.", Draft: draft})
	require.NoError(f.t, err)
	return opened
}

func (f *fixture) merge(h *holder, expected string) (*change.Merged, error) {
	return f.svc.Merge(f.ctx, f.tenant, h.agent, &change.MergeInput{ExpectedSHA: expected})
}

// openRecord records a change as the service does, in a transaction of its
// own, and answers the store's error as it is.
func (f *fixture) openRecord(ctx context.Context, rec *change.Record) (*change.Record, error) {
	var out *change.Record
	err := f.database.InTx(ctx, func(q db.Querier) error {
		var err error
		out, err = f.store.Open(ctx, q, rec)
		return err
	})
	return out, err
}
