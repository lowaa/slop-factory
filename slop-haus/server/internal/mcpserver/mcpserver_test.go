package mcpserver_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit/audittest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/mcpserver"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

// These tests drive the tools through a real MCP client over the real
// transport. A handler unit test would not catch a schema the SDK refuses
// to build, a tool whose input type cannot be inferred, or auth applied in
// the wrong place -- and those are exactly the failures an agent would hit
// first.

type fakeTickets struct {
	// recent is what RecentRevocation answers.
	recent     *ticket.RevocationContext
	claim      *ticket.ClaimResult
	lease      *ticket.Lease
	extend     *ticket.ExtendResult
	narrowErr  error
	narrowed   *[2][]string
	heartbeats int
	// leaseErr is what LeaseFor fails with, ahead of lease.
	leaseErr    error
	claimErr    error
	claimedWith *int64
	done        *ticket.DoneParams
	// released is what ReleaseWith was last called with, and releaseOutcome
	// what it answers; the open ticket when nil.
	released       *ticket.ReleaseParams
	releaseOutcome *ticket.ReleaseOutcome
	comments       []ticket.Comment
	contexts       []ticket.AgentContext
	external       []ticket.ExternalEvent
	held           *ticket.AttemptRef
	discussed      []string // question, handoff
	discussedBy    uuid.UUID
	prepared       string
	created        *ticket.CreateParams
	overlaps       []ticket.ScopeOverlap
	overlapsErr    error
	commented      int
	// heartbeatErr, when set, is what every heartbeat returns -- standing in
	// for the store's once-only revocation notice. heartbeatErrs, when
	// non-empty, is consumed first, one per heartbeat.
	heartbeatErr  error
	heartbeatErrs []error
	doneErr       error
	// lockErr is what the integration lock call answers.
	lockErr      error
	prepareErr   error
	preparedBy   uuid.UUID
	edited       *ticket.EditParams
	editedBy     uuid.UUID
	editErr      error
	editNoChange bool
	revisions    []ticket.Revision
	// got, when set, is what Get returns.
	got *ticket.Ticket
	// branch is the held attempt's branch DetailFor answers with.
	branch     *ticket.AttemptBranch
	reviewed   *ticket.ReviewParams
	reviewedBy uuid.UUID
	reviewErr  error
	// gate is what ChangeGateFor answers: a promoted attempt of a ticket
	// needing no approval when nil.
	gate    *ticket.ChangeGate
	gateErr error
	// depChange is what AddDependency and RemoveDependency answer, and
	// depAgent who they were called as.
	depChange *ticket.DependencyChange
	depAgent  uuid.UUID
	// listed is what List returns, and listedWith the filter it was given.
	listed     []ticket.Ticket
	listedWith *ticket.ListFilter
}

func (f *fakeTickets) Claim(_ context.Context, _ uuid.UUID, only *int64) (*ticket.ClaimResult, error) {
	f.claimedWith = only
	if f.claimErr != nil {
		return nil, f.claimErr
	}
	if f.claim == nil {
		return &ticket.ClaimResult{}, nil
	}
	return f.claim, nil
}

func (f *fakeTickets) Heartbeat(context.Context, uuid.UUID) error {
	f.heartbeats++
	if len(f.heartbeatErrs) > 0 {
		next := f.heartbeatErrs[0]
		f.heartbeatErrs = f.heartbeatErrs[1:]
		return next
	}
	if f.heartbeatErr != nil {
		return f.heartbeatErr
	}
	if f.lease == nil {
		return ticket.ErrNoLease
	}
	return nil
}

func (f *fakeTickets) ReleaseWith(_ context.Context, _ uuid.UUID, p ticket.ReleaseParams) (*ticket.ReleaseOutcome, error) {
	f.released = &p
	if f.releaseOutcome != nil {
		return f.releaseOutcome, nil
	}
	return &ticket.ReleaseOutcome{Ticket: &ticket.Ticket{ID: 1, Status: ticket.StatusOpen}}, nil
}

func (f *fakeTickets) Done(_ context.Context, _ uuid.UUID, params *ticket.DoneParams) (*ticket.Ticket, error) {
	f.done = params
	if f.doneErr != nil {
		return nil, f.doneErr
	}
	return &ticket.Ticket{ID: 1, Status: ticket.StatusDone}, nil
}

func (f *fakeTickets) ExtendScope(context.Context, uuid.UUID, []model.Path) (*ticket.ExtendResult, error) {
	if f.extend == nil {
		return &ticket.ExtendResult{Granted: true}, nil
	}
	return f.extend, nil
}

func (f *fakeTickets) NarrowScope(_ context.Context, _ uuid.UUID, remove, keep []string) (*ticket.NarrowResult, error) {
	f.narrowed = &[2][]string{remove, keep}
	if f.narrowErr != nil {
		return nil, f.narrowErr
	}
	return &ticket.NarrowResult{Removed: remove, Kept: keep, Remaining: []string{"server/a/"}}, nil
}

func (f *fakeTickets) AcquireIntegrationLock(context.Context, uuid.UUID) error { return f.lockErr }
func (f *fakeTickets) ReleaseIntegrationLock(context.Context, uuid.UUID) error { return nil }

func (f *fakeTickets) LeaseFor(context.Context, uuid.UUID) (*ticket.Lease, error) {
	if f.leaseErr != nil {
		return nil, f.leaseErr
	}
	if f.lease == nil {
		return nil, ticket.ErrNoLease
	}
	return f.lease, nil
}

func (f *fakeTickets) RecentRevocation(context.Context, uuid.UUID) (*ticket.RevocationContext, error) {
	return f.recent, nil
}

func (f *fakeTickets) Get(_ context.Context, id int64) (*ticket.Ticket, error) {
	if f.got != nil {
		return f.got, nil
	}
	return &ticket.Ticket{ID: id, Title: "A ticket", Status: ticket.StatusOpen}, nil
}

func (f *fakeTickets) List(_ context.Context, filter *ticket.ListFilter) ([]ticket.Ticket, error) {
	f.listedWith = filter
	return f.listed, nil
}

func (f *fakeTickets) Create(_ context.Context, p *ticket.CreateParams) (*ticket.Ticket, error) {
	f.created = p
	return &ticket.Ticket{ID: 99, Title: p.Title, Status: ticket.StatusPreparing}, nil
}

func (f *fakeTickets) ScopeOverlaps(context.Context, int64) ([]ticket.ScopeOverlap, error) {
	return f.overlaps, f.overlapsErr
}

func (f *fakeTickets) AddComment(context.Context, int64, ticket.Author, string) (*ticket.Comment, error) {
	f.commented++
	return &ticket.Comment{ID: 1}, nil
}

func (f *fakeTickets) AgentCommentOn(context.Context, uuid.UUID, int64, *int, string) (*ticket.Comment, error) {
	f.commented++
	return &ticket.Comment{ID: 1}, nil
}

func (f *fakeTickets) ResolveAttempt(context.Context, int64, *int) (*ticket.AttemptRef, error) {
	return nil, nil
}

func (f *fakeTickets) Prepare(_ context.Context, agentID uuid.UUID, reason string) (*ticket.Ticket, error) {
	f.prepared = reason
	f.preparedBy = agentID
	if f.prepareErr != nil {
		return nil, f.prepareErr
	}
	return &ticket.Ticket{ID: 1, Status: ticket.StatusPreparing}, nil
}

func (f *fakeTickets) ListComments(context.Context, int64) ([]ticket.Comment, error) {
	return f.comments, nil
}

func (f *fakeTickets) Discuss(_ context.Context, agentID uuid.UUID, question, handoff string) (*ticket.Ticket, error) {
	f.discussed = []string{question, handoff}
	f.discussedBy = agentID
	return &ticket.Ticket{ID: 1, Status: ticket.StatusDiscussion}, nil
}

func (f *fakeTickets) ChangeGateFor(context.Context, uuid.UUID) (*ticket.ChangeGate, error) {
	if f.gateErr != nil {
		return nil, f.gateErr
	}
	if f.gate != nil {
		return f.gate, nil
	}
	return &ticket.ChangeGate{Ticket: &ticket.Ticket{ID: 1}, Promoted: true}, nil
}

func (f *fakeTickets) Review(_ context.Context, agentID uuid.UUID, params *ticket.ReviewParams) (*ticket.Ticket, error) {
	f.reviewed, f.reviewedBy = params, agentID
	if f.reviewErr != nil {
		return nil, f.reviewErr
	}
	return &ticket.Ticket{ID: 1, Status: ticket.StatusReview}, nil
}

func (f *fakeTickets) ListAgentContexts(context.Context, int64) ([]ticket.AgentContext, error) {
	return f.contexts, nil
}

func (f *fakeTickets) EditHeld(_ context.Context, agentID uuid.UUID, params *ticket.EditParams) (*ticket.EditResult, error) {
	f.edited, f.editedBy = params, agentID
	if f.editErr != nil {
		return nil, f.editErr
	}
	return &ticket.EditResult{Ticket: &ticket.Ticket{ID: 7, Revision: 3}, Changed: !f.editNoChange}, nil
}

func (f *fakeTickets) ListRevisions(context.Context, int64) ([]ticket.Revision, error) {
	return f.revisions, nil
}

type fakeSessions struct{ touched, progress int }

func (f *fakeSessions) Touch(context.Context, uuid.UUID) error {
	f.touched++
	return nil
}

func (f *fakeSessions) RecordProgress(context.Context, uuid.UUID) error {
	f.progress++
	return nil
}

type fakeAgents struct{}

func (fakeAgents) Name(context.Context, uuid.UUID) (string, error) { return "builder-1", nil }

// fakeRoles gives every caller the same roles.
type fakeRoles struct {
	names []string
	err   error
}

func (f *fakeRoles) RoleNames(context.Context, uuid.UUID) ([]string, error) { return f.names, f.err }

type identity struct {
	id       uuid.UUID
	readOnly bool
}

func (i identity) AgentID() uuid.UUID  { return i.id }
func (i identity) AgentName() string   { return "builder-1" }
func (i identity) TenantID() uuid.UUID { return testTenantID }
func (i identity) ReadOnly() bool      { return i.readOnly }
func (i identity) AgentKind() string   { return "worker" }

// testTenantID is fixed: these tests exercise the tool surface against
// fakes with no tenant concept of their own, not cross-tenant isolation,
// which is covered against the real stores in internal/ticket.
var testTenantID = uuid.New()

const goodToken = "valid-token"

// resourceMetadataURL stands in for oauthSvc.ResourceMetadataURL() in
// cmd/api/main.go: a 401 carries it in the challenge so an MCP client
// discovers this server wants OAuth (RFC 9728), which is what these tests
// check for.
const resourceMetadataURL = "https://slop-haus.test/.well-known/oauth-protected-resource"

const testBoardURL = "https://board.slop-haus.test"

type rig struct {
	// url is where the server is listening.
	url      string
	session  *mcp.ClientSession
	tickets  *fakeTickets
	sessions *fakeSessions
	roles    *fakeRoles
	events   *audittest.Counter
	// agentID is who the rig's calls are made as.
	agentID uuid.UUID
}

func newRig(t *testing.T) *rig {
	t.Helper()
	return newRigWith(t, &rigOptions{})
}

// newRigAs is newRig signed in with a read-only credential when readOnly.
func newRigAs(t *testing.T, readOnly bool) *rig {
	t.Helper()
	return newRigWith(t, &rigOptions{readOnly: readOnly})
}

type rigOptions struct {
	// lander is the handler's Lander (none if nil).
	lander mcpserver.Lander
	// serve defaults to httptest.NewServer.
	serve    func(http.Handler) *httptest.Server
	readOnly bool
	// client configures the test's MCP client.
	client *mcp.ClientOptions
	// logs receives the handler's log output (discarded if nil).
	logs slog.Handler
	// messages is what the handler delivers the human's words from.
	messages mcpserver.Messages
	// workspace is the handler's Workspace (none if nil).
	workspace mcpserver.Workspace
	// changes is the handler's Changes, which switches the change tools on
	// (none if nil).
	changes mcpserver.Changes
	// queueMode is the handler's QueueMode (the lock tools are always offered if nil).
	queueMode mcpserver.QueueMode
	// auditLogs receives the audit events' log lines (discarded if nil).
	auditLogs slog.Handler
	// refusals is the handler's Refusals (none if nil).
	refusals mcpserver.Refusals
	// batches switches landing_batches on (none if nil).
	batches mcpserver.Batches
	// toolUsage is the handler's ToolUsage (none if nil).
	toolUsage mcpserver.ToolUsage
}

func newRigWith(t *testing.T, opts *rigOptions) *rig {
	t.Helper()
	serve, readOnly := opts.serve, opts.readOnly
	if serve == nil {
		serve = httptest.NewServer
	}

	logs := opts.logs
	if logs == nil {
		logs = slog.DiscardHandler
	}

	tickets, sessions, roles := &fakeTickets{}, &fakeSessions{}, &fakeRoles{}
	events, counter := audittest.NewRecorder()
	if opts.auditLogs != nil {
		events = audit.New(opts.auditLogs, counter)
	}
	// One agent for the whole rig: the session is bound to whoever
	// initialized it, so a token resolving to a fresh agent per request
	// would have every call after the first refused.
	caller := identity{id: uuid.New(), readOnly: readOnly}
	handler := mcpserver.NewHTTPHandler(mcpserver.Deps{
		Tickets: tickets, Sessions: sessions, Agents: fakeAgents{}, Roles: roles, Events: events, Messages: opts.messages,
		BoardURL: testBoardURL + "/", Workspace: opts.workspace, Changes: opts.changes, Lander: opts.lander, QueueMode: opts.queueMode, Refusals: opts.refusals, Batches: opts.batches, ToolUsage: opts.toolUsage,
	}, func(_ context.Context, token string) (authn.Identity, error) {
		if token != goodToken {
			return nil, errors.New("unknown token")
		}
		return caller, nil
	}, resourceMetadataURL, slog.New(logs))

	srv := serve(handler)
	t.Cleanup(srv.Close)

	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0.0.1"}, opts.client)
	transport := &mcp.StreamableClientTransport{
		Endpoint:   srv.URL,
		HTTPClient: &http.Client{Transport: bearer{token: goodToken}},
	}
	cs, err := client.Connect(context.Background(), transport, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cs.Close() })

	return &rig{url: srv.URL, session: cs, tickets: tickets, sessions: sessions, roles: roles, events: counter, agentID: caller.id}
}

type bearer struct{ token string }

func (b bearer) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(clone)
}

func resultText(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

// The tool surface is what every agent carries in its context on every
// turn, so its size is a real cost. This pins it: adding a tool should be
// a deliberate act, not something that drifts.
func TestToolSurface(t *testing.T) {
	r := newRig(t)

	tools, err := r.session.ListTools(context.Background(), nil)
	require.NoError(t, err)

	names := make([]string, 0, len(tools.Tools))
	for _, tool := range tools.Tools {
		names = append(names, tool.Name)
		assert.NotEmpty(t, tool.Description, "tool %q needs a description; it is what the model reads", tool.Name)
		// Agents have no chat room to be sent to: every word a tool carries is
		// context, and one naming a channel that does not exist is a wrong turn.
		shown, err := json.Marshal(tool)
		require.NoError(t, err)
		assert.NotContains(t, strings.ToLower(string(shown)), "chat", "tool %q", tool.Name)
	}
	assert.ElementsMatch(t, []string{
		"checkin", "ticket_next", "ticket_get", "ticket_attachment_get", "ticket_attach", "ticket_search", "ticket_comment", "ticket_edit", "ticket_scope_extend", "ticket_scope_narrow",
		"ticket_release", "ticket_discuss", "ticket_finish", "ticket_prepare", "ticket_create",
		"ticket_blocker_add", "ticket_blocker_remove",
		"integration_lock", "integration_unlock",
	}, names)

	// No bare ticket_claim: cherry-picking defeats dependency ordering, so
	// ticket_next takes an optional id instead.
	assert.NotContains(t, names, "ticket_claim")
	// No general lock_acquire(name): a lock whose lifetime derives from a
	// lease only makes sense for a holder that has one.
	assert.NotContains(t, names, "lock_acquire")
	// No tool touches roles: a role gates which tickets an agent may claim,
	// so one an agent could grant itself would gate nothing.
	for _, name := range names {
		assert.NotContains(t, name, "role", "tool %q", name)
	}
}

func TestCheckin_ReportsIdentityAndHeldTicket(t *testing.T) {
	r := newRig(t)
	r.tickets.lease = &ticket.Lease{TicketID: 42}

	res, err := r.session.CallTool(context.Background(), &mcp.CallToolParams{Name: "checkin"})
	require.NoError(t, err)
	assert.Contains(t, resultText(t, res), "builder-1")
	assert.Contains(t, resultText(t, res), "#42")
}

// An agent learns its roles at checkin, in the text it reads and in the
// structured result; with none, the text says nothing about roles.
func TestCheckin_ReportsTheAgentsRoles(t *testing.T) {
	r := newRig(t)

	res, err := r.session.CallTool(context.Background(), &mcp.CallToolParams{Name: "checkin"})
	require.NoError(t, err)
	assert.NotContains(t, resultText(t, res), "roles")

	r.roles.names = []string{"deployer", "reviewer"}
	res, err = r.session.CallTool(context.Background(), &mcp.CallToolParams{Name: "checkin"})
	require.NoError(t, err)
	assert.Contains(t, resultText(t, res), "builder-1 (roles: deployer, reviewer)")
	var out struct {
		Roles []string `json:"roles"`
	}
	raw, err := json.Marshal(res.StructuredContent)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &out))
	assert.Equal(t, []string{"deployer", "reviewer"}, out.Roles)
}

// A checkin that couldn't read the roles fails rather than telling the
// agent it has none.
func TestCheckin_FailsWhenRolesCannotBeRead(t *testing.T) {
	r := newRig(t)
	r.roles.err = errors.New("database down")

	res, err := r.session.CallTool(context.Background(), &mcp.CallToolParams{Name: "checkin"})
	require.NoError(t, err)
	assert.True(t, res.IsError, resultText(t, res))
	assert.NotContains(t, resultText(t, res), "holding nothing")
}

func TestTicketNext_GrantsAndReportsScopes(t *testing.T) {
	r := newRig(t)
	r.tickets.claim = &ticket.ClaimResult{Ticket: &ticket.Ticket{
		ID: 7, Title: "Add leases", Status: ticket.StatusInProgress,
		Paths: []model.Path{{Path: "server/internal/ticket/"}},
	}}

	res, err := r.session.CallTool(context.Background(), &mcp.CallToolParams{Name: "ticket_next"})
	require.NoError(t, err)
	body := resultText(t, res)
	assert.Contains(t, body, "#7")
	assert.Contains(t, body, "server/internal/ticket/", "the agent must be told its scope")
}

// A deadlocked queue must say why. An agent that gets a bare "nothing for
// you" stops silently and the human sees an idle fleet rather than a
// contended one.
func TestTicketNext_BlockedExplainsItself(t *testing.T) {
	r := newRig(t)
	r.tickets.claim = &ticket.ClaimResult{Blocked: &ticket.Blocked{
		TicketID: 9, ConflictingTicketID: 3, ConflictingPath: "server/",
	}}

	res, err := r.session.CallTool(context.Background(), &mcp.CallToolParams{Name: "ticket_next"})
	require.NoError(t, err)
	body := resultText(t, res)
	assert.Contains(t, body, "#3")
	assert.Contains(t, body, "server/")
	assert.Contains(t, body, "stop here", "it should be told to stop, not retry")
}

func TestTicketNext_PassesADirectedTicketID(t *testing.T) {
	r := newRig(t)

	_, err := r.session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "ticket_next",
		Arguments: map[string]any{"ticket_id": 12},
	})
	require.NoError(t, err)
	require.NotNil(t, r.tickets.claimedWith)
	assert.Equal(t, int64(12), *r.tickets.claimedWith)
}

// Every tool is a heartbeat: there is no separate keepalive to forget.
func TestEveryToolCallHeartbeatsAndTouchesTheSession(t *testing.T) {
	r := newRig(t)

	_, err := r.session.CallTool(context.Background(), &mcp.CallToolParams{Name: "checkin"})
	require.NoError(t, err)
	assert.Equal(t, 1, r.tickets.heartbeats)
	assert.Equal(t, 1, r.sessions.touched)

	_, err = r.session.CallTool(context.Background(), &mcp.CallToolParams{Name: "checkin"})
	require.NoError(t, err)
	assert.Equal(t, 2, r.tickets.heartbeats, "every call is a heartbeat")
}

// A denied extension mutated nothing, so it must not count as progress --
// otherwise an agent stuck in an extend-deny cycle evades the stop-hook
// loop breaker forever.
func TestDeniedScopeExtensionIsNotProgress(t *testing.T) {
	r := newRig(t)
	r.tickets.extend = &ticket.ExtendResult{
		Denied: &ticket.Blocked{ConflictingTicketID: 3, ConflictingPath: "server/"},
	}

	before := r.sessions.progress
	res, err := r.session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "ticket_scope_extend", Arguments: map[string]any{"paths": []string{"server/x.go"}},
	})
	require.NoError(t, err)
	assert.Contains(t, resultText(t, res), "Denied")
	assert.Equal(t, before, r.sessions.progress, "a denial is not progress")
}

func TestGrantedScopeExtensionIsProgress(t *testing.T) {
	r := newRig(t)

	before := r.sessions.progress
	_, err := r.session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "ticket_scope_extend", Arguments: map[string]any{"paths": []string{"server/x.go"}},
	})
	require.NoError(t, err)
	assert.Equal(t, before+1, r.sessions.progress)
}

func TestScopeNarrow_PassesRemoveAndKeepAndIsProgress(t *testing.T) {
	r := newRig(t)

	before := r.sessions.progress
	res, err := r.session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "ticket_scope_narrow",
		Arguments: map[string]any{
			"remove": []string{"server/"},
			"keep":   []string{"server/a/"},
		},
	})
	require.NoError(t, err)
	require.False(t, res.IsError)
	require.NotNil(t, r.tickets.narrowed)
	assert.Equal(t, []string{"server/"}, r.tickets.narrowed[0])
	assert.Equal(t, []string{"server/a/"}, r.tickets.narrowed[1])
	assert.Contains(t, resultText(t, res), "server/a/")
	assert.Equal(t, before+1, r.sessions.progress)
}

func TestScopeNarrow_RefusalIsAToolErrorAndNotProgress(t *testing.T) {
	r := newRig(t)
	r.tickets.narrowErr = ticket.ErrNarrowAdds

	before := r.sessions.progress
	res, err := r.session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "ticket_scope_narrow", Arguments: map[string]any{"remove": []string{"server/"}, "keep": []string{"web/"}},
	})
	require.NoError(t, err)
	assert.True(t, res.IsError)
	assert.Contains(t, resultText(t, res), "ticket_scope_extend")
	assert.Equal(t, before, r.sessions.progress)
}

func TestTicketGet_ShowsThePRURL(t *testing.T) {
	r := newRig(t)
	link := "https://example.test/org/repo/pull/7"
	r.tickets.got = &ticket.Ticket{ID: 1, Title: "Finished", Status: ticket.StatusDone, PRURL: &link}

	res, err := r.session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "ticket_get", Arguments: map[string]any{"ticket_id": 1},
	})
	require.NoError(t, err)
	require.False(t, res.IsError, resultText(t, res))
	assert.Contains(t, resultText(t, res), "\nPR: "+link)

	var out struct {
		PRURL string `json:"pr_url"`
	}
	raw, err := json.Marshal(res.StructuredContent)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &out))
	assert.Equal(t, link, out.PRURL)
}

// The link an agent puts in its PR description. It names the tenant: a
// ticket's number is only unique within one, and the board refuses to show
// a ticket from a tenant other than the one its link names.
func TestTicketGet_LinksToTheTicketOnTheBoard(t *testing.T) {
	r := newRig(t)
	r.tickets.got = &ticket.Ticket{ID: 12, Title: "Linked", Status: ticket.StatusInProgress}

	res, err := r.session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "ticket_get", Arguments: map[string]any{"ticket_id": 12},
	})
	require.NoError(t, err)
	require.False(t, res.IsError, resultText(t, res))

	want := testBoardURL + "/tickets/12?tenant=" + testTenantID.String()
	assert.Equal(t, want, structured(t, res)["url"])
	assert.Contains(t, resultText(t, res), "\nLink: "+want)
}

func TestTicketNext_TellsTheAgentWhereItsPRLinksTo(t *testing.T) {
	r := newRig(t)
	r.tickets.claim = &ticket.ClaimResult{Ticket: &ticket.Ticket{ID: 7, Title: "Add leases", Status: ticket.StatusInProgress}}

	res, err := r.session.CallTool(context.Background(), &mcp.CallToolParams{Name: "ticket_next"})
	require.NoError(t, err)
	require.False(t, res.IsError, resultText(t, res))

	want := testBoardURL + "/tickets/7?tenant=" + testTenantID.String()
	assert.Contains(t, resultText(t, res), want)
	assert.Contains(t, resultText(t, res), "slop-haus #7")
	ticketOut, ok := structured(t, res)["ticket"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, want, ticketOut["url"])
}

// A follow-up an agent files gets its link too, for the agent to cite it
// in a comment or in the PR that splits the work.
func TestTicketCreate_ReturnsTheNewTicketsLink(t *testing.T) {
	r := newRig(t)

	res, err := r.session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "ticket_create", Arguments: map[string]any{"title": "Follow-up"},
	})
	require.NoError(t, err)
	require.False(t, res.IsError, resultText(t, res))
	assert.Equal(t, testBoardURL+"/tickets/99?tenant="+testTenantID.String(), structured(t, res)["url"])
}

func TestTicketGet_NoPRLineWithoutAPR(t *testing.T) {
	r := newRig(t)

	res, err := r.session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "ticket_get", Arguments: map[string]any{"ticket_id": 1},
	})
	require.NoError(t, err)
	assert.NotContains(t, resultText(t, res), "PR:")
}

func TestUnauthenticatedClientCannotConnect(t *testing.T) {
	tickets, sessions := &fakeTickets{}, &fakeSessions{}
	handler := mcpserver.NewHTTPHandler(mcpserver.Deps{
		Tickets: tickets, Sessions: sessions, Agents: fakeAgents{}, Roles: &fakeRoles{},
	}, func(_ context.Context, token string) (authn.Identity, error) {
		if token != goodToken {
			return nil, errors.New("unknown token")
		}
		return identity{id: uuid.New()}, nil
	}, resourceMetadataURL, slog.New(slog.DiscardHandler))

	srv := httptest.NewServer(handler)
	defer srv.Close()

	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0.0.1"}, nil)
	_, err := client.Connect(context.Background(),
		&mcp.StreamableClientTransport{Endpoint: srv.URL}, nil)
	require.Error(t, err, "the MCP endpoint must not be reachable without a token")
}

// A bare 401 tells a human nothing; an MCP client needs the challenge to
// find out this server wants OAuth at all (RFC 9728). Driven with a raw
// HTTP request rather than the MCP client, which surfaces only the
// connection failure and not the response headers behind it.
func TestUnauthenticatedRequest_ChallengesWithResourceMetadata(t *testing.T) {
	handler := mcpserver.NewHTTPHandler(mcpserver.Deps{
		Tickets: &fakeTickets{}, Sessions: &fakeSessions{}, Agents: fakeAgents{}, Roles: &fakeRoles{},
	}, func(_ context.Context, token string) (authn.Identity, error) {
		return nil, errors.New("unknown token")
	}, resourceMetadataURL, slog.New(slog.DiscardHandler))

	srv := httptest.NewServer(handler)
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	assert.Equal(t, `Bearer resource_metadata="`+resourceMetadataURL+`"`, resp.Header.Get("WWW-Authenticate"))
}

// Codex re-runs its http_headers_helper only when a call is answered with a
// 401, so a rejected bearer token on a tools/call must be that and not a 403
// or a tool-level error.
func TestExpiredBearerToken_ToolCallAnswers401(t *testing.T) {
	handler := mcpserver.NewHTTPHandler(mcpserver.Deps{
		Tickets: &fakeTickets{}, Sessions: &fakeSessions{}, Agents: fakeAgents{}, Roles: &fakeRoles{},
	}, func(_ context.Context, token string) (authn.Identity, error) {
		return nil, errors.New("expired token")
	}, resourceMetadataURL, slog.New(slog.DiscardHandler))

	srv := httptest.NewServer(handler)
	defer srv.Close()

	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"checkin","arguments":{}}}`
	req, err := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer expired")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Mcp-Session-Id", "some-session")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

// The flag exists because the body is not a spec. It has to be said where
// the agent cannot miss it -- at the claim -- not left in a field.
func TestTicketNext_ANeedsDiscussionTicketSaysPlanDontBuild(t *testing.T) {
	r := newRig(t)
	r.tickets.claim = &ticket.ClaimResult{Ticket: &ticket.Ticket{
		ID: 7, Title: "Rough idea", Status: ticket.StatusInProgress, NeedsDiscussion: true,
	}}

	res, err := r.session.CallTool(context.Background(), &mcp.CallToolParams{Name: "ticket_next"})
	require.NoError(t, err)
	body := resultText(t, res)
	assert.Contains(t, body, "NEEDS DISCUSSION")
	assert.Contains(t, body, "Do not build")
	assert.Contains(t, body, "ticket_discuss")
}

func TestTicketNext_AnOrdinaryTicketSaysNothingAboutDiscussion(t *testing.T) {
	r := newRig(t)
	r.tickets.claim = &ticket.ClaimResult{Ticket: &ticket.Ticket{ID: 7, Title: "Spec'd", Status: ticket.StatusInProgress}}

	res, err := r.session.CallTool(context.Background(), &mcp.CallToolParams{Name: "ticket_next"})
	require.NoError(t, err)
	assert.NotContains(t, resultText(t, res), "DISCUSSION")
}

func TestTicketDiscuss_PassesTheQuestionAndHandoffAndCountsAsProgress(t *testing.T) {
	r := newRig(t)
	r.tickets.lease = &ticket.Lease{TicketID: 1}

	res, err := r.session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "ticket_discuss",
		Arguments: map[string]any{"question": "Flat or nested?", "handoff": "Half the handler is written."},
	})
	require.NoError(t, err)
	require.False(t, res.IsError, resultText(t, res))
	assert.Equal(t, []string{"Flat or nested?", "Half the handler is written."}, r.tickets.discussed)
	assert.NotEqual(t, uuid.Nil, r.tickets.discussedBy, "it acts as the authenticated agent, not as nobody")
	assert.Positive(t, r.sessions.progress, "entering discussion is a change, not a stall")
	assert.Contains(t, resultText(t, res), "no longer hold",
		"the agent must be told the ticket is gone, or it keeps working on it")
}

// A resuming agent needs the conversation and the handoff; it gets the
// latest handoff only, since each supersedes the last.
func TestTicketGet_IncludesTheConversationAndTheLatestHandoff(t *testing.T) {
	r := newRig(t)
	asker := uuid.New()
	r.tickets.comments = []ticket.Comment{
		{AuthorAgentID: &asker, Body: "Flat or nested?", CreatedAt: time.Date(2026, 10, 3, 11, 54, 38, 0, time.UTC)},
		{Body: "Flat.", CreatedAt: time.Date(2026, 10, 3, 11, 59, 32, 0, time.UTC)},
	}
	r.tickets.contexts = []ticket.AgentContext{
		{AgentID: asker, Body: "stale handoff"},
		{AgentID: asker, Body: "Half the handler is written; do the flat shape next."},
	}

	res, err := r.session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "ticket_get", Arguments: map[string]any{"ticket_id": 1},
	})
	require.NoError(t, err)
	body := resultText(t, res)
	assert.Contains(t, body, "Flat or nested?")
	assert.Contains(t, body, "- human (2026-10-03T11:59:32Z): Flat.", "the human's reply is labelled as the human's, with the server's time")
	assert.Contains(t, body, "- builder-1 (2026-10-03T11:54:38Z): Flat or nested?", "an agent's comment carries its name")
	assert.Contains(t, body, "Half the handler is written; do the flat shape next.")
	assert.NotContains(t, body, "stale handoff")
}
