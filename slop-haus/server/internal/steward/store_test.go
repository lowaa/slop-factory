package steward_test

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/agent"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/pendingaction"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/schema"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/steward"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

func TestMain(m *testing.M) { dbtest.Main(m, schema.Migrations) }

var epoch = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

// getByAction reads one proposal's assessment through the production batch read.
func getByAction(r *rig, id uuid.UUID) (*steward.Record, error) {
	got, err := r.store.GetByActions(r.ctx, []uuid.UUID{id})
	if err != nil {
		return nil, err
	}
	if rec, ok := got[id]; ok {
		return rec, nil
	}
	return nil, steward.ErrNotFound
}

type rig struct {
	database *db.DB
	ctx      context.Context
	tenant   uuid.UUID
	tickets  *ticket.Service
	agents   *agent.Service
	pending  *pendingaction.Service
	store    *steward.Store
	console  uuid.UUID
}

func newRig(t *testing.T, database *db.DB) *rig {
	t.Helper()
	tenant := dbtest.NewTenant(t, database)
	tickets := ticket.NewService(ticket.NewPostgresStore(database), slog.DiscardHandler)
	r := &rig{
		database: database, tenant: tenant, ctx: db.WithTenant(context.Background(), tenant),
		tickets: tickets, agents: agent.NewService(agent.NewPostgresStore(database), tickets, nil, slog.DiscardHandler),
		store: steward.NewStore(database),
	}
	r.pending = pendingaction.NewService(pendingaction.NewPostgresStore(database), pendingaction.Config{}, slog.DiscardHandler,
		pendingaction.WithApproval(tickets, liveGrants{}),
		pendingaction.WithDecisionRecorder(steward.NewDecisionRecorder(r.store)))
	console, err := r.agents.CreateKind(r.ctx, "console", agent.KindConsole)
	require.NoError(t, err)
	r.console = console.ID
	return r
}

// proposal files a real pending action, which an assessment is anchored to.
func (r *rig) proposal(t *testing.T) (*pendingaction.Action, int64) {
	t.Helper()
	created, err := r.tickets.Create(r.ctx, &ticket.CreateParams{Title: "a ticket", Status: ticket.StatusOpen})
	require.NoError(t, err)
	_, err = r.database.Exec(r.ctx, `UPDATE tickets SET status = 'preparing' WHERE id = $1`, created.ID)
	require.NoError(t, err)
	a, err := r.pending.Propose(r.ctx, &pendingaction.ProposeParams{
		AgentID: r.console, TicketID: created.ID, Kind: pendingaction.KindReady})
	require.NoError(t, err)
	return a, created.ID
}

func (r *rig) assess(t *testing.T, category string, verdict steward.Verdict, at time.Time) (*steward.Record, *pendingaction.Action) {
	t.Helper()
	a, ticketID := r.proposal(t)
	rev := 3
	rec, err := r.store.Create(r.ctx, &steward.NewRecord{
		TicketID: &ticketID, PendingActionID: a.ID, AgentID: r.console, Mode: steward.ModeShadow,
		TicketRevision: &rev, HeadSHA: "abc", CreatedAt: at,
		Assessment: &steward.Assessment{Category: category, Tier: steward.TierVeto, Verdict: verdict, Reason: "because",
			Evidence: []steward.Evidence{{Check: "ci_green_on_head", OK: true, Ref: "run-1"}}},
	})
	require.NoError(t, err)
	return rec, a
}

func TestAnAssessmentRoundTrips(t *testing.T) {
	r := newRig(t, dbtest.WithDB(t))
	rec, a := r.assess(t, "merge_continue", steward.VerdictWouldVeto, epoch)

	got, err := getByAction(r, a.ID)
	require.NoError(t, err)
	assert.Equal(t, rec.ID, got.ID)
	assert.Equal(t, "merge_continue", got.Category)
	assert.Equal(t, steward.TierVeto, got.Tier)
	assert.Equal(t, steward.VerdictWouldVeto, got.Verdict)
	assert.Equal(t, steward.ModeShadow, got.Mode)
	assert.Equal(t, "because", got.Reason)
	assert.Equal(t, []steward.Evidence{{Check: "ci_green_on_head", OK: true, Ref: "run-1"}}, got.Evidence)
	assert.Equal(t, 3, *got.TicketRevision)
	assert.Equal(t, "abc", *got.HeadSHA)
	assert.Nil(t, got.HumanDecision)

	_, err = getByAction(r, uuid.New())
	assert.ErrorIs(t, err, steward.ErrNotFound)

	_, err = r.store.Create(r.ctx, &steward.NewRecord{PendingActionID: a.ID, AgentID: r.console, Mode: steward.ModeShadow,
		CreatedAt: epoch, Assessment: &steward.Assessment{Category: "x", Tier: steward.TierHuman, Verdict: steward.VerdictNeedsHuman}})
	assert.Error(t, err, "a proposal is assessed once")
}

func TestListFiltersByCategoryAndWindow(t *testing.T) {
	r := newRig(t, dbtest.WithDB(t))
	r.assess(t, "a", steward.VerdictWouldAuto, epoch)
	r.assess(t, "b", steward.VerdictWouldAuto, epoch.Add(time.Hour))
	r.assess(t, "a", steward.VerdictWouldAuto, epoch.Add(2*time.Hour))

	all, err := r.store.List(r.ctx, &steward.ListFilter{})
	require.NoError(t, err)
	assert.Len(t, all, 3)
	assert.Equal(t, epoch.Add(2*time.Hour), all[0].CreatedAt.UTC(), "newest first")

	onlyA, err := r.store.List(r.ctx, &steward.ListFilter{Category: "a"})
	require.NoError(t, err)
	assert.Len(t, onlyA, 2)

	window, err := r.store.List(r.ctx, &steward.ListFilter{Since: epoch.Add(30 * time.Minute), Until: epoch.Add(2 * time.Hour)})
	require.NoError(t, err)
	require.Len(t, window, 1)
	assert.Equal(t, "b", window[0].Category)
}

func TestAssessmentsAreTenantScoped(t *testing.T) {
	database := dbtest.WithDB(t)
	mine, theirs := newRig(t, database), newRig(t, database)
	_, a := mine.assess(t, "a", steward.VerdictWouldAuto, epoch)
	theirs.assess(t, "a", steward.VerdictWouldAuto, epoch)

	_, err := getByAction(theirs, a.ID)
	assert.ErrorIs(t, err, steward.ErrNotFound)
	list, err := mine.store.List(mine.ctx, &steward.ListFilter{})
	require.NoError(t, err)
	assert.Len(t, list, 1)

	changed, err := theirs.store.RecordDecision(theirs.ctx, a.ID, steward.DecisionApproved, epoch)
	require.NoError(t, err)
	assert.False(t, changed, "another tenant cannot decide it")
}

func TestTheOwnersDecisionIsRecordedOnce(t *testing.T) {
	r := newRig(t, dbtest.WithDB(t))
	rec, a := r.assess(t, "a", steward.VerdictWouldAuto, epoch)

	changed, err := r.store.RecordDecision(r.ctx, a.ID, steward.DecisionRejected, epoch.Add(time.Minute))
	require.NoError(t, err)
	assert.True(t, changed)
	changed, err = r.store.RecordDecision(r.ctx, a.ID, steward.DecisionApproved, epoch.Add(time.Hour))
	require.NoError(t, err)
	assert.False(t, changed, "the first decision stands")

	got, err := getByAction(r, a.ID)
	require.NoError(t, err)
	assert.Equal(t, rec.ID, got.ID)
	assert.Equal(t, steward.DecisionRejected, *got.HumanDecision)
	assert.Equal(t, epoch.Add(time.Minute), got.DecidedAt.UTC())
}

type liveGrants struct{}

func (liveGrants) HasFullAccessGrant(context.Context, uuid.UUID, uuid.UUID) (bool, error) {
	return true, nil
}

func TestDecidingAProposalThroughTheServiceRecordsIt(t *testing.T) {
	r := newRig(t, dbtest.WithDB(t))
	r.pending = pendingaction.NewService(pendingaction.NewPostgresStore(r.database), pendingaction.Config{}, slog.DiscardHandler,
		pendingaction.WithApproval(r.tickets, liveGrants{}),
		pendingaction.WithDecisionRecorder(steward.NewDecisionRecorder(r.store)))
	approver := &authn.User{ID: dbtest.NewUser(t, r.database), TenantID: r.tenant, AuthTime: time.Now()}

	_, approved := r.assess(t, "a", steward.VerdictWouldAuto, epoch)
	_, err := r.pending.Approve(r.ctx, approver, approved.ID)
	require.NoError(t, err)
	_, rejected := r.assess(t, "a", steward.VerdictWouldAuto, epoch)
	_, err = r.pending.Reject(r.ctx, approver, rejected.ID, "no")
	require.NoError(t, err)
	_, withdrawn := r.assess(t, "a", steward.VerdictWouldAuto, epoch)
	_, err = r.pending.Withdraw(r.ctx, r.console, withdrawn.ID)
	require.NoError(t, err)

	for _, c := range []struct {
		a    *pendingaction.Action
		want steward.Decision
	}{{approved, steward.DecisionApproved}, {rejected, steward.DecisionRejected}, {withdrawn, steward.DecisionWithdrawn}} {
		got, err := getByAction(r, c.a.ID)
		require.NoError(t, err)
		require.NotNil(t, got.HumanDecision)
		assert.Equal(t, c.want, *got.HumanDecision)
	}
}

func (r *rig) newRecord(t *testing.T, a *pendingaction.Action, ticketID int64, at time.Time) *steward.NewRecord {
	t.Helper()
	return &steward.NewRecord{
		TicketID: &ticketID, PendingActionID: a.ID, AgentID: r.console, Mode: steward.ModeShadow, CreatedAt: at,
		Assessment: &steward.Assessment{Category: "ticket_ready", Tier: steward.TierVeto, Verdict: steward.VerdictWouldVeto},
	}
}

func TestCreateWithinCapCountsOnlyTheWindow(t *testing.T) {
	r := newRig(t, dbtest.WithDB(t))
	r.assess(t, "merge_continue", steward.VerdictWouldAuto, epoch.Add(-25*time.Hour))
	r.assess(t, "merge_continue", steward.VerdictWouldAuto, epoch.Add(-time.Hour))
	since := epoch.Add(-24 * time.Hour)

	a, ticketID := r.proposal(t)
	_, err := r.store.CreateWithinCap(r.ctx, r.newRecord(t, a, ticketID, epoch), since, 1)
	assert.ErrorIs(t, err, steward.ErrDailyCap, "one assessment inside the window already")
	_, err = r.store.CreateWithinCap(r.ctx, r.newRecord(t, a, ticketID, epoch), since, 2)
	require.NoError(t, err)
}

func TestCreateWithinCapAssessesAProposalOnceAndOnlyWhilePending(t *testing.T) {
	r := newRig(t, dbtest.WithDB(t))
	a, ticketID := r.proposal(t)
	_, err := r.store.CreateWithinCap(r.ctx, r.newRecord(t, a, ticketID, epoch), epoch.Add(-time.Hour), 10)
	require.NoError(t, err)
	_, err = r.store.CreateWithinCap(r.ctx, r.newRecord(t, a, ticketID, epoch), epoch.Add(-time.Hour), 10)
	assert.ErrorIs(t, err, steward.ErrAlreadyAssessed)

	decided, ticketID := r.proposal(t)
	_, err = r.pending.Withdraw(r.ctx, r.console, decided.ID)
	require.NoError(t, err)
	_, err = r.store.CreateWithinCap(r.ctx, r.newRecord(t, decided, ticketID, epoch), epoch.Add(-time.Hour), 10)
	assert.ErrorIs(t, err, steward.ErrNotPending)
}

// Concurrent assessments of different proposals must not pass the cap
// together: the count and the insert are one serialised step.
func TestCreateWithinCapHoldsUnderConcurrency(t *testing.T) {
	r := newRig(t, dbtest.WithDB(t))
	const attempts, dailyCap = 6, 2
	var records []*steward.NewRecord
	for range attempts {
		a, ticketID := r.proposal(t)
		records = append(records, r.newRecord(t, a, ticketID, epoch))
	}
	var wg sync.WaitGroup
	errs := make([]error, attempts)
	for i, rec := range records {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = r.store.CreateWithinCap(r.ctx, rec, epoch.Add(-time.Hour), dailyCap)
		}()
	}
	wg.Wait()
	made := 0
	for _, err := range errs {
		if err == nil {
			made++
		} else {
			assert.ErrorIs(t, err, steward.ErrDailyCap)
		}
	}
	assert.Equal(t, dailyCap, made)
}

func TestGetByActionsReadsOnlyTheAssessedProposals(t *testing.T) {
	r := newRig(t, dbtest.WithDB(t))
	_, a := r.assess(t, "a", steward.VerdictWouldAuto, epoch)
	_, b := r.assess(t, "b", steward.VerdictWouldVeto, epoch)
	unassessed, _ := r.proposal(t)

	got, err := r.store.GetByActions(r.ctx, []uuid.UUID{a.ID, b.ID, unassessed.ID})
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, "a", got[a.ID].Category)
	assert.Equal(t, "b", got[b.ID].Category)

	none, err := r.store.GetByActions(r.ctx, nil)
	require.NoError(t, err)
	assert.Empty(t, none)
}

type fixedProposals struct{ facts *steward.ProposalFacts }

func (f fixedProposals) Proposal(context.Context, uuid.UUID) (*steward.ProposalFacts, error) {
	return f.facts, nil
}

type fixedSettings struct{}

func (fixedSettings) StewardConfig(context.Context) (*steward.Config, error) {
	return &steward.Config{Mode: steward.ModeShadow, DailyCap: 10}, nil
}

func TestAssessRefusesACallerThatIsNotALiveSteward(t *testing.T) {
	r := newRig(t, dbtest.WithDB(t))
	// The database only takes a steward that a supervisor minted.
	supervisor := uuid.New()
	_, err := r.database.Exec(r.ctx, `INSERT INTO agents (id, name, kind, slot_cap) VALUES ($1, 'sup', 'supervisor', 1)`, supervisor)
	require.NoError(t, err)
	mint := func(name string) uuid.UUID {
		id := uuid.New()
		_, err := r.database.Exec(r.ctx, `INSERT INTO agents (id, name, kind, steward_minted_by) VALUES ($1, $2, 'steward', $3)`, id, name, supervisor)
		require.NoError(t, err)
		return id
	}
	revoked := mint("revoked steward")
	_, err = r.agents.SetDisabled(r.ctx, revoked, true)
	require.NoError(t, err)
	// One live steward per tenant: the revoked one is archived to make room.
	_, err = r.database.Exec(r.ctx, `UPDATE agents SET archived_at = now() WHERE id = $1`, revoked)
	require.NoError(t, err)
	live := mint("steward")

	ticketID := int64(1)
	svc := func(owner uuid.UUID) *steward.Service {
		facts := &steward.ProposalFacts{ID: uuid.New(), AgentID: owner, TicketID: &ticketID}
		return steward.NewService(slog.DiscardHandler, r.agents, fixedProposals{facts}, fixedSettings{}, r.store,
			steward.NewEngine(slog.DiscardHandler), nil)
	}
	req := &steward.Request{ProposalID: uuid.New(), Category: "x"}

	for name, id := range map[string]uuid.UUID{"a console": r.console, "a disabled steward": revoked} {
		_, err := svc(id).Assess(r.ctx, id, req)
		assert.ErrorIs(t, err, steward.ErrNotSteward, name)
	}
	liveSvc := svc(live)
	_, err = liveSvc.Assess(r.ctx, live, req)
	assert.NotErrorIs(t, err, steward.ErrNotSteward, "a live steward passes the caller check")

	_, err = r.agents.SetDisabled(r.ctx, live, true)
	require.NoError(t, err)
	_, err = liveSvc.Assess(r.ctx, live, req)
	assert.ErrorIs(t, err, steward.ErrNotSteward, "revoked after the service was built")
}

type failingStewards struct{ err error }

func (f failingStewards) IsSteward(context.Context, uuid.UUID) (bool, error) { return false, f.err }

func TestAssessDoesNotProceedWhenTheCallerCannotBeChecked(t *testing.T) {
	boom := errors.New("boom")
	ticketID := int64(1)
	id := uuid.New()
	svc := steward.NewService(slog.DiscardHandler, failingStewards{boom}, fixedProposals{&steward.ProposalFacts{ID: uuid.New(), AgentID: id, TicketID: &ticketID}},
		fixedSettings{}, nil, steward.NewEngine(slog.DiscardHandler), nil)
	_, err := svc.Assess(context.Background(), id, &steward.Request{ProposalID: uuid.New()})
	assert.ErrorIs(t, err, boom)
	assert.NotErrorIs(t, err, steward.ErrNotSteward)
}
