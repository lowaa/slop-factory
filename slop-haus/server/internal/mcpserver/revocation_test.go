package mcpserver_test

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-auth/ratelimit"
	usagelib "github.com/lowaa/slop-factory/go-llm-usage"
	"github.com/lowaa/slop-factory/go-llm-usage/pricing"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/agent"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/agentmsg"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit/audittest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/llmusage"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/manifest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/mcpserver"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/pendingaction"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/reviewreport"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/settings"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/steward"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/steward/assist"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/steward/discussionlabel"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/stewardwire"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// These follow a person taking an agent's ticket away through the real
// ticket store and the real MCP transport. Against fakes, the once-only
// delivery and the lease actually being gone would each be the fake's
// say-so.

// tenantIdentity is identity with a real tenant, so the tools reach rows
// the test created through the real store's row-level security.
type tenantIdentity struct {
	id     uuid.UUID
	tenant uuid.UUID
}

func (i tenantIdentity) AgentID() uuid.UUID  { return i.id }
func (i tenantIdentity) AgentName() string   { return "builder-1" }
func (i tenantIdentity) TenantID() uuid.UUID { return i.tenant }
func (i tenantIdentity) ReadOnly() bool      { return false }

const revokedNotice = "moved to Preparing by a human -- stop working on it"

type realRig struct {
	t       *testing.T
	ctx     context.Context
	tickets *ticket.Service
	agentID uuid.UUID
	// human is a real global_users row, for a person's edit to be
	// attributed to.
	human   uuid.UUID
	session *mcp.ClientSession
	// roles is the real role service, for tickets that require roles.
	roles    *agent.RoleService
	events   *audittest.Counter
	database *db.DB
	tenantID uuid.UUID
	agents   *agent.Service
	owners   *lateOwners
	pending  *pendingaction.Service
	usage    *llmusage.Service
	messages *agentmsg.Service
	// url is the MCP endpoint, and tokens maps a bearer token to the agent
	// it signs in as.
	url    string
	tokens *tokenTable
	// labels queues discussion rounds for the steward to label (set with a steward).
	labels *discussionlabel.Service
	rounds *fixedRounds
}

func newRealRig(t *testing.T) *realRig {
	t.Helper()
	return newRealRigIn(t, dbtest.WithDB(t))
}

// newRealRigIn is a rig in a tenant of its own in database, so two of them
// can share one database as two tenants.
func newRealRigIn(t *testing.T, database *db.DB) *realRig {
	t.Helper()
	return newRealRigWith(t, database, &realRigOptions{})
}

// realRigOptions switches on what a real rig leaves off by default.
type realRigOptions struct {
	// changes is the handler's Changes, which switches the change tools on
	// (none if nil).
	changes mcpserver.Changes
	// manifests switches workspace_get on, reading what the real store holds.
	manifests mcpserver.Manifests
	// batches switches landing_batches on, reading what the real store holds.
	batches mcpserver.Batches
	// steward switches steward_assess on, under these settings.
	steward *steward.Config
	// discussions switches on the discussion label in ticket_get.
	discussions ticket.AttemptDiscussions
	// labelToWorkers shows that label to workers too.
	labelToWorkers bool
	// attachments switches the attachment tools on.
	attachments mcpserver.Attachments
	// reviews switches the slop-review tools on, over the real report store.
	reviews bool
}

func newRealRigWith(t *testing.T, database *db.DB, opts *realRigOptions) *realRig {
	t.Helper()
	tenantID := dbtest.NewTenant(t, database)
	ctx := db.WithTenant(context.Background(), tenantID)
	owners := &lateOwners{}
	tickets := ticket.NewService(ticket.NewPostgresStore(database), slog.DiscardHandler, ticket.WithOwnerResolver(owners))
	pending := pendingaction.NewService(pendingaction.NewPostgresStore(database), pendingaction.Config{}, slog.DiscardHandler,
		pendingaction.WithApproval(tickets, nil), pendingaction.WithManifests(manifest.NewEditor(manifest.NewPostgresStore(database))))
	agents := agent.NewService(agent.NewPostgresStore(database), tickets, nil, slog.DiscardHandler, agent.WithProposalVoider(pending))
	builder, err := agents.Create(ctx, "builder-1")
	require.NoError(t, err)

	tokens := &tokenTable{byToken: map[string]uuid.UUID{goodToken: builder.ID}}
	messages := agentmsg.NewService(agentmsg.NewPostgresStore(database), agentmsg.Config{}, slog.DiscardHandler)
	sessions := &fakeSessions{}
	prices := pricing.Default()
	collector, err := usagelib.NewCollector(&usagelib.CollectorOptions{Models: prices})
	require.NoError(t, err)
	usage, err := llmusage.NewService(&llmusage.Options{
		Store: llmusage.NewPostgresStore(database, prices), Collector: collector, Pricer: prices,
		Limit: &ratelimit.Config{Name: "llm_usage", Limit: 100, Window: time.Hour, MaxKeys: 100},
	}, slog.DiscardHandler)
	require.NoError(t, err)

	events, counter := audittest.NewRecorder()
	var stewards mcpserver.Stewards
	var assessor mcpserver.Assessor
	var drafter mcpserver.Drafter
	var drafts pendingaction.Drafts
	var labeller mcpserver.DiscussionLabeller
	var labels *discussionlabel.Service
	rounds := &fixedRounds{tickets: tickets, head: "head-1"}
	if opts.steward != nil {
		stewards = agents
		deps := &stewardwire.Deps{Database: database, Logger: slog.DiscardHandler, Pending: pending, Tickets: tickets,
			Agents: agents, Hosts: codehost.NewRegistry(), Settings: fixedStewardConfig{opts.steward}, Store: steward.NewStore(database)}
		assessor = stewardwire.NewService(deps)
		store := assist.NewStore(database)
		drafter, drafts = stewardwire.NewAssist(deps, store), draftsOf{store}
		labels = discussionlabel.NewService(slog.DiscardHandler, nil, stewardwire.NewDiscussionAssessor(deps), fixedStewardConfig{opts.steward},
			discussionlabel.NewStore(database), nil)
		labeller = stewardwire.NewDiscussionSubmitter(rounds, labels)
	}
	var reviews mcpserver.Reviews
	if opts.reviews {
		svc, err := reviewreport.NewService(&reviewreport.Options{
			Store: reviewreport.NewPostgresStore(database), Settings: noSettingsHistory{},
			Limit: &ratelimit.Config{Name: "review_report", Limit: 100, Window: time.Hour, MaxKeys: 100},
		}, slog.DiscardHandler)
		require.NoError(t, err)
		reviews = svc
	}
	handler := mcpserver.NewHTTPHandler(mcpserver.Deps{
		Reviews:  reviews,
		Stewards: stewards, Assessor: assessor, Drafter: drafter, Drafts: drafts, Labeller: labeller,
		Tickets: tickets, Messages: messages, Sessions: sessions, Agents: fakeAgents{}, Roles: &fakeRoles{}, Events: events,
		Proposers: agents, Proposals: pending, Usage: usage, BoardURL: testBoardURL, Changes: opts.changes, Manifests: opts.manifests, Batches: opts.batches,
		Discussions: opts.discussions, DiscussionLabelToWorkers: opts.labelToWorkers, Attachments: opts.attachments,
	}, func(_ context.Context, token string) (authn.Identity, error) {
		agentID, ok := tokens.agent(token)
		if !ok {
			return nil, errors.New("unknown token")
		}
		// The agent as the server authenticates it, so what the tools ask of
		// the caller (a console, say) is answered as in production.
		return agents.Get(db.WithTenant(context.Background(), tenantID), agentID)
	}, resourceMetadataURL, slog.New(slog.DiscardHandler))
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0.0.1"}, nil)
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint: srv.URL, HTTPClient: &http.Client{Transport: bearer{token: goodToken}},
	}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = session.Close() })
	return &realRig{
		t: t, ctx: ctx, tickets: tickets, agentID: builder.ID,
		human: dbtest.NewUser(t, database), session: session,
		roles:    agent.NewRoleService(agent.NewPostgresRoleStore(database), slog.DiscardHandler),
		database: database, tenantID: tenantID, agents: agents, owners: owners, pending: pending, usage: usage, messages: messages,
		url: srv.URL, tokens: tokens, events: counter, labels: labels, rounds: rounds,
	}
}

// noSettingsHistory is a tenant that never set a setting; the review tools
// never read one.
type noSettingsHistory struct{}

func (noSettingsHistory) History(context.Context, string, int64, int) ([]settings.Change, int64, error) {
	return nil, 0, nil
}

type fixedStewardConfig struct{ cfg *steward.Config }

func (f fixedStewardConfig) StewardConfig(context.Context) (*steward.Config, error) {
	return f.cfg, nil
}

// tokenTable maps bearer tokens to agents; a test adds one while the server
// is serving.
type tokenTable struct {
	mu      sync.Mutex
	byToken map[string]uuid.UUID
}

func (t *tokenTable) agent(token string) (uuid.UUID, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	id, ok := t.byToken[token]
	return id, ok
}

func (t *tokenTable) add(token string, id uuid.UUID) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.byToken[token] = id
}

func (r *realRig) call(name string, args map[string]any) *mcp.CallToolResult {
	r.t.Helper()
	res, err := r.session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	require.NoError(r.t, err, "a tool error, not a protocol failure")
	return res
}

func (r *realRig) openTicket(title string) *ticket.Ticket {
	r.t.Helper()
	created, err := r.tickets.Create(r.ctx, &ticket.CreateParams{Title: title, Status: ticket.StatusOpen})
	require.NoError(r.t, err)
	return created
}

// claimThenRevoke has the agent claim a ticket over MCP and a human move it
// to Preparing, returning it.
func (r *realRig) claimThenRevoke() *ticket.Ticket {
	r.t.Helper()
	held := r.openTicket("Being worked")
	claimed := r.call("ticket_next", map[string]any{"ticket_id": held.ID})
	require.False(r.t, claimed.IsError, resultText(r.t, claimed))
	require.Contains(r.t, resultText(r.t, claimed), "Granted")

	status := ticket.StatusPreparing
	_, err := r.tickets.Update(r.ctx, held.ID, &ticket.UpdateParams{Status: &status})
	require.NoError(r.t, err)
	return held
}

// The whole arc: the call that needs the withdrawn ticket is refused with
// the reason and does nothing, the agent holds nothing after, and the
// withdrawn ticket is neither finishable nor handed back out.
func TestRevocation_ReachesTheAgentThroughItsNextToolCall(t *testing.T) {
	r := newRealRig(t)
	held := r.claimThenRevoke()

	told := r.call("ticket_comment", map[string]any{"ticket_id": held.ID, "body": "Nearly there."})
	assert.True(t, told.IsError)
	assert.Contains(t, resultText(t, told), revokedNotice)
	comments, err := r.tickets.ListComments(r.ctx, held.ID)
	require.NoError(t, err)
	assert.Empty(t, comments, "the call that carried the notice did nothing else")

	after := r.call("checkin", nil)
	assert.False(t, after.IsError, "told once: the next call goes through")
	assert.NotContains(t, resultText(t, after), revokedNotice)
	assert.Contains(t, resultText(t, after), "holding nothing")

	finished := r.call("ticket_finish", map[string]any{"pr_url": testPR, "handoff": "finished anyway"})
	assert.True(t, finished.IsError, "the withdrawn ticket cannot be finished")
	got, err := r.tickets.Get(r.ctx, held.ID)
	require.NoError(t, err)
	assert.Equal(t, ticket.StatusPreparing, got.Status)

	next := r.call("ticket_next", nil)
	assert.False(t, next.IsError)
	assert.Contains(t, resultText(t, next), "The queue is empty", "and the Preparing ticket is not handed back out")
}

// A call that doesn't need the withdrawn ticket does what was asked, with
// the notice leading its result -- once.
func TestRevocation_ALeaseFreeCallSucceedsAndCarriesTheNotice(t *testing.T) {
	for name, tc := range map[string]struct {
		tool   string
		args   func(r *realRig) map[string]any
		result string
	}{
		"checkin":                         {tool: "checkin", result: "holding nothing"},
		"ticket_next with an empty queue": {tool: "ticket_next", result: "The queue is empty"},
		"ticket_get": {
			tool:   "ticket_get",
			args:   func(r *realRig) map[string]any { return map[string]any{"ticket_id": r.openTicket("Other work").ID} },
			result: "Other work",
		},
	} {
		t.Run(name, func(t *testing.T) {
			r := newRealRig(t)
			r.claimThenRevoke()
			var args map[string]any
			if tc.args != nil {
				args = tc.args(r)
			}

			res := r.call(tc.tool, args)
			body := resultText(t, res)
			require.False(t, res.IsError, body)
			assert.Contains(t, body, tc.result, "the call did what was asked")
			assert.Equal(t, 1, strings.Count(body, revokedNotice), body)
			assert.Contains(t, firstText(t, res), revokedNotice, "the notice leads, where it is read before the result")

			again := r.call(tc.tool, args)
			assert.NotContains(t, resultText(t, again), revokedNotice, "delivered once")
		})
	}
}

// A call that needs the withdrawn ticket has nothing to act on, so the
// notice is its answer.
func TestRevocation_ALeaseHeldCallFailsWithTheNotice(t *testing.T) {
	for name, tc := range map[string]struct {
		tool string
		args func(held *ticket.Ticket) map[string]any
	}{
		"ticket_finish": {tool: "ticket_finish", args: func(*ticket.Ticket) map[string]any {
			return map[string]any{"handoff": "finished", "pr_url": testPR}
		}},
		"ticket_release": {tool: "ticket_release", args: func(*ticket.Ticket) map[string]any {
			return map[string]any{"reason": "handing back"}
		}},
		"ticket_discuss": {tool: "ticket_discuss", args: func(*ticket.Ticket) map[string]any {
			return map[string]any{"question": "Flat or nested?", "handoff": "Half written."}
		}},
		"ticket_prepare": {tool: "ticket_prepare", args: func(*ticket.Ticket) map[string]any {
			return map[string]any{"reason": "Not ready."}
		}},
		"ticket_scope_extend": {tool: "ticket_scope_extend", args: func(*ticket.Ticket) map[string]any {
			return map[string]any{"paths": []string{"server/"}}
		}},
		"ticket_scope_narrow": {tool: "ticket_scope_narrow", args: func(*ticket.Ticket) map[string]any {
			return map[string]any{"remove": []string{"server/"}}
		}},
		"ticket_edit": {tool: "ticket_edit", args: func(*ticket.Ticket) map[string]any {
			return map[string]any{"revision": 0, "title": "Renamed after the fact"}
		}},
		"integration_lock":   {tool: "integration_lock"},
		"integration_unlock": {tool: "integration_unlock"},
		"ticket_comment on the withdrawn ticket": {tool: "ticket_comment", args: func(held *ticket.Ticket) map[string]any {
			return map[string]any{"ticket_id": held.ID, "body": "Nearly there."}
		}},
	} {
		t.Run(name, func(t *testing.T) {
			r := newRealRig(t)
			held := r.claimThenRevoke()
			var args map[string]any
			if tc.args != nil {
				args = tc.args(held)
			}

			res := r.call(tc.tool, args)
			body := resultText(t, res)
			assert.True(t, res.IsError)
			assert.Equal(t, 1, strings.Count(body, revokedNotice), body)
			// Either phrase means the tool ran and the notice merely rode on
			// its outcome, rather than refusing it.
			assert.NotContains(t, body, "went through")
			assert.NotContains(t, body, "Separately")
			assert.NotContains(t, body, "no active lease", "refused with the reason, not run into a bare lease error")

			got, err := r.tickets.Get(r.ctx, held.ID)
			require.NoError(t, err)
			assert.Equal(t, ticket.StatusPreparing, got.Status, "the withdrawn ticket is untouched")
			assert.Equal(t, held.Title, got.Title)
			comments, err := r.tickets.ListComments(r.ctx, held.ID)
			require.NoError(t, err)
			assert.Empty(t, comments)
		})
	}
}

// A comment names its ticket, and one on any other ticket needs nothing
// the agent lost.
func TestRevocation_ACommentOnAnotherTicketLandsAndCarriesTheNotice(t *testing.T) {
	r := newRealRig(t)
	r.claimThenRevoke()
	other := r.openTicket("Somebody else's")

	res := r.call("ticket_comment", map[string]any{"ticket_id": other.ID, "body": "Spotted a problem here."})
	require.False(t, res.IsError, resultText(t, res))
	assert.Contains(t, firstText(t, res), revokedNotice)
	comments, err := r.tickets.ListComments(r.ctx, other.ID)
	require.NoError(t, err)
	require.Len(t, comments, 1)
	assert.Equal(t, "Spotted a problem here.", comments[0].Body)
	require.NotNil(t, comments[0].AuthorAgentID)
	assert.Equal(t, r.agentID, *comments[0].AuthorAgentID)
}

// The notice is marked delivered before the tool runs, so a lease-free
// call that then fails for its own reasons still has to carry it.
func TestRevocation_ALeaseFreeCallThatFailsStillCarriesTheNotice(t *testing.T) {
	r := newRealRig(t)
	r.claimThenRevoke()

	res := r.call("ticket_get", map[string]any{"ticket_id": 999999})
	body := resultText(t, res)
	assert.True(t, res.IsError)
	assert.Contains(t, body, revokedNotice)
	assert.Contains(t, body, "not found", "and the call's own failure")
}

// Whichever kind of call comes first delivers it; nothing after repeats it.
func TestRevocation_DeliveredOnceAcrossBothKindsOfCall(t *testing.T) {
	t.Run("lease-free first", func(t *testing.T) {
		r := newRealRig(t)
		r.claimThenRevoke()

		assert.Contains(t, resultText(t, r.call("checkin", nil)), revokedNotice)
		done := r.call("ticket_finish", map[string]any{"pr_url": testPR, "handoff": "finished"})
		assert.True(t, done.IsError)
		assert.NotContains(t, resultText(t, done), revokedNotice)
		assert.Contains(t, resultText(t, done), "your last ticket, #1, was moved to Preparing by a human")
		assert.NotContains(t, resultText(t, r.call("checkin", nil)), revokedNotice)
	})
	t.Run("lease-held first", func(t *testing.T) {
		r := newRealRig(t)
		r.claimThenRevoke()

		done := r.call("ticket_finish", map[string]any{"pr_url": testPR, "handoff": "finished"})
		assert.Contains(t, resultText(t, done), revokedNotice)
		assert.NotContains(t, resultText(t, r.call("checkin", nil)), revokedNotice)
		assert.NotContains(t, resultText(t, r.call("ticket_next", nil)), revokedNotice)
	})
}

// An agent told to stop and wait for work calls ticket_next next. That one
// call has to both tell it why and hand it the work that is waiting.
func TestRevocation_TicketNextDeliversTheNoticeAndGrantsClaimableWork(t *testing.T) {
	r := newRealRig(t)
	held := r.claimThenRevoke()
	waiting := r.openTicket("Waiting in the queue")

	res := r.call("ticket_next", nil)
	body := resultText(t, res)
	require.False(t, res.IsError, body)
	assert.Equal(t, 1, strings.Count(body, revokedNotice), body)
	assert.Contains(t, body, "Granted ticket #")

	lease, err := r.tickets.LeaseFor(r.ctx, r.agentID)
	require.NoError(t, err, "the grant is real, not just text")
	assert.Equal(t, waiting.ID, lease.TicketID)
	assert.NotEqual(t, held.ID, lease.TicketID)
}

const revokedContext = "You hold no lease: your last ticket, #1, was moved to Preparing by a human at "

// After the notice, the agent can still learn why it holds nothing, without
// the notice being repeated and without any state changing.
func TestRevocation_ContextFollowsTheNoticeOnLaterCalls(t *testing.T) {
	r := newRealRig(t)
	r.claimThenRevoke()

	first := r.call("checkin", nil)
	assert.Contains(t, resultText(t, first), revokedNotice)
	assert.NotContains(t, resultText(t, first), revokedContext, "the call that carries the notice does not also explain it")

	for range 2 {
		done := r.call("ticket_finish", map[string]any{"pr_url": testPR, "handoff": "finished"})
		assert.True(t, done.IsError)
		assert.Contains(t, resultText(t, done), revokedContext)
		assert.NotContains(t, resultText(t, done), revokedNotice)
		assert.NotContains(t, resultText(t, done), "ticket:")
	}

	checkin := r.call("checkin", nil)
	assert.False(t, checkin.IsError)
	assert.Contains(t, resultText(t, checkin), "holding nothing. "+revokedContext)
	assert.NotContains(t, resultText(t, checkin), revokedNotice)
}

// The context is only for an agent that still holds nothing.
func TestRevocation_ContextEndsWhenTheAgentClaimsAnotherTicket(t *testing.T) {
	r := newRealRig(t)
	r.claimThenRevoke()
	r.call("checkin", nil)
	other := r.openTicket("Next")
	require.False(t, r.call("ticket_next", map[string]any{"ticket_id": other.ID}).IsError)
	finished := r.call("ticket_finish", map[string]any{"pr_url": testPR, "handoff": "done"})
	require.False(t, finished.IsError, resultText(t, finished))

	assert.NotContains(t, resultText(t, r.call("checkin", nil)), "your last ticket")
	again := r.call("ticket_finish", map[string]any{"pr_url": testPR, "handoff": "again"})
	assert.True(t, again.IsError)
	assert.NotContains(t, resultText(t, again), "your last ticket")
	assert.Contains(t, resultText(t, again), "agent holds no active lease")
}

func firstText(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	require.NotEmpty(t, res.Content)
	tc, ok := res.Content[0].(*mcp.TextContent)
	require.True(t, ok)
	return tc.Text
}

// lateOwners is the owner resolver for one that needs the agent service,
// which is built from the ticket service; it resolves nobody until set.
type lateOwners struct{ resolver ticket.OwnerResolver }

func (l *lateOwners) ResolveOwner(ctx context.Context, agentID uuid.UUID) (*ticket.OwnerBound, error) {
	if l.resolver == nil {
		return nil, nil
	}
	return l.resolver.ResolveOwner(ctx, agentID)
}

// draftsOf reads the drafts the real store holds, as the board does.
type draftsOf struct{ store *assist.Store }

func (d draftsOf) Drafts(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]*pendingaction.DraftView, error) {
	found, err := d.store.ByAction(ctx, ids)
	out := map[uuid.UUID]*pendingaction.DraftView{}
	for id, f := range found {
		out[id] = &pendingaction.DraftView{Category: f.Category, Body: f.Body, WouldSendBack: f.WouldSendBack, Reason: f.Reason,
			TicketRevision: f.TicketRevision, HeadSHA: f.HeadSHA, Applied: f.AppliedAt != nil}
		for _, e := range f.Evidence {
			out[id].Evidence = append(out[id].Evidence, pendingaction.AssessmentEvidence{Check: e.Check, OK: e.OK, Ref: e.Ref, Detail: e.Detail})
		}
	}
	return out, err
}
