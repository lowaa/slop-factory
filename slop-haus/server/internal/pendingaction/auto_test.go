package pendingaction_test

import (
	"context"
	"log/slog"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lowaa/slop-factory/go-auth/ratelimit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/agent"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit/audittest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/oauth"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/pendingaction"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/user"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/usersettings"
)

// tenantSettingsReader is the production adapter's shape: the setting is
// named by the kind, and read from the real tenant settings table.
type tenantSettingsReader struct {
	svc *usersettings.TenantService
	// beforeRead runs when a setting is read, which is between a proposal
	// being stored and its approval: the moment a ticket can move under it.
	beforeRead func()
}

func (r *tenantSettingsReader) AutoApprove(ctx context.Context, userID uuid.UUID, kind pendingaction.Kind) (bool, error) {
	if r.beforeRead != nil {
		r.beforeRead()
	}
	got, err := r.svc.Get(ctx, userID, "approvals.auto."+string(kind))
	if err != nil {
		return false, err
	}
	return got.Value.Bool, nil
}

type labels struct{ store *user.PostgresStore }

func (l labels) DisplayName(ctx context.Context, id uuid.UUID) (string, error) {
	u, err := l.store.FindByID(ctx, id)
	if err != nil {
		return "", err
	}
	return u.Label(), nil
}

type roles struct{ store *user.PostgresStore }

func (r roles) IsTenantOwner(ctx context.Context, userID, tenantID uuid.UUID) (bool, error) {
	return r.store.IsOwner(ctx, userID, tenantID)
}

func (r roles) IsTenantMember(ctx context.Context, userID, tenantID uuid.UUID) (bool, error) {
	return r.store.IsMember(ctx, userID, tenantID)
}

// fixedOwner names the tenant's owner as the test says.
type fixedOwner struct {
	id  uuid.UUID
	err error
}

func (o *fixedOwner) TenantOwner(context.Context, uuid.UUID) (uuid.UUID, error) { return o.id, o.err }

// decisionLog records what the steward is told of decisions.
type decisionLog struct {
	mu  sync.Mutex
	ids []uuid.UUID
}

func (d *decisionLog) RecordDecision(_ context.Context, id uuid.UUID, _ string, _ time.Time) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.ids = append(d.ids, id)
	return nil
}

func (d *decisionLog) seen(id uuid.UUID) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Contains(d.ids, id)
}

// autoFixture is the proposals service wired as the server wires it: the real
// owner resolver, real grants and real tenant settings, so who an agent acts
// for is decided by the code that decides it in production.
type autoFixture struct {
	*fixture
	svc        *pendingaction.Service
	settings   *usersettings.TenantService
	reader     *tenantSettingsReader
	conductors *agent.ConductorService
	events     *audittest.Counter
	owner      *fixedOwner
	decisions  *decisionLog
	a, b       *authn.User
}

func newAutoFixture(t *testing.T) *autoFixture {
	t.Helper()
	f := newFixture(t, pendingaction.Config{})
	users := user.NewPostgresStore(f.database)
	generous := &ratelimit.Config{Limit: 1000, Window: time.Minute, MaxKeys: 1000}
	grants := oauth.NewService(oauth.NewPostgresStore(f.database), f.agents, &oauth.Config{
		Issuer: "http://x", RegisterLimit: generous, DeviceLookupLimit: generous, DeviceStartLimit: generous,
	}, nil, slog.DiscardHandler)
	conductors := agent.NewConductorService(f.agents, roles{users}, slog.DiscardHandler)
	resolver := agent.NewOwnerResolver(f.agents, conductors, roles{users}, grants, labels{users})

	af := &autoFixture{fixture: f, conductors: conductors, owner: &fixedOwner{}, decisions: &decisionLog{}}
	af.settings = usersettings.NewTenantService(usersettings.NewPostgresTenantStore(f.database), usersettings.DefaultRegistry(), slog.DiscardHandler)
	af.reader = &tenantSettingsReader{svc: af.settings}
	recorder, counter := audittest.NewRecorder()
	af.events = counter
	af.svc = pendingaction.NewService(pendingaction.NewPostgresStore(f.database), pendingaction.Config{}, slog.DiscardHandler,
		pendingaction.WithApproval(f.tickets, grants),
		pendingaction.WithEvents(recorder),
		pendingaction.WithAutoApprove(resolver, af.reader),
		pendingaction.WithStewardOwner(af.owner),
		pendingaction.WithDecisionRecorder(af.decisions))
	af.svc.SetNow(f.clock.Now)
	af.a, af.b = af.member(t, f.tenant), af.member(t, f.tenant)
	af.owner.id = af.a.ID
	return af
}

func (f *autoFixture) member(t *testing.T, tenant uuid.UUID) *authn.User {
	t.Helper()
	id := dbtest.NewUser(t, f.database)
	_, err := f.database.Global().Exec(context.Background(),
		`INSERT INTO global_tenant_memberships (id, tenant, user_id, role) VALUES ($1, $2, $3, 'member')`, uuid.New(), tenant, id)
	require.NoError(t, err)
	return &authn.User{ID: id, TenantID: tenant, AuthTime: time.Now()}
}

func (f *autoFixture) grant(t *testing.T, agentID uuid.UUID, approver *authn.User) {
	t.Helper()
	_, err := f.database.Global().Exec(context.Background(), `
		INSERT INTO global_oauth_grants (id, tenant, agent_id, client_id, resource, approved_by, scope)
		VALUES ($1, $2, $3, 'slop-haus-workspace', 'r', $4, 'agent')`, uuid.New(), approver.TenantID, agentID, approver.ID)
	require.NoError(t, err)
}

// consoleFor is a console whose live full-access grant who approved.
func (f *autoFixture) consoleFor(t *testing.T, who *authn.User) uuid.UUID {
	t.Helper()
	id := f.newAgent(t, "console-"+uuid.NewString()[:8], true)
	f.grant(t, id, who)
	return id
}

func (f *autoFixture) conductorFor(t *testing.T, who *authn.User) uuid.UUID {
	t.Helper()
	c, err := f.conductors.Register(f.ctx, who, "conductor-"+uuid.NewString()[:8])
	require.NoError(t, err)
	f.grant(t, c.ID, who)
	return c.ID
}

func (f *autoFixture) turnOn(t *testing.T, who *authn.User, kind pendingaction.Kind) {
	t.Helper()
	ctx := db.WithTenant(context.Background(), who.TenantID)
	_, err := f.settings.Set(ctx, who.ID, "approvals.auto."+string(kind), usersettings.BoolValue(true))
	require.NoError(t, err)
}

func (f *autoFixture) proposeReady(t *testing.T, agentID uuid.UUID) *pendingaction.Action {
	t.Helper()
	got, err := f.svc.Propose(f.ctx, &pendingaction.ProposeParams{
		AgentID: agentID, TicketID: f.ticketIn(t, ticket.StatusPreparing), Kind: pendingaction.KindReady,
	})
	require.NoError(t, err)
	return got
}

func TestAutoApproveAppliesAtOnceAsTheOwnerAndSaysSo(t *testing.T) {
	f := newAutoFixture(t)
	console := f.consoleFor(t, f.a)
	f.turnOn(t, f.a, pendingaction.KindReady)

	got := f.proposeReady(t, console)

	assert.Equal(t, pendingaction.StateApproved, got.State)
	assert.True(t, got.AutoApproved())
	require.NotNil(t, got.DecidedByUserID)
	assert.Equal(t, f.a.ID, *got.DecidedByUserID)
	assert.Equal(t, ticket.StatusOpen, f.ticketStatus(t, *got.TicketID))
	assert.Equal(t, 1, f.events.Count("proposal_approved"))

	var cause string
	var actor uuid.UUID
	require.NoError(t, f.database.QueryRow(f.ctx,
		`SELECT cause, actor_id FROM ticket_status_changes WHERE ticket_id = $1 ORDER BY id DESC LIMIT 1`, got.TicketID).Scan(&cause, &actor))
	assert.Equal(t, pendingaction.AutoCauseFor(got.ID), cause)
	assert.Equal(t, f.a.ID, actor)
}

func TestAutoApproveOffLeavesTheProposalPending(t *testing.T) {
	f := newAutoFixture(t)
	console := f.consoleFor(t, f.a)

	got := f.proposeReady(t, console)

	assert.Equal(t, pendingaction.StatePending, got.State)
	assert.False(t, got.AutoApproved())
	assert.Equal(t, ticket.StatusPreparing, f.ticketStatus(t, *got.TicketID))
}

func TestAutoApproveIsPerKind(t *testing.T) {
	f := newAutoFixture(t)
	console := f.consoleFor(t, f.a)
	f.turnOn(t, f.a, pendingaction.KindStatus)
	id := f.ticketIn(t, ticket.StatusOpen)
	rev := 0
	title := "renamed"

	got, err := f.svc.Propose(f.ctx, &pendingaction.ProposeParams{
		AgentID: console, TicketID: id, Kind: pendingaction.KindEdit,
		Payload: pendingaction.Payload{Edit: &pendingaction.EditPayload{BaseRevision: rev, Title: &title}},
	})

	require.NoError(t, err)
	assert.Equal(t, pendingaction.StatePending, got.State, "status is on, edit is not")
}

func TestAutoApproveUsesTheConductorsRegisteredOwner(t *testing.T) {
	f := newAutoFixture(t)
	conductor := f.conductorFor(t, f.a)
	f.turnOn(t, f.a, pendingaction.KindReady)

	got := f.proposeReady(t, conductor)

	assert.True(t, got.AutoApproved())
	assert.Equal(t, f.a.ID, *got.DecidedByUserID)
}

func TestAutoApproveIgnoresAnotherMembersSettingForAConsoleTheyDidNotApprove(t *testing.T) {
	f := newAutoFixture(t)
	consoleOfB := f.consoleFor(t, f.b)
	f.turnOn(t, f.a, pendingaction.KindReady)

	got := f.proposeReady(t, consoleOfB)

	assert.Equal(t, pendingaction.StatePending, got.State)
	assert.Equal(t, ticket.StatusPreparing, f.ticketStatus(t, *got.TicketID))
}

func TestAutoApproveIgnoresAConductorOfAnotherMember(t *testing.T) {
	f := newAutoFixture(t)
	conductorOfB := f.conductorFor(t, f.b)
	f.turnOn(t, f.a, pendingaction.KindReady)

	assert.Equal(t, pendingaction.StatePending, f.proposeReady(t, conductorOfB).State)
}

func TestAutoApproveNeedsAnOwnerTheResolverAccepts(t *testing.T) {
	f := newAutoFixture(t)
	f.turnOn(t, f.a, pendingaction.KindReady)
	f.turnOn(t, f.b, pendingaction.KindReady)

	noGrant := f.newAgent(t, "no-grant", true)
	twoApprovers := f.consoleFor(t, f.a)
	f.grant(t, twoApprovers, f.b)

	assert.Equal(t, pendingaction.StatePending, f.proposeReady(t, noGrant).State, "no live grant, so no owner")
	assert.Equal(t, pendingaction.StatePending, f.proposeReady(t, twoApprovers).State, "two approvers is ambiguous")
}

func TestAWorkerCannotProposeSoNothingIsAutoApproved(t *testing.T) {
	f := newAutoFixture(t)
	f.turnOn(t, f.a, pendingaction.KindReady)
	worker := f.newAgent(t, "worker", false)

	_, err := f.svc.Propose(f.ctx, &pendingaction.ProposeParams{
		AgentID: worker, TicketID: f.ticketIn(t, ticket.StatusPreparing), Kind: pendingaction.KindReady,
	})

	assert.ErrorIs(t, err, pendingaction.ErrNotConsole)
	assert.Zero(t, f.count(t))
}

func TestAutoApproveOfAStaleProposalSettlesItStaleAndTheProposeCallSucceeds(t *testing.T) {
	f := newAutoFixture(t)
	console := f.consoleFor(t, f.a)
	f.turnOn(t, f.a, pendingaction.KindReady)
	id := f.ticketIn(t, ticket.StatusPreparing)
	f.reader.beforeRead = func() { f.exec(t, `UPDATE tickets SET revision = revision + 1 WHERE id = $1`, id) }

	got, err := f.svc.Propose(f.ctx, &pendingaction.ProposeParams{AgentID: console, TicketID: id, Kind: pendingaction.KindReady})

	require.NoError(t, err)
	assert.Equal(t, pendingaction.StateStale, got.State)
	assert.False(t, got.AutoApproved())
	assert.Equal(t, ticket.StatusPreparing, f.ticketStatus(t, id))
}

func TestAutoApproveInOneTenantDoesNotApplyInAnother(t *testing.T) {
	f := newAutoFixture(t)
	f.turnOn(t, f.a, pendingaction.KindReady)

	other := dbtest.NewTenant(t, f.database)
	otherCtx := db.WithTenant(context.Background(), other)
	aThere := &authn.User{ID: f.a.ID, TenantID: other}
	_, err := f.database.Global().Exec(context.Background(),
		`INSERT INTO global_tenant_memberships (id, tenant, user_id, role) VALUES ($1, $2, $3, 'member')`, uuid.New(), other, f.a.ID)
	require.NoError(t, err)
	created, err := f.agents.CreateKind(otherCtx, "console-elsewhere", agent.KindConsole)
	require.NoError(t, err)
	f.grant(t, created.ID, aThere)
	tk, err := f.tickets.Create(otherCtx, &ticket.CreateParams{Title: "elsewhere", Status: ticket.StatusPreparing})
	require.NoError(t, err)

	got, err := f.svc.Propose(otherCtx, &pendingaction.ProposeParams{AgentID: created.ID, TicketID: tk.ID, Kind: pendingaction.KindReady})

	require.NoError(t, err)
	assert.Equal(t, pendingaction.StatePending, got.State, "the switch is on in the first tenant only")
}

// The grant is revoked after the owner is resolved and before the approval
// runs: the approval's own check must still refuse, as it does a click.
func TestAutoApproveIsRefusedByTheApprovalChecksAClickedApprovalMeets(t *testing.T) {
	f := newAutoFixture(t)
	console := f.consoleFor(t, f.a)
	f.turnOn(t, f.a, pendingaction.KindReady)
	f.reader.beforeRead = func() { f.exec(t, `UPDATE global_oauth_grants SET revoked_at = now()`) }

	got := f.proposeReady(t, console)

	assert.Equal(t, pendingaction.StateVoided, got.State)
	assert.Equal(t, ticket.StatusPreparing, f.ticketStatus(t, *got.TicketID))
}

func TestAutoApproveAppliesEachKindAsAClickWould(t *testing.T) {
	newTitle := "renamed"
	cases := []struct {
		kind   pendingaction.Kind
		status ticket.Status
		params func(f *autoFixture, id int64) pendingaction.ProposeParams
		check  func(t *testing.T, f *autoFixture, id int64)
	}{
		{pendingaction.KindStatus, ticket.StatusOpen,
			func(*autoFixture, int64) pendingaction.ProposeParams {
				return pendingaction.ProposeParams{Payload: pendingaction.Payload{Status: ticket.StatusParked, Body: "later"}}
			},
			func(t *testing.T, f *autoFixture, id int64) {
				assert.Equal(t, ticket.StatusParked, f.ticketStatus(t, id))
			}},
		{pendingaction.KindEdit, ticket.StatusOpen,
			func(*autoFixture, int64) pendingaction.ProposeParams {
				return pendingaction.ProposeParams{Payload: pendingaction.Payload{Edit: &pendingaction.EditPayload{Title: &newTitle}}}
			},
			func(t *testing.T, f *autoFixture, id int64) {
				got, err := f.tickets.Get(f.ctx, id)
				require.NoError(t, err)
				assert.Equal(t, newTitle, got.Title)
			}},
		{pendingaction.KindDiscussionAnswer, ticket.StatusDiscussion,
			func(*autoFixture, int64) pendingaction.ProposeParams {
				return pendingaction.ProposeParams{Payload: pendingaction.Payload{Body: "use B"}}
			},
			func(t *testing.T, f *autoFixture, id int64) {
				assert.Equal(t, ticket.StatusOpen, f.ticketStatus(t, id))
				comments, err := f.tickets.ListComments(f.ctx, id)
				require.NoError(t, err)
				require.Len(t, comments, 1)
				require.NotNil(t, comments[0].AuthorUserID)
				assert.Equal(t, f.a.ID, *comments[0].AuthorUserID)
			}},
		{pendingaction.KindReviewDecision, ticket.StatusReview,
			func(*autoFixture, int64) pendingaction.ProposeParams {
				return pendingaction.ProposeParams{Payload: pendingaction.Payload{Decision: pendingaction.DecisionRequestChanges, Body: "fix it"}}
			},
			func(t *testing.T, f *autoFixture, id int64) {
				assert.NotEqual(t, ticket.StatusReview, f.ticketStatus(t, id), "sent back")
			}},
	}
	for _, c := range cases {
		t.Run(string(c.kind), func(t *testing.T) {
			f := newAutoFixture(t)
			console := f.consoleFor(t, f.a)
			f.turnOn(t, f.a, c.kind)
			id := f.ticketIn(t, c.status)
			f.exec(t, `UPDATE attempts SET pr_url = 'https://github.com/o/r/pull/1', review_started_at = $2 WHERE ticket_id = $1 AND number = 1`, id, f.clock.Now())
			params := c.params(f, id)
			params.AgentID, params.TicketID, params.Kind = console, id, c.kind

			got, err := f.svc.Propose(f.ctx, &params)

			require.NoError(t, err)
			require.True(t, got.AutoApproved(), "state %s: %s", got.State, got.Outcome)
			assert.Equal(t, f.a.ID, *got.DecidedByUserID)
			c.check(t, f, id)
		})
	}
}
