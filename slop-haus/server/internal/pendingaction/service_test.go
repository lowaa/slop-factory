package pendingaction_test

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/agent"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/pendingaction"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/approvaltest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

var epoch = time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)

// clock is a settable time source shared by a fixture's services.
type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

type fixture struct {
	database *db.DB
	ctx      context.Context
	tenant   uuid.UUID
	clock    *clock
	svc      *pendingaction.Service
	tickets  *ticket.Service
	// approvals hears every approval tickets commits.
	approvals *approvaltest.Recorder
	agents    *agent.Service
}

func newFixture(t *testing.T, cfg pendingaction.Config) *fixture {
	t.Helper()
	return newFixtureOn(t, cfg, dbtest.WithDB(t))
}

func newFixtureOn(t *testing.T, cfg pendingaction.Config, database *db.DB) *fixture {
	t.Helper()
	tenant := dbtest.NewTenant(t, database)
	approvals := &approvaltest.Recorder{}
	tickets := ticket.NewService(ticket.NewPostgresStore(database), slog.DiscardHandler, ticket.WithApprovalHook(approvals))
	f := &fixture{
		database: database,
		ctx:      db.WithTenant(context.Background(), tenant),
		tenant:   tenant,
		clock:    &clock{now: epoch},
		tickets:  tickets, approvals: approvals,
		agents: agent.NewService(agent.NewPostgresStore(database), tickets, nil, slog.DiscardHandler),
	}
	f.svc = f.serviceFor(cfg)
	return f
}

func (f *fixture) serviceFor(cfg pendingaction.Config) *pendingaction.Service {
	svc := pendingaction.NewService(pendingaction.NewPostgresStore(f.database), cfg, slog.DiscardHandler)
	svc.SetNow(f.clock.Now)
	return svc
}

func (f *fixture) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	_, err := f.database.Exec(f.ctx, sql, args...)
	require.NoError(t, err)
}

func (f *fixture) newAgent(t *testing.T, name string, console bool) uuid.UUID {
	t.Helper()
	kind := agent.KindWorker
	if console {
		kind = agent.KindConsole
	}
	created, err := f.agents.CreateKind(f.ctx, name, kind)
	require.NoError(t, err)
	return created.ID
}

// ticketIn files a ticket and puts it straight into status: what is under
// test is what a proposal makes of the status, not how it got there.
func (f *fixture) ticketIn(t *testing.T, status ticket.Status) int64 {
	t.Helper()
	created, err := f.tickets.Create(f.ctx, &ticket.CreateParams{Title: "a ticket", Status: ticket.StatusOpen})
	require.NoError(t, err)
	f.exec(t, `UPDATE tickets SET status = $2 WHERE id = $1`, created.ID, string(status))
	return created.ID
}

func (f *fixture) ticketStatus(t *testing.T, id int64) ticket.Status {
	t.Helper()
	got, err := f.tickets.Get(f.ctx, id)
	require.NoError(t, err)
	return got.Status
}

func (f *fixture) count(t *testing.T) int {
	t.Helper()
	var n int
	require.NoError(t, f.database.QueryRow(f.ctx, `SELECT count(*) FROM pending_actions`).Scan(&n))
	return n
}

func (f *fixture) pending(t *testing.T) []pendingaction.Action {
	t.Helper()
	got, err := f.svc.List(f.ctx, &pendingaction.ListFilter{States: []pendingaction.State{pendingaction.StatePending}})
	require.NoError(t, err)
	return got
}

func answer(agentID uuid.UUID, ticketID int64, body string) *pendingaction.ProposeParams {
	return &pendingaction.ProposeParams{
		AgentID: agentID, TicketID: ticketID, Kind: pendingaction.KindDiscussionAnswer,
		Payload: pendingaction.Payload{Body: body},
	}
}

func TestProposeStoresThePayloadAndTheTicketItWasProposedAgainst(t *testing.T) {
	f := newFixture(t, pendingaction.Config{})
	console := f.newAgent(t, "console", true)
	prURL, started := "https://github.com/o/r/pull/9", epoch.Add(-time.Hour)

	cases := []struct {
		name   string
		status ticket.Status
		params pendingaction.ProposeParams
		want   pendingaction.Payload
	}{
		{"answer", ticket.StatusDiscussion,
			pendingaction.ProposeParams{Kind: pendingaction.KindDiscussionAnswer, Payload: pendingaction.Payload{Body: "use B"}},
			pendingaction.Payload{Body: "use B"}},
		{"ready", ticket.StatusPreparing,
			pendingaction.ProposeParams{Kind: pendingaction.KindReady}, pendingaction.Payload{}},
		{"park", ticket.StatusOpen,
			pendingaction.ProposeParams{Kind: pendingaction.KindStatus, Payload: pendingaction.Payload{Status: ticket.StatusParked, Body: "later"}},
			pendingaction.Payload{Status: ticket.StatusParked, Body: "later"}},
		{"cancel", ticket.StatusPreparing,
			pendingaction.ProposeParams{Kind: pendingaction.KindStatus, Payload: pendingaction.Payload{Status: ticket.StatusCancelled, Body: "dup"}},
			pendingaction.Payload{Status: ticket.StatusCancelled, Body: "dup"}},
		{"approve review drops the body, which approving never applies", ticket.StatusReview,
			pendingaction.ProposeParams{Kind: pendingaction.KindReviewDecision, Payload: pendingaction.Payload{Decision: pendingaction.DecisionApprove, Body: "ignored"}},
			pendingaction.Payload{Decision: pendingaction.DecisionApprove}},
		{"request changes", ticket.StatusReview,
			pendingaction.ProposeParams{Kind: pendingaction.KindReviewDecision, Payload: pendingaction.Payload{Decision: pendingaction.DecisionRequestChanges, Body: "fix x"}},
			pendingaction.Payload{Decision: pendingaction.DecisionRequestChanges, Body: "fix x"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id := f.ticketIn(t, tc.status)
			if tc.status == ticket.StatusReview {
				f.exec(t, `UPDATE attempts SET pr_url = $2, review_started_at = $3 WHERE ticket_id = $1 AND number = 1`, id, prURL, started)
			}
			tc.params.AgentID, tc.params.TicketID = console, id

			got, err := f.svc.Propose(f.ctx, &tc.params)
			require.NoError(t, err)

			assert.Equal(t, tc.want, got.Payload)
			assert.Equal(t, pendingaction.StatePending, got.State)
			assert.Equal(t, console, got.AgentID)
			assert.True(t, epoch.Equal(got.CreatedAt), got.CreatedAt)
			assert.True(t, epoch.Add(pendingaction.DefaultTTL).Equal(got.ExpiresAt), got.ExpiresAt)
			assert.Equal(t, tc.status, got.Basis.Status)
			assert.Equal(t, 0, got.Basis.Revision)
			if tc.status == ticket.StatusReview {
				assert.Equal(t, &prURL, got.Basis.PRURL)
				require.NotNil(t, got.Basis.ReviewStartedAt)
				assert.True(t, started.Equal(*got.Basis.ReviewStartedAt))
			}
			assert.Equal(t, tc.status, f.ticketStatus(t, id), "proposing changes nothing on the ticket")

			read, err := f.svc.Get(f.ctx, got.ID)
			require.NoError(t, err)
			assert.Equal(t, got.Payload, read.Payload)
		})
	}
}

func TestOnlyAnEnabledConsoleMayPropose(t *testing.T) {
	f := newFixture(t, pendingaction.Config{})
	worker := f.newAgent(t, "worker", false)
	disabled := f.newAgent(t, "old-console", true)
	f.exec(t, `UPDATE agents SET disabled_at = now() WHERE id = $1`, disabled)
	id := f.ticketIn(t, ticket.StatusDiscussion)

	for name, agentID := range map[string]uuid.UUID{"worker": worker, "disabled console": disabled, "unknown agent": uuid.New()} {
		t.Run(name, func(t *testing.T) {
			_, err := f.svc.Propose(f.ctx, answer(agentID, id, "yes"))
			assert.ErrorIs(t, err, pendingaction.ErrNotConsole)
			assert.Zero(t, f.count(t), "a refused proposal leaves nothing behind")
		})
	}
}

func TestAgentsAreNotConsolesByDefault(t *testing.T) {
	f := newFixture(t, pendingaction.Config{})
	id := f.newAgent(t, "fresh", false)
	var kind string
	require.NoError(t, f.database.QueryRow(f.ctx, `SELECT kind FROM agents WHERE id = $1`, id).Scan(&kind))
	assert.Equal(t, "worker", kind)
}

func TestProposalsAreRefusedWhereAPersonCouldNotDoTheAction(t *testing.T) {
	f := newFixture(t, pendingaction.Config{})
	console := f.newAgent(t, "console", true)

	status := func(s ticket.Status, body string) pendingaction.Payload {
		return pendingaction.Payload{Status: s, Body: body}
	}
	cases := []struct {
		name         string
		ticketStatus ticket.Status
		params       pendingaction.ProposeParams
	}{
		{"answer outside discussion", ticket.StatusOpen,
			pendingaction.ProposeParams{Kind: pendingaction.KindDiscussionAnswer, Payload: pendingaction.Payload{Body: "x"}}},
		{"blank answer", ticket.StatusDiscussion,
			pendingaction.ProposeParams{Kind: pendingaction.KindDiscussionAnswer, Payload: pendingaction.Payload{Body: " \n"}}},
		{"oversized answer", ticket.StatusDiscussion,
			pendingaction.ProposeParams{Kind: pendingaction.KindDiscussionAnswer, Payload: pendingaction.Payload{Body: strings.Repeat("a", pendingaction.MaxTextBytes+1)}}},
		{"answer with a status", ticket.StatusDiscussion,
			pendingaction.ProposeParams{Kind: pendingaction.KindDiscussionAnswer, Payload: pendingaction.Payload{Body: "x", Status: ticket.StatusParked}}},
		{"ready outside preparing", ticket.StatusOpen, pendingaction.ProposeParams{Kind: pendingaction.KindReady}},
		{"ready with a payload", ticket.StatusPreparing,
			pendingaction.ProposeParams{Kind: pendingaction.KindReady, Payload: pendingaction.Payload{Body: "x"}}},
		{"park a ticket that is blocked", ticket.StatusBlocked,
			pendingaction.ProposeParams{Kind: pendingaction.KindStatus, Payload: status(ticket.StatusParked, "x")}},
		{"park a discussion", ticket.StatusDiscussion,
			pendingaction.ProposeParams{Kind: pendingaction.KindStatus, Payload: status(ticket.StatusParked, "x")}},
		{"cancel a done ticket", ticket.StatusDone,
			pendingaction.ProposeParams{Kind: pendingaction.KindStatus, Payload: status(ticket.StatusCancelled, "x")}},
		{"park a parked ticket", ticket.StatusParked,
			pendingaction.ProposeParams{Kind: pendingaction.KindStatus, Payload: status(ticket.StatusParked, "x")}},
		{"status to open", ticket.StatusPreparing,
			pendingaction.ProposeParams{Kind: pendingaction.KindStatus, Payload: status(ticket.StatusOpen, "x")}},
		{"status without a reason", ticket.StatusOpen,
			pendingaction.ProposeParams{Kind: pendingaction.KindStatus, Payload: status(ticket.StatusParked, "")}},
		{"review decision outside review", ticket.StatusOpen,
			pendingaction.ProposeParams{Kind: pendingaction.KindReviewDecision, Payload: pendingaction.Payload{Decision: pendingaction.DecisionApprove}}},
		{"request changes without text", ticket.StatusReview,
			pendingaction.ProposeParams{Kind: pendingaction.KindReviewDecision, Payload: pendingaction.Payload{Decision: pendingaction.DecisionRequestChanges}}},
		{"unknown decision", ticket.StatusReview,
			pendingaction.ProposeParams{Kind: pendingaction.KindReviewDecision, Payload: pendingaction.Payload{Decision: "merge"}}},
		{"unknown kind", ticket.StatusOpen, pendingaction.ProposeParams{Kind: "delete"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.params.AgentID, tc.params.TicketID = console, f.ticketIn(t, tc.ticketStatus)
			_, err := f.svc.Propose(f.ctx, &tc.params)
			assert.ErrorIs(t, err, pendingaction.ErrInvalid)
			assert.Zero(t, f.count(t))
		})
	}
}

func TestStatusProposalsFollowTheHumanMoveRule(t *testing.T) {
	f := newFixture(t, pendingaction.Config{})
	console := f.newAgent(t, "console", true)
	id := f.ticketIn(t, ticket.StatusBlocked)

	_, err := f.svc.Propose(f.ctx, &pendingaction.ProposeParams{
		AgentID: console, TicketID: id, Kind: pendingaction.KindStatus,
		Payload: pendingaction.Payload{Status: ticket.StatusParked, Body: "x"},
	})
	assert.ErrorIs(t, err, ticket.ErrNotParkable, "the board's own refusal, not a second copy of the rule")
}

func TestNoProposalOnATicketAnAgentHolds(t *testing.T) {
	f := newFixture(t, pendingaction.Config{})
	console := f.newAgent(t, "console", true)
	worker := f.newAgent(t, "worker", false)
	created, err := f.tickets.Create(f.ctx, &ticket.CreateParams{Title: "held", Status: ticket.StatusOpen})
	require.NoError(t, err)
	claimed, err := f.tickets.Claim(f.ctx, worker, nil)
	require.NoError(t, err)
	require.NotNil(t, claimed.Ticket)
	require.Equal(t, created.ID, claimed.Ticket.ID)

	_, err = f.svc.Propose(f.ctx, &pendingaction.ProposeParams{
		AgentID: console, TicketID: created.ID, Kind: pendingaction.KindStatus,
		Payload: pendingaction.Payload{Status: ticket.StatusCancelled, Body: "x"},
	})
	assert.ErrorIs(t, err, pendingaction.ErrTicketHeld)
	assert.Zero(t, f.count(t))

	_, err = f.tickets.Release(f.ctx, worker, "done for now", nil)
	require.NoError(t, err)
	_, err = f.svc.Propose(f.ctx, &pendingaction.ProposeParams{
		AgentID: console, TicketID: created.ID, Kind: pendingaction.KindStatus,
		Payload: pendingaction.Payload{Status: ticket.StatusCancelled, Body: "x"},
	})
	assert.NoError(t, err, "released, the ticket is open to a proposal again")
}

func TestProposingAgainSupersedesThePendingProposalOfTheSameKind(t *testing.T) {
	f := newFixture(t, pendingaction.Config{})
	console := f.newAgent(t, "console", true)
	other := f.newAgent(t, "console-2", true)
	id := f.ticketIn(t, ticket.StatusDiscussion)

	first, err := f.svc.Propose(f.ctx, answer(console, id, "draft 1"))
	require.NoError(t, err)
	second, err := f.svc.Propose(f.ctx, answer(console, id, "draft 2"))
	require.NoError(t, err)
	third, err := f.svc.Propose(f.ctx, answer(other, id, "draft 3"))
	require.NoError(t, err, "another console's proposal supersedes too: the slot is the ticket's")

	live := f.pending(t)
	require.Len(t, live, 1)
	assert.Equal(t, third.ID, live[0].ID)

	for _, id := range []uuid.UUID{first.ID, second.ID} {
		got, err := f.svc.Get(f.ctx, id)
		require.NoError(t, err)
		assert.Equal(t, pendingaction.StateWithdrawn, got.State)
		assert.Equal(t, "superseded by a newer proposal", got.Outcome)
		assert.NotNil(t, got.DecidedAt)
	}
}

func TestADifferentKindOrTicketIsNotSuperseded(t *testing.T) {
	f := newFixture(t, pendingaction.Config{})
	console := f.newAgent(t, "console", true)
	one := f.ticketIn(t, ticket.StatusDiscussion)
	two := f.ticketIn(t, ticket.StatusDiscussion)

	_, err := f.svc.Propose(f.ctx, answer(console, one, "a"))
	require.NoError(t, err)
	_, err = f.svc.Propose(f.ctx, answer(console, two, "b"))
	require.NoError(t, err)
	_, err = f.svc.Propose(f.ctx, &pendingaction.ProposeParams{
		AgentID: console, TicketID: one, Kind: pendingaction.KindStatus,
		Payload: pendingaction.Payload{Status: ticket.StatusCancelled, Body: "x"},
	})
	require.NoError(t, err)
	assert.Len(t, f.pending(t), 3, "each ticket's answer and the other kind stand side by side")
}

func TestAConsoleCannotFillThePersonsQueue(t *testing.T) {
	f := newFixture(t, pendingaction.Config{MaxPendingPerAgent: 2, RatePerMinute: 1000})
	console := f.newAgent(t, "console", true)
	other := f.newAgent(t, "console-2", true)
	one, two, three := f.ticketIn(t, ticket.StatusDiscussion), f.ticketIn(t, ticket.StatusDiscussion), f.ticketIn(t, ticket.StatusDiscussion)

	_, err := f.svc.Propose(f.ctx, answer(console, one, "a"))
	require.NoError(t, err)
	_, err = f.svc.Propose(f.ctx, answer(console, two, "b"))
	require.NoError(t, err)

	_, err = f.svc.Propose(f.ctx, answer(console, three, "c"))
	assert.ErrorIs(t, err, pendingaction.ErrTooManyPending)
	assert.Len(t, f.pending(t), 2, "the refused proposal leaves the others as they were")

	_, err = f.svc.Propose(f.ctx, answer(console, one, "a again"))
	assert.NoError(t, err, "replacing a proposal does not count against the cap")

	_, err = f.svc.Propose(f.ctx, answer(other, three, "c"))
	assert.NoError(t, err, "the cap is per agent")

	f.clock.Advance(pendingaction.DefaultTTL + time.Second)
	_, err = f.svc.Propose(f.ctx, answer(console, three, "c"))
	assert.NoError(t, err, "expired proposals no longer count")
}

func TestARefusedProposalDoesNotSupersede(t *testing.T) {
	f := newFixture(t, pendingaction.Config{MaxPendingPerAgent: 1, RatePerMinute: 1000})
	console := f.newAgent(t, "console", true)
	id := f.ticketIn(t, ticket.StatusDiscussion)
	first, err := f.svc.Propose(f.ctx, answer(console, id, "keep me"))
	require.NoError(t, err)

	_, err = f.svc.Propose(f.ctx, answer(console, id, " "))
	require.ErrorIs(t, err, pendingaction.ErrInvalid)

	live := f.pending(t)
	require.Len(t, live, 1)
	assert.Equal(t, first.ID, live[0].ID)
}

func TestProposalsAreRateLimitedPerAgent(t *testing.T) {
	f := newFixture(t, pendingaction.Config{RatePerMinute: 2, MaxPendingPerAgent: 1000})
	console := f.newAgent(t, "console", true)
	id := f.ticketIn(t, ticket.StatusDiscussion)

	for range 2 {
		_, err := f.svc.Propose(f.ctx, answer(console, id, "again"))
		require.NoError(t, err)
	}
	_, err := f.svc.Propose(f.ctx, answer(console, id, "again"))
	assert.ErrorIs(t, err, pendingaction.ErrRateLimited)

	f.clock.Advance(61 * time.Second)
	_, err = f.svc.Propose(f.ctx, answer(console, id, "again"))
	assert.NoError(t, err)
}

func TestProposalsExpire(t *testing.T) {
	f := newFixture(t, pendingaction.Config{TTL: time.Hour})
	console := f.newAgent(t, "console", true)
	id := f.ticketIn(t, ticket.StatusDiscussion)
	made, err := f.svc.Propose(f.ctx, answer(console, id, "x"))
	require.NoError(t, err)
	assert.True(t, epoch.Add(time.Hour).Equal(made.ExpiresAt), made.ExpiresAt)

	f.clock.Advance(time.Hour - time.Second)
	got, err := f.svc.Get(f.ctx, made.ID)
	require.NoError(t, err)
	assert.Equal(t, pendingaction.StatePending, got.State)

	f.clock.Advance(time.Second)
	got, err = f.svc.Get(f.ctx, made.ID)
	require.NoError(t, err)
	assert.Equal(t, pendingaction.StateExpired, got.State, "reads see the expiry before any sweep has recorded it")
	assert.Empty(t, f.pending(t))
	expired, err := f.svc.List(f.ctx, &pendingaction.ListFilter{States: []pendingaction.State{pendingaction.StateExpired}})
	require.NoError(t, err)
	assert.Len(t, expired, 1)

	_, err = f.svc.Withdraw(f.ctx, console, made.ID)
	assert.ErrorIs(t, err, pendingaction.ErrNotPending)

	n, err := f.svc.ExpireDue(f.ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(1), n)
	n, err = f.svc.ExpireDue(f.ctx)
	require.NoError(t, err)
	assert.Zero(t, n, "recorded once")
	var state string
	require.NoError(t, f.database.QueryRow(f.ctx, `SELECT status FROM pending_actions WHERE id = $1`, made.ID).Scan(&state))
	assert.Equal(t, "expired", state)
}

func TestAnExpiredProposalDoesNotBlockProposingAgain(t *testing.T) {
	f := newFixture(t, pendingaction.Config{TTL: time.Hour})
	console := f.newAgent(t, "console", true)
	id := f.ticketIn(t, ticket.StatusDiscussion)
	old, err := f.svc.Propose(f.ctx, answer(console, id, "old"))
	require.NoError(t, err)

	f.clock.Advance(2 * time.Hour)
	_, err = f.svc.Propose(f.ctx, answer(console, id, "new"))
	require.NoError(t, err, "no sweep has run, and the dead proposal must not hold the ticket's slot")

	got, err := f.svc.Get(f.ctx, old.ID)
	require.NoError(t, err)
	assert.Equal(t, pendingaction.StateExpired, got.State, "closed as expired, not as superseded")
}

func TestOnlyTheProposerWithdraws(t *testing.T) {
	f := newFixture(t, pendingaction.Config{})
	console := f.newAgent(t, "console", true)
	other := f.newAgent(t, "console-2", true)
	id := f.ticketIn(t, ticket.StatusDiscussion)
	made, err := f.svc.Propose(f.ctx, answer(console, id, "x"))
	require.NoError(t, err)

	_, err = f.svc.Withdraw(f.ctx, other, made.ID)
	assert.ErrorIs(t, err, pendingaction.ErrNotFound, "another agent's proposal is none of its business")
	assert.Len(t, f.pending(t), 1)

	got, err := f.svc.Withdraw(f.ctx, console, made.ID)
	require.NoError(t, err)
	assert.Equal(t, pendingaction.StateWithdrawn, got.State)
	assert.Empty(t, f.pending(t))

	_, err = f.svc.Withdraw(f.ctx, console, made.ID)
	assert.ErrorIs(t, err, pendingaction.ErrNotPending)
	_, err = f.svc.Withdraw(f.ctx, console, uuid.New())
	assert.ErrorIs(t, err, pendingaction.ErrNotFound)
}

func TestVoidingAnAgentEndsItsPendingProposalsOnly(t *testing.T) {
	f := newFixture(t, pendingaction.Config{})
	console := f.newAgent(t, "console", true)
	other := f.newAgent(t, "console-2", true)
	one, two, three := f.ticketIn(t, ticket.StatusDiscussion), f.ticketIn(t, ticket.StatusDiscussion), f.ticketIn(t, ticket.StatusDiscussion)
	a, err := f.svc.Propose(f.ctx, answer(console, one, "a"))
	require.NoError(t, err)
	b, err := f.svc.Propose(f.ctx, answer(console, two, "b"))
	require.NoError(t, err)
	kept, err := f.svc.Propose(f.ctx, answer(other, three, "c"))
	require.NoError(t, err)
	_, err = f.svc.Withdraw(f.ctx, console, b.ID)
	require.NoError(t, err)

	n, err := f.svc.VoidAgent(f.ctx, console, "console turned off")
	require.NoError(t, err)
	assert.Equal(t, int64(1), n, "only the one still pending")

	got, err := f.svc.Get(f.ctx, a.ID)
	require.NoError(t, err)
	assert.Equal(t, pendingaction.StateVoided, got.State)
	assert.Equal(t, "console turned off", got.Outcome)
	got, err = f.svc.Get(f.ctx, b.ID)
	require.NoError(t, err)
	assert.Equal(t, pendingaction.StateWithdrawn, got.State, "a decided proposal keeps its outcome")
	live := f.pending(t)
	require.Len(t, live, 1)
	assert.Equal(t, kept.ID, live[0].ID)
}

func TestListFiltersAndOrdersNewestFirst(t *testing.T) {
	f := newFixture(t, pendingaction.Config{})
	console := f.newAgent(t, "console", true)
	other := f.newAgent(t, "console-2", true)
	one, two := f.ticketIn(t, ticket.StatusDiscussion), f.ticketIn(t, ticket.StatusDiscussion)
	a, err := f.svc.Propose(f.ctx, answer(console, one, "a"))
	require.NoError(t, err)
	f.clock.Advance(time.Second)
	b, err := f.svc.Propose(f.ctx, answer(other, two, "b"))
	require.NoError(t, err)

	ids := func(filter *pendingaction.ListFilter) []uuid.UUID {
		got, err := f.svc.List(f.ctx, filter)
		require.NoError(t, err)
		var out []uuid.UUID
		for _, x := range got {
			out = append(out, x.ID)
		}
		return out
	}
	assert.Equal(t, []uuid.UUID{b.ID, a.ID}, ids(nil))
	assert.Equal(t, []uuid.UUID{a.ID}, ids(&pendingaction.ListFilter{AgentID: &console}))
	assert.Equal(t, []uuid.UUID{b.ID}, ids(&pendingaction.ListFilter{TicketID: &two}))
	assert.Equal(t, []uuid.UUID{b.ID}, ids(&pendingaction.ListFilter{Limit: 1}))
	assert.Empty(t, ids(&pendingaction.ListFilter{States: []pendingaction.State{pendingaction.StateApproved}}))

	_, err = f.svc.List(f.ctx, &pendingaction.ListFilter{States: []pendingaction.State{"bogus"}})
	assert.ErrorIs(t, err, pendingaction.ErrInvalid)
}

// listedForTickets is the ids of the pending proposals about tickets the
// filter matches.
func listedForTickets(t *testing.T, f *fixture, ctx context.Context, tickets *ticket.ListFilter) []uuid.UUID {
	t.Helper()
	got, err := f.svc.List(ctx, &pendingaction.ListFilter{Ticket: tickets})
	require.NoError(t, err)
	out := []uuid.UUID{}
	for _, x := range got {
		out = append(out, x.ID)
	}
	return out
}

func TestListFiltersProposalsByTheirTicketLikeTheTicketLists(t *testing.T) {
	f := newFixture(t, pendingaction.Config{})
	console := f.newAgent(t, "console", true)
	propose := func(title string, priority model.Priority, status ticket.Status) (uuid.UUID, int64) {
		created, err := f.tickets.Create(f.ctx, &ticket.CreateParams{Title: title, Priority: priority, Status: ticket.StatusOpen})
		require.NoError(t, err)
		f.exec(t, `UPDATE tickets SET status = $2 WHERE id = $1`, created.ID, string(status))
		p, err := f.svc.Propose(f.ctx, answer(console, created.ID, "a"))
		require.NoError(t, err)
		return p.ID, created.ID
	}
	// A discussion answer is proposable only on a discussion ticket, so the
	// parked one is moved after its proposal is made.
	live, _ := propose("fix the zebra crossing", model.PriorityHigh, ticket.StatusDiscussion)
	quiet, _ := propose("tidy the garden", model.PriorityLow, ticket.StatusDiscussion)
	parked, parkedTicket := propose("parked zebra", model.PriorityNormal, ticket.StatusDiscussion)
	f.exec(t, `UPDATE tickets SET status = 'parked' WHERE id = $1`, parkedTicket)

	assert.ElementsMatch(t, []uuid.UUID{live, quiet, parked}, listedForTickets(t, f, f.ctx, &ticket.ListFilter{}), "no filter is every proposal")

	assert.Equal(t, []uuid.UUID{live}, listedForTickets(t, f, f.ctx,
		&ticket.ListFilter{Priorities: []model.Priority{model.PriorityHigh}}), "priority")
	assert.ElementsMatch(t, []uuid.UUID{live, parked}, listedForTickets(t, f, f.ctx,
		&ticket.ListFilter{Text: "zebra"}), "text")
	assert.Equal(t, []uuid.UUID{parked}, listedForTickets(t, f, f.ctx,
		&ticket.ListFilter{Statuses: []ticket.Status{ticket.StatusParked}}), "the ticket's status now, not the one proposed against")
	// The Approvals page's default: everything but parked, cancelled and done.
	assert.ElementsMatch(t, []uuid.UUID{live, quiet}, listedForTickets(t, f, f.ctx, &ticket.ListFilter{Statuses: []ticket.Status{
		ticket.StatusPreparing, ticket.StatusOpen, ticket.StatusBlocked, ticket.StatusInProgress, ticket.StatusDiscussion, ticket.StatusReview,
	}}), "default exclusion")

	// Tickets are stamped by the database, not by the fixture clock.
	tomorrow := time.Now().Add(24 * time.Hour)
	assert.Empty(t, listedForTickets(t, f, f.ctx, &ticket.ListFilter{Created: ticket.TimeRange{From: &tomorrow}}), "date range")
	assert.Len(t, listedForTickets(t, f, f.ctx, &ticket.ListFilter{Created: ticket.TimeRange{Before: &tomorrow}}), 3)
}

func TestListByTicketFilterNeverReachesAnotherTenant(t *testing.T) {
	f := newFixture(t, pendingaction.Config{})
	console := f.newAgent(t, "console", true)
	id := f.ticketIn(t, ticket.StatusDiscussion)
	_, err := f.svc.Propose(f.ctx, answer(console, id, "secret plan"))
	require.NoError(t, err)

	otherCtx := db.WithTenant(context.Background(), dbtest.NewTenant(t, f.database))
	assert.Empty(t, listedForTickets(t, f, otherCtx, &ticket.ListFilter{Text: "a ticket"}))
}

func TestListByIDsNarrowsToThoseProposals(t *testing.T) {
	f := newFixture(t, pendingaction.Config{})
	console := f.newAgent(t, "console", true)
	a, err := f.svc.Propose(f.ctx, answer(console, f.ticketIn(t, ticket.StatusDiscussion), "a"))
	require.NoError(t, err)
	_, err = f.svc.Propose(f.ctx, answer(console, f.ticketIn(t, ticket.StatusDiscussion), "b"))
	require.NoError(t, err)

	got, err := f.svc.List(f.ctx, &pendingaction.ListFilter{IDs: []uuid.UUID{a.ID}})
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, a.ID, got[0].ID)
	got, err = f.svc.List(f.ctx, &pendingaction.ListFilter{IDs: []uuid.UUID{}})
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestProposalsAreTenantScoped(t *testing.T) {
	f := newFixture(t, pendingaction.Config{})
	console := f.newAgent(t, "console", true)
	id := f.ticketIn(t, ticket.StatusDiscussion)
	made, err := f.svc.Propose(f.ctx, answer(console, id, "secret plan"))
	require.NoError(t, err)

	otherTenant := dbtest.NewTenant(t, f.database)
	otherCtx := db.WithTenant(context.Background(), otherTenant)

	_, err = f.svc.Get(otherCtx, made.ID)
	assert.ErrorIs(t, err, pendingaction.ErrNotFound)
	listed, err := f.svc.List(otherCtx, nil)
	require.NoError(t, err)
	assert.Empty(t, listed)
	_, err = f.svc.Withdraw(otherCtx, console, made.ID)
	assert.ErrorIs(t, err, pendingaction.ErrNotFound)
	n, err := f.svc.VoidAgent(otherCtx, console, "x")
	require.NoError(t, err)
	assert.Zero(t, n)

	var seen int
	require.NoError(t, f.database.QueryRow(otherCtx, `SELECT count(*) FROM pending_actions`).Scan(&seen))
	assert.Zero(t, seen, "the database itself hides the row, not just the service")

	// Another tenant's console naming this tenant's ticket finds no ticket,
	// and its own agent row is not this tenant's console either.
	intruder, err := f.agents.CreateKind(otherCtx, "console", agent.KindConsole)
	require.NoError(t, err)
	_, err = f.svc.Propose(otherCtx, answer(intruder.ID, id, "hijack"))
	assert.ErrorIs(t, err, pendingaction.ErrTicketNotFound)
	_, err = f.svc.Propose(otherCtx, answer(console, id, "hijack"))
	assert.ErrorIs(t, err, pendingaction.ErrNotConsole, "another tenant's agent is not a console here")
	assert.Equal(t, 1, f.count(t))
}

func TestTheTenantForeignKeysRefuseAnotherTenantsTicket(t *testing.T) {
	f := newFixture(t, pendingaction.Config{})
	console := f.newAgent(t, "console", true)
	other := dbtest.NewTenant(t, f.database)
	otherCtx := db.WithTenant(context.Background(), other)
	foreign, err := f.tickets.Create(otherCtx, &ticket.CreateParams{Title: "theirs"})
	require.NoError(t, err)

	_, err = f.database.Exec(f.ctx, `
		INSERT INTO pending_actions (id, ticket_id, agent_id, kind, payload, base_status, base_revision, created_at, expires_at)
		VALUES ($1, $2, $3, 'ready', '{}', 'preparing', 0, now(), now() + interval '1 hour')`,
		uuid.New(), foreign.ID, console)
	assert.Error(t, err, "a row naming another tenant's ticket cannot even be inserted")
}

func TestConcurrentProposalsLeaveOnePendingPerTicketAndKind(t *testing.T) {
	f := newFixture(t, pendingaction.Config{RatePerMinute: 1000})
	const proposers = 6
	agents := make([]uuid.UUID, proposers)
	for i := range agents {
		agents[i] = f.newAgent(t, "console-"+string(rune('a'+i)), true)
	}
	id := f.ticketIn(t, ticket.StatusDiscussion)

	start := make(chan struct{})
	errs := make(chan error, proposers)
	var wg sync.WaitGroup
	for _, a := range agents {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := f.svc.Propose(f.ctx, answer(a, id, "mine"))
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)

	for err := range errs {
		assert.NoError(t, err, "each proposer supersedes the last; none may meet the unique index")
	}
	assert.Len(t, f.pending(t), 1)
	assert.Equal(t, proposers, f.count(t))
}

// A worker handing its ticket to review releases its lease, so the ticket
// is proposable from the moment a person is asked to look at it; and the
// basis records the PR the proposal was made against.
func TestAReviewHandedOverByAWorkerIsProposable(t *testing.T) {
	f := newFixture(t, pendingaction.Config{})
	console := f.newAgent(t, "console", true)
	worker := f.newAgent(t, "worker", false)
	_, err := f.tickets.Create(f.ctx, &ticket.CreateParams{Title: "built", Status: ticket.StatusOpen})
	require.NoError(t, err)
	claimed, err := f.tickets.Claim(f.ctx, worker, nil)
	require.NoError(t, err)
	_, err = f.tickets.Review(f.ctx, worker, &ticket.ReviewParams{Reason: "reviewed by a person", PRURL: "https://github.com/o/r/pull/3", Handoff: "ready"})
	require.NoError(t, err)

	got, err := f.svc.Propose(f.ctx, &pendingaction.ProposeParams{
		AgentID: console, TicketID: claimed.Ticket.ID, Kind: pendingaction.KindReviewDecision,
		Payload: pendingaction.Payload{Decision: pendingaction.DecisionApprove},
	})
	require.NoError(t, err)
	require.NotNil(t, got.Basis.PRURL)
	assert.Equal(t, "https://github.com/o/r/pull/3", *got.Basis.PRURL)
	assert.NotNil(t, got.Basis.ReviewStartedAt)
}

func TestARateLimitedReplacementLeavesTheExistingProposal(t *testing.T) {
	f := newFixture(t, pendingaction.Config{RatePerMinute: 1})
	console := f.newAgent(t, "console", true)
	id := f.ticketIn(t, ticket.StatusDiscussion)
	first, err := f.svc.Propose(f.ctx, answer(console, id, "keep me"))
	require.NoError(t, err)

	_, err = f.svc.Propose(f.ctx, answer(console, id, "too soon"))
	require.ErrorIs(t, err, pendingaction.ErrRateLimited)

	live := f.pending(t)
	require.Len(t, live, 1, "the refusal rolls the supersede back")
	assert.Equal(t, first.ID, live[0].ID)
}

func TestVoidingLeavesAnExpiredProposalExpired(t *testing.T) {
	f := newFixture(t, pendingaction.Config{TTL: time.Hour})
	console := f.newAgent(t, "console", true)
	id := f.ticketIn(t, ticket.StatusDiscussion)
	made, err := f.svc.Propose(f.ctx, answer(console, id, "x"))
	require.NoError(t, err)

	f.clock.Advance(2 * time.Hour)
	n, err := f.svc.VoidAgent(f.ctx, console, "console turned off")
	require.NoError(t, err)
	assert.Zero(t, n)
	got, err := f.svc.Get(f.ctx, made.ID)
	require.NoError(t, err)
	assert.Equal(t, pendingaction.StateExpired, got.State)
}

// A lease past its expiry that nothing has swept yet is not a hold: the
// ticket system treats the ticket as free, and so must a proposal.
func TestAnExpiredUnsweptLeaseDoesNotHoldTheTicket(t *testing.T) {
	f := newFixture(t, pendingaction.Config{})
	console := f.newAgent(t, "console", true)
	worker := f.newAgent(t, "worker", false)
	created, err := f.tickets.Create(f.ctx, &ticket.CreateParams{Title: "stale lease", Status: ticket.StatusOpen})
	require.NoError(t, err)
	_, err = f.tickets.Claim(f.ctx, worker, nil)
	require.NoError(t, err)
	f.exec(t, `UPDATE leases SET expires_at = now() - interval '1 minute'`)
	f.exec(t, `UPDATE tickets SET status = 'open' WHERE id = $1`, created.ID)

	_, err = f.svc.Propose(f.ctx, &pendingaction.ProposeParams{
		AgentID: console, TicketID: created.ID, Kind: pendingaction.KindStatus,
		Payload: pendingaction.Payload{Status: ticket.StatusParked, Body: "later"},
	})
	assert.NoError(t, err)
}
