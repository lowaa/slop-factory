package pendingaction_test

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit/audittest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/pendingaction"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// The tenant kind of these tests exists only here: the database's kind CHECK
// is widened for it in the test's own database, as the migration that adds a
// real one does.
const (
	kindWorkspaceNote pendingaction.Kind = "test_workspace_note"
	kindOwnerNote     pendingaction.Kind = "test_owner_note"
	kindNeverAuto     pendingaction.Kind = "test_never_auto"
)

type ownersFake struct {
	mu     sync.Mutex
	owners map[uuid.UUID]bool
	// demoteAfter, when set, demotes the person it names once they have been
	// asked n times: a role lost between two checks of one decision.
	demoteAfter map[uuid.UUID]int
}

func (o *ownersFake) IsTenantOwner(_ context.Context, userID, _ uuid.UUID) (bool, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	owner := o.owners[userID]
	if n, ok := o.demoteAfter[userID]; ok {
		if n--; n <= 0 {
			o.owners[userID] = false
			delete(o.demoteAfter, userID)
		} else {
			o.demoteAfter[userID] = n
		}
	}
	return owner, nil
}

type applierFake struct {
	mu      sync.Mutex
	applied []uuid.UUID
}

func (a *applierFake) Apply(_ context.Context, _ ticket.TransitionTx, _ uuid.UUID, p *pendingaction.Action) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.applied = append(a.applied, p.ID)
	return nil
}

func (a *applierFake) count() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.applied)
}

// ownerResolver says every agent acts for one person.
type ownerResolver struct{ user uuid.UUID }

func (o ownerResolver) ResolveOwner(context.Context, uuid.UUID) (*ticket.OwnerBound, error) {
	return &ticket.OwnerBound{UserID: o.user}, nil
}

// autoSwitch is a person's auto-approve choice for every kind at once.
type autoSwitch struct{ on atomic.Bool }

func (a *autoSwitch) AutoApprove(context.Context, uuid.UUID, pendingaction.Kind) (bool, error) {
	return a.on.Load(), nil
}

type tenantRig struct {
	*decider
	owner, member *authn.User
	applier       *applierFake
	owners        *ownersFake
	events        *audittest.Counter
	auto          *autoSwitch
}

func newTenantRig(t *testing.T) *tenantRig {
	t.Helper()
	app, owner := dbtest.WithOwnerDB(t)
	_, err := owner.Global().Exec(context.Background(), `ALTER TABLE pending_actions DROP CONSTRAINT pending_actions_kind_check`)
	require.NoError(t, err)

	normalise := func(p pendingaction.Payload) (pendingaction.Payload, error) {
		if p.Body == "refused" {
			return pendingaction.Payload{}, fmt.Errorf("%w: the note is refused", pendingaction.ErrInvalid)
		}
		return pendingaction.Payload{Body: "normalised: " + p.Body}, nil
	}
	line := func(a *pendingaction.Action) string { return "Apply " + string(a.Kind) + " to the workspace." }
	tenantKind := pendingaction.KindSpec{Subject: pendingaction.SubjectTenant, Approver: pendingaction.RoleMember,
		AutoApprovable: true, Normalise: normalise, ActionLine: line}
	pendingaction.SetSpecForTest(t, kindWorkspaceNote, tenantKind)
	ownerOnly := tenantKind
	ownerOnly.Approver = pendingaction.RoleOwner
	pendingaction.SetSpecForTest(t, kindOwnerNote, ownerOnly)
	never := tenantKind
	never.AutoApprovable = false
	pendingaction.SetSpecForTest(t, kindNeverAuto, never)

	d := newDeciderOn(t, app)
	r := &tenantRig{decider: d, auto: &autoSwitch{}, applier: &applierFake{}, owners: &ownersFake{owners: map[uuid.UUID]bool{}}}
	r.owner = &authn.User{ID: dbtest.NewUser(t, app), TenantID: d.tenant}
	r.member = d.approver
	r.owners.owners[r.owner.ID] = true
	recorder, counter := audittest.NewRecorder()
	r.events = counter
	opts := []pendingaction.Option{
		pendingaction.WithApproval(d.tickets, d.grants), pendingaction.WithEvents(recorder), pendingaction.WithRoles(r.owners),
		pendingaction.WithAutoApprove(ownerResolver{user: r.owner.ID}, r.auto),
	}
	for _, k := range []pendingaction.Kind{kindWorkspaceNote, kindOwnerNote, kindNeverAuto} {
		opts = append(opts, pendingaction.WithTenantApplier(k, r.applier))
	}
	d.svc = pendingaction.NewService(pendingaction.NewPostgresStore(app), pendingaction.Config{}, slog.DiscardHandler, opts...)
	d.svc.SetNow(d.clock.Now)
	return r
}

func (r *tenantRig) proposeNote(t *testing.T, kind pendingaction.Kind) *pendingaction.Action {
	t.Helper()
	return r.propose(t, &pendingaction.ProposeParams{Kind: kind, Payload: pendingaction.Payload{Body: "note"}})
}

func TestTheDatabaseTiesTheTicketToTheKind(t *testing.T) {
	r := newTenantRig(t)
	ticketID := r.ticketIn(t, ticket.StatusPreparing)
	insert := func(kind string, ticketID *int64) error {
		_, err := r.database.Exec(r.ctx, `
			INSERT INTO pending_actions (id, ticket_id, agent_id, kind, payload, base_status, base_revision, expires_at)
			VALUES ($1, $2, $3, $4, '{}', '', 0, now() + interval '1 day')`, uuid.New(), ticketID, r.console, kind)
		return err
	}
	assert.Error(t, insert("ready", nil), "a ticket kind needs its ticket")
	assert.Error(t, insert(string(kindWorkspaceNote), &ticketID), "a kind about the tenant carries no ticket")
	assert.NoError(t, insert("ready", &ticketID))
	assert.NoError(t, insert(string(kindWorkspaceNote), nil))
}

func TestAnOwnerOnlyKindRefusesAMemberAndLeavesItPending(t *testing.T) {
	r := newTenantRig(t)
	p := r.proposeNote(t, kindOwnerNote)

	_, err := r.svc.Approve(r.ctx, r.member, p.ID)
	require.ErrorIs(t, err, pendingaction.ErrNotApprover)
	_, err = r.svc.Reject(r.ctx, r.member, p.ID, "no")
	require.ErrorIs(t, err, pendingaction.ErrNotApprover)
	assert.Equal(t, pendingaction.StatePending, r.stateOf(t, p).State)
	assert.Zero(t, r.applier.count())

	_, err = r.svc.Approve(r.ctx, r.owner, p.ID)
	require.NoError(t, err)
	assert.Equal(t, pendingaction.StateApproved, r.stateOf(t, p).State)
	assert.Equal(t, 1, r.applier.count())
}

func TestAMemberCanDecideAMemberKindAboutTheTenant(t *testing.T) {
	r := newTenantRig(t)
	p := r.proposeNote(t, kindWorkspaceNote)
	_, err := r.svc.Approve(r.ctx, r.member, p.ID)
	require.NoError(t, err)
	assert.Equal(t, 1, r.applier.count())
}

func TestARoleCheckFailsClosedWithoutARoleSource(t *testing.T) {
	r := newTenantRig(t)
	p := r.proposeNote(t, kindOwnerNote)
	r.svc = pendingaction.NewService(pendingaction.NewPostgresStore(r.database), pendingaction.Config{}, slog.DiscardHandler,
		pendingaction.WithApproval(r.tickets, r.grants), pendingaction.WithTenantApplier(kindOwnerNote, r.applier))
	_, err := r.svc.Approve(r.ctx, r.owner, p.ID)
	require.ErrorIs(t, err, pendingaction.ErrNotApprover)
	assert.Zero(t, r.applier.count())
}

func TestRejectAllByAMemberSkipsWhatOnlyAnOwnerDecides(t *testing.T) {
	r := newTenantRig(t)
	ownerOnly := r.proposeNote(t, kindOwnerNote)
	open := r.proposeNote(t, kindWorkspaceNote)

	rejected, err := r.svc.RejectAll(r.ctx, r.member, nil, "clearing out")
	require.NoError(t, err)
	require.Len(t, rejected, 1)
	assert.Equal(t, open.ID, rejected[0].ID)
	assert.Equal(t, pendingaction.StatePending, r.stateOf(t, ownerOnly).State)

	rejected, err = r.svc.RejectAll(r.ctx, r.owner, nil, "clearing out")
	require.NoError(t, err)
	require.Len(t, rejected, 1)
	assert.Equal(t, ownerOnly.ID, rejected[0].ID)
}

func TestANeverAutoApprovableKindStaysPendingWithEverythingSwitchedOn(t *testing.T) {
	r := newTenantRig(t)
	r.auto.on.Store(true)

	auto := r.proposeNote(t, kindWorkspaceNote)
	assert.Equal(t, pendingaction.StateApproved, auto.State, "control: the same setup applies an auto-approvable kind")
	assert.True(t, auto.AutoApproved())
	assert.Equal(t, 1, r.applier.count())

	never := r.proposeNote(t, kindNeverAuto)
	assert.Equal(t, pendingaction.StatePending, never.State)
	assert.Equal(t, 1, r.applier.count())
	_, err := r.svc.Approve(r.ctx, r.member, never.ID)
	require.NoError(t, err, "a click still approves it")
}

func TestAutoApproveIsRefusedForAnOwnerKindWhoseOwnerIsNotOne(t *testing.T) {
	r := newTenantRig(t)
	r.auto.on.Store(true)
	pendingaction.SetSpecForTest(t, kindWorkspaceNote, pendingaction.KindSpec{
		Subject: pendingaction.SubjectTenant, Approver: pendingaction.RoleOwner, AutoApprovable: true,
		Normalise: func(p pendingaction.Payload) (pendingaction.Payload, error) { return p, nil },
	})
	r.owners.owners[r.owner.ID] = false
	p := r.proposeNote(t, kindWorkspaceNote)
	assert.Equal(t, pendingaction.StatePending, p.State, "the role applies to an auto-approval as to a click")
	assert.Zero(t, r.applier.count())
}

func TestATenantProposalAndATicketAreNotInterchangeable(t *testing.T) {
	r := newTenantRig(t)
	ticketID := r.ticketIn(t, ticket.StatusPreparing)
	r.console = r.newAgent(t, "console-two", true)

	_, err := r.svc.Propose(r.ctx, &pendingaction.ProposeParams{AgentID: r.console, Kind: kindWorkspaceNote, TicketID: ticketID,
		Payload: pendingaction.Payload{Body: "x"}})
	require.ErrorIs(t, err, pendingaction.ErrInvalid)
	_, err = r.svc.Propose(r.ctx, &pendingaction.ProposeParams{AgentID: r.console, Kind: pendingaction.KindReady})
	require.ErrorIs(t, err, pendingaction.ErrInvalid)
}

func TestATenantKindWithoutAPayloadRuleCannotBeProposed(t *testing.T) {
	r := newTenantRig(t)
	pendingaction.SetSpecForTest(t, kindWorkspaceNote, pendingaction.KindSpec{
		Subject: pendingaction.SubjectTenant, Approver: pendingaction.RoleMember, AutoApprovable: true})
	_, err := r.svc.Propose(r.ctx, &pendingaction.ProposeParams{AgentID: r.console, Kind: kindWorkspaceNote})
	require.ErrorIs(t, err, pendingaction.ErrInvalid)
}

func TestANewerTenantProposalOfAKindSupersedesTheOlder(t *testing.T) {
	r := newTenantRig(t)
	first := r.proposeNote(t, kindNeverAuto)
	second := r.proposeNote(t, kindNeverAuto)
	assert.Equal(t, pendingaction.StateWithdrawn, r.stateOf(t, first).State)
	assert.Equal(t, pendingaction.StatePending, r.stateOf(t, second).State)
	other := r.proposeNote(t, kindOwnerNote)
	assert.Equal(t, pendingaction.StatePending, r.stateOf(t, other).State, "another kind is its own slot")
}

// Every read, sweep and edit of a proposal that once assumed a ticket.
func TestEveryQueryAnswersForATicketlessProposal(t *testing.T) {
	r := newTenantRig(t)
	q := r.proposeNote(t, kindOwnerNote)
	r.proposeNote(t, kindNeverAuto)

	t.Run("get and list", func(t *testing.T) {
		got, err := r.svc.Get(r.ctx, q.ID)
		require.NoError(t, err)
		assert.Nil(t, got.TicketID)
		listed, err := r.svc.List(r.ctx, &pendingaction.ListFilter{States: []pendingaction.State{pendingaction.StatePending}})
		require.NoError(t, err)
		assert.Len(t, listed, 2)
	})
	t.Run("views describe it without a ticket", func(t *testing.T) {
		v, err := r.svc.ViewOf(r.ctx, testPerson(), q.ID)
		require.NoError(t, err)
		assert.Equal(t, "console-one", v.AgentName)
		assert.Empty(t, v.TicketTitle)
		assert.Empty(t, v.StaleReason)
		assert.Equal(t, "Apply "+string(kindOwnerNote)+" to the workspace.", v.ActionLine)
		page, err := r.svc.ViewsPage(r.ctx, testPerson(), &pendingaction.ListFilter{States: []pendingaction.State{pendingaction.StatePending}, Limit: 1})
		require.NoError(t, err)
		require.Len(t, page.Views, 1)
		require.NotNil(t, page.Next)
		next, err := r.svc.ViewsPage(r.ctx, testPerson(), &pendingaction.ListFilter{States: []pendingaction.State{pendingaction.StatePending}, Limit: 1, After: page.Next})
		require.NoError(t, err)
		assert.Len(t, next.Views, 1)
	})
	t.Run("the ticket filter and a ticket id match no ticketless proposal", func(t *testing.T) {
		ticketID := r.ticketIn(t, ticket.StatusOpen)
		listed, err := r.svc.List(r.ctx, &pendingaction.ListFilter{TicketID: &ticketID})
		require.NoError(t, err)
		assert.Empty(t, listed)
		listed, err = r.svc.List(r.ctx, &pendingaction.ListFilter{Ticket: &ticket.ListFilter{}})
		require.NoError(t, err)
		assert.Empty(t, listed, "a proposal filtered by its ticket has to have one")
	})
	t.Run("withdraw", func(t *testing.T) {
		got, err := r.svc.Withdraw(r.ctx, r.console, q.ID)
		require.NoError(t, err)
		assert.Equal(t, pendingaction.StateWithdrawn, got.State)
	})
	t.Run("void and expire", func(t *testing.T) {
		a := r.proposeNote(t, kindOwnerNote)
		n, err := r.svc.VoidAgent(r.ctx, r.console, "gone")
		require.NoError(t, err)
		assert.Positive(t, n)
		assert.Equal(t, pendingaction.StateVoided, r.stateOf(t, a).State)

		b := r.proposeNote(t, kindOwnerNote)
		r.clock.Advance(pendingaction.DefaultTTL + 1)
		n, err = r.svc.ExpireDue(r.ctx)
		require.NoError(t, err)
		assert.Positive(t, n)
		assert.Equal(t, pendingaction.StateExpired, r.stateOf(t, b).State)
	})
}

func TestAnOwnerDemotedWhileTheApprovalWaitsForItsLocksDoesNotApprove(t *testing.T) {
	r := newTenantRig(t)
	p := r.proposeNote(t, kindOwnerNote)
	r.owners.demoteAfter = map[uuid.UUID]int{r.owner.ID: 1}

	_, err := r.svc.Approve(r.ctx, r.owner, p.ID)
	require.ErrorIs(t, err, pendingaction.ErrNotApprover)
	assert.Equal(t, pendingaction.StatePending, r.stateOf(t, p).State)
	assert.Zero(t, r.applier.count())
}

func TestRejectAllByAMemberLeavesAKindTheTableForgot(t *testing.T) {
	r := newTenantRig(t)
	p := r.proposeNote(t, kindWorkspaceNote)
	pendingaction.ForgetKindForTest(t, kindWorkspaceNote)

	rejected, err := r.svc.RejectAll(r.ctx, r.member, nil, "clearing out")
	require.NoError(t, err)
	assert.Empty(t, rejected)
	assert.Equal(t, pendingaction.StatePending, r.stateOf(t, p).State, "an unknown kind is owner-only, as it is for one decision")
}

func TestATenantKindsPayloadIsTheNormalisedOne(t *testing.T) {
	r := newTenantRig(t)
	p := r.proposeNote(t, kindNeverAuto)
	assert.Equal(t, "normalised: note", p.Payload.Body)
	assert.Equal(t, "normalised: note", r.stateOf(t, p).Payload.Body, "the stored payload, not the proposer's")

	_, err := r.svc.Propose(r.ctx, &pendingaction.ProposeParams{AgentID: r.console, Kind: kindNeverAuto,
		Payload: pendingaction.Payload{Body: "refused"}})
	require.ErrorIs(t, err, pendingaction.ErrInvalid)
}

func TestConcurrentProposalsOfOneTenantKindLeaveExactlyOnePending(t *testing.T) {
	r := newTenantRig(t)
	agents := []uuid.UUID{r.console, r.newAgent(t, "console-two", true), r.newAgent(t, "console-three", true), r.newAgent(t, "console-four", true)}
	start := make(chan struct{})
	errs := make(chan error, len(agents))
	for _, id := range agents {
		go func() {
			<-start
			_, err := r.svc.Propose(r.ctx, &pendingaction.ProposeParams{AgentID: id, Kind: kindNeverAuto, Payload: pendingaction.Payload{Body: "note"}})
			errs <- err
		}()
	}
	close(start)
	for range agents {
		require.NoError(t, <-errs)
	}
	pending, err := r.svc.List(r.ctx, &pendingaction.ListFilter{States: []pendingaction.State{pendingaction.StatePending}, Kind: ptrTo(kindNeverAuto)})
	require.NoError(t, err)
	assert.Len(t, pending, 1)
}

func ptrTo[T any](v T) *T { return &v }

func TestTheAutoApprovableKindsAreTheOnesTheTableSays(t *testing.T) {
	assert.ElementsMatch(t, []pendingaction.Kind{pendingaction.KindDiscussionAnswer, pendingaction.KindReady, pendingaction.KindStatus,
		pendingaction.KindReviewDecision, pendingaction.KindEdit}, pendingaction.AutoApprovableKinds())
	newTenantRig(t)
	assert.NotContains(t, pendingaction.AutoApprovableKinds(), kindNeverAuto)
	assert.Contains(t, pendingaction.AutoApprovableKinds(), kindWorkspaceNote)
}

func TestAWorkspaceProposalIsOnTheWireWithoutATicket(t *testing.T) {
	r := newTenantRig(t)
	p := r.proposeNote(t, kindOwnerNote)
	v, err := r.svc.ViewOf(r.ctx, testPerson(), p.ID)
	require.NoError(t, err)

	raw, err := json.Marshal(pendingaction.ToBody(v))
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, json.Unmarshal(raw, &got))
	assert.Contains(t, got, "ticket_id")
	assert.Nil(t, got["ticket_id"])
	assert.NotContains(t, got, "ticket_status")
	assert.Equal(t, "tenant", got["subject"])
	assert.Equal(t, "owner", got["approver_role"])
}
