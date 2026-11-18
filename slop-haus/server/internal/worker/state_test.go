package worker_test

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-review/report"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/change"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/reviewreport"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/worker"
)

// fakeChanges answers with what the test gives it and records who it was asked
// about. Whether checks pass is the code host's and change.Service's; this
// layer only carries the answer.
type fakeChanges struct {
	report  *change.Report
	err     error
	asked   []uuid.UUID
	tenants []uuid.UUID
}

func (f *fakeChanges) Checks(_ context.Context, tenant, agentID uuid.UUID, _ string) (*change.Report, error) {
	f.asked = append(f.asked, agentID)
	f.tenants = append(f.tenants, tenant)
	return f.report, f.err
}

type rig struct {
	tickets *ticket.Service
	changes *fakeChanges
	svc     *worker.Service
	ctx     context.Context
	tenant  uuid.UUID
	db      *db.DB
}

func newRig(t *testing.T) *rig {
	t.Helper()
	database := dbtest.WithDB(t)
	tenant := dbtest.NewTenant(t, database)
	svc := ticket.NewService(ticket.NewPostgresStore(database), slog.DiscardHandler)
	changes := &fakeChanges{err: change.ErrNoChange}
	return &rig{
		tickets: svc, changes: changes, svc: worker.NewService(svc, changes, nil, slog.DiscardHandler),
		ctx: db.WithTenant(context.Background(), tenant), tenant: tenant, db: database,
	}
}

func (r *rig) agent(t *testing.T) uuid.UUID {
	t.Helper()
	id := uuid.New()
	_, err := r.db.Exec(r.ctx, `INSERT INTO agents (id, name) VALUES ($1, $2)`, id, "agent-"+id.String())
	require.NoError(t, err)
	return id
}

// claim files a ticket on its own scope and has the agent claim it.
func (r *rig) claim(t *testing.T, agentID uuid.UUID, scope string) *ticket.Ticket {
	t.Helper()
	_, err := r.tickets.Create(r.ctx, &ticket.CreateParams{Title: "Work on " + scope, Paths: []model.Path{{Path: scope}}})
	require.NoError(t, err)
	got, err := r.tickets.Claim(r.ctx, agentID, nil)
	require.NoError(t, err)
	require.NotNil(t, got.Ticket)
	return got.Ticket
}

func (r *rig) state(t *testing.T, agentID uuid.UUID) *worker.State {
	t.Helper()
	got, err := r.svc.State(r.ctx, r.tenant, agentID)
	require.NoError(t, err)
	return got
}

func TestState_AnAgentThatNeverHeldALeaseHasNothing(t *testing.T) {
	r := newRig(t)

	got := r.state(t, r.agent(t))

	assert.Equal(t, &worker.State{}, got)
	assert.Empty(t, r.changes.asked, "no lease, no change to read")
}

func TestState_AHolderHasItsLeaseTicketAndAttempt(t *testing.T) {
	r := newRig(t)
	a := r.agent(t)
	held := r.claim(t, a, "server/")

	got := r.state(t, a)

	require.NotNil(t, got.Lease)
	assert.Equal(t, held.ID, got.Lease.TicketID)
	assert.Equal(t, 1, got.Lease.Attempt)
	assert.Equal(t, &worker.TicketState{ID: held.ID, Status: ticket.StatusInProgress, Paths: []string{"server/"}}, got.Ticket)
	assert.Equal(t, &worker.AttemptState{Number: 1, Status: "in_progress", Promoted: true}, got.Attempt)
	assert.Nil(t, got.Ended)
	assert.Equal(t, worker.LockPlace{}, got.Lock)
	assert.Nil(t, got.Checks, "no change is open")
}

// The supervisor reads this after the run exited: the lease is gone and what
// the run left the ticket as is how it knows what to do next.
func TestState_AfterTheLeaseEndsTheTicketAndHowItEndedRemain(t *testing.T) {
	for name, tc := range map[string]struct {
		leave  func(r *rig, a uuid.UUID) error
		status ticket.Status
		exit   ticket.LeaseExit
	}{
		"discussion": {status: ticket.StatusDiscussion, exit: ticket.ExitDiscussion, leave: func(r *rig, a uuid.UUID) error {
			_, err := r.tickets.Discuss(r.ctx, a, "Which?", "Half done.")
			return err
		}},
		"preparing": {status: ticket.StatusPreparing, exit: ticket.ExitPreparing, leave: func(r *rig, a uuid.UUID) error {
			_, err := r.tickets.Prepare(r.ctx, a, "Not ready.")
			return err
		}},
		"done": {status: ticket.StatusDone, exit: ticket.ExitDone, leave: func(r *rig, a uuid.UUID) error {
			_, err := r.tickets.Done(r.ctx, a, &ticket.DoneParams{Summary: "Done."})
			return err
		}},
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t)
			a := r.agent(t)
			held := r.claim(t, a, "server/")
			require.NoError(t, tc.leave(r, a))

			got := r.state(t, a)

			assert.Nil(t, got.Lease)
			assert.Equal(t, &worker.TicketState{ID: held.ID, Status: tc.status, Paths: []string{"server/"}}, got.Ticket)
			assert.Equal(t, &worker.EndState{Kind: ticket.LeaseFinished, Exit: tc.exit}, got.Ended)
			assert.Equal(t, worker.LockPlace{}, got.Lock)
			assert.Empty(t, r.changes.asked, "a lease that is gone has no change to read")
		})
	}
}

func TestState_ALeaseAHumanTookBackIsRevoked(t *testing.T) {
	r := newRig(t)
	a := r.agent(t)
	held := r.claim(t, a, "server/")
	_, err := r.tickets.MoveMany(r.ctx, []int64{held.ID}, ticket.StatusPreparing)
	require.NoError(t, err)

	got := r.state(t, a)

	assert.Nil(t, got.Lease)
	assert.Equal(t, &worker.EndState{Kind: ticket.LeaseRevoked, Status: ticket.StatusPreparing}, got.Ended)
}

func TestState_ALeaseThatRanOutIsLapsed(t *testing.T) {
	r := newRig(t)
	a := r.agent(t)
	r.claim(t, a, "server/")
	_, err := r.db.Exec(r.ctx, `UPDATE leases SET expires_at = now() - interval '1 minute' WHERE agent_id = $1`, a)
	require.NoError(t, err)

	got := r.state(t, a)

	assert.Nil(t, got.Lease)
	require.NotNil(t, got.Ended)
	assert.Equal(t, ticket.LeaseLapsed, got.Ended.Kind)
}

func TestState_LockPlace(t *testing.T) {
	r := newRig(t)
	holder, second, third := r.agent(t), r.agent(t), r.agent(t)
	r.claim(t, holder, "a/")
	r.claim(t, second, "b/")
	r.claim(t, third, "c/")
	require.NoError(t, r.tickets.AcquireIntegrationLock(r.ctx, holder))
	require.ErrorIs(t, r.tickets.AcquireIntegrationLock(r.ctx, second), ticket.ErrLockHeld)
	require.ErrorIs(t, r.tickets.AcquireIntegrationLock(r.ctx, third), ticket.ErrLockHeld)

	assert.Equal(t, worker.LockPlace{Holds: true}, r.state(t, holder).Lock)
	assert.Equal(t, worker.LockPlace{Position: 1}, r.state(t, second).Lock, "first in line, but the lock is held: no offer yet")
	assert.Equal(t, worker.LockPlace{Position: 2}, r.state(t, third).Lock)

	require.NoError(t, r.tickets.ReleaseIntegrationLock(r.ctx, holder))

	assert.Equal(t, worker.LockPlace{Position: 1, Offered: true}, r.state(t, second).Lock, "the lock came free for the head of the queue")
	assert.Equal(t, worker.LockPlace{Position: 2}, r.state(t, third).Lock, "the one behind is not offered it")
}

func TestState_ReadingJoinsNoQueueAndTakesNoLock(t *testing.T) {
	r := newRig(t)
	a := r.agent(t)
	r.claim(t, a, "server/")

	r.state(t, a)
	r.state(t, a)

	lock, err := r.tickets.IntegrationLockState(r.ctx)
	require.NoError(t, err)
	assert.Nil(t, lock.Holder)
	assert.Empty(t, lock.Queue)
}

func TestState_ChecksOfTheHeldAttemptsChange(t *testing.T) {
	r := newRig(t)
	a := r.agent(t)
	r.claim(t, a, "server/")
	r.changes.err = nil
	r.changes.report = &change.Report{URL: "https://example.test/pr/1", HeadSHA: "abc123", State: "pending"}

	got := r.state(t, a)

	assert.Equal(t, &worker.ChecksState{URL: "https://example.test/pr/1", HeadSHA: "abc123", State: "pending"}, got.Checks)
	assert.Equal(t, []uuid.UUID{a}, r.changes.asked)
	assert.Equal(t, []uuid.UUID{r.tenant}, r.changes.tenants, "the checks are read with the caller's tenant's code host credentials")
}

func TestState_ChecksThatPassedSayPassed(t *testing.T) {
	r := newRig(t)
	a := r.agent(t)
	r.claim(t, a, "server/")
	r.changes.err = nil
	r.changes.report = &change.Report{HeadSHA: "abc123", State: "success", Passed: true}

	assert.True(t, r.state(t, a).Checks.Passed)
}

// A code host that is down for a minute must not make the lock queue and the
// lease unreadable: the supervisor still needs those to decide.
func TestState_ChecksTheHostCouldNotAnswerAreUnknownAndTheRestIsStillReported(t *testing.T) {
	r := newRig(t)
	a := r.agent(t)
	held := r.claim(t, a, "server/")
	r.changes.err = errors.New("github: 502")

	got := r.state(t, a)

	require.NotNil(t, got.Checks)
	assert.Equal(t, worker.ChecksUnknown, got.Checks.State)
	assert.False(t, got.Checks.Passed)
	assert.Contains(t, got.Checks.Error, "502")
	assert.Equal(t, []uuid.UUID{r.tenant}, r.changes.tenants)
	require.NotNil(t, got.Lease)
	assert.Equal(t, held.ID, got.Lease.TicketID)
}

func TestState_WithoutCodeHostsThereAreNoChecks(t *testing.T) {
	r := newRig(t)
	svc := worker.NewService(r.tickets, nil, nil, slog.DiscardHandler)
	a := r.agent(t)
	r.claim(t, a, "server/")

	got, err := svc.State(r.ctx, r.tenant, a)

	require.NoError(t, err)
	assert.Nil(t, got.Checks)
}

// scriptedTickets answers WorkerSnapshot from a script, one entry per call and
// the last one thereafter: a claim or release landing between the reads of one
// state, which a real database cannot be made to do on cue. Everything else is
// an ended lease and a free lock, which decide nothing here.
type scriptedTickets struct {
	snapshots []*ticket.WorkerSnapshot
	calls     int
}

func (s *scriptedTickets) WorkerSnapshot(context.Context, uuid.UUID) (*ticket.WorkerSnapshot, error) {
	next := s.snapshots[min(s.calls, len(s.snapshots)-1)]
	s.calls++
	if next == nil {
		return nil, ticket.ErrNoLease
	}
	return next, nil
}

func (s *scriptedTickets) Get(_ context.Context, id int64) (*ticket.Ticket, error) {
	return &ticket.Ticket{ID: id}, nil
}

func (s *scriptedTickets) LeaseEnd(context.Context, int64) (*ticket.LeaseEnd, error) {
	return &ticket.LeaseEnd{Kind: ticket.LeaseFinished, Exit: ticket.ExitReview}, nil
}

func (s *scriptedTickets) IntegrationLockStateFor(context.Context, uuid.UUID) (*ticket.LockState, error) {
	return &ticket.LockState{}, nil
}

func leaseSnap(id int64, active bool) *ticket.WorkerSnapshot {
	return &ticket.WorkerSnapshot{LeaseID: id, TicketID: 10 + id, Attempt: 1, Active: active, TicketStatus: ticket.StatusInProgress}
}

func TestState_AClaimLandingMidReadIsReadAgainNotReportedAsNoLease(t *testing.T) {
	tickets := &scriptedTickets{snapshots: []*ticket.WorkerSnapshot{
		leaseSnap(1, false), // the state begins on the ended lease...
		leaseSnap(2, true),  // ...and the agent claims another before it is done
	}}
	svc := worker.NewService(tickets, nil, nil, slog.DiscardHandler)

	got, err := svc.State(context.Background(), uuid.New(), uuid.New())

	require.NoError(t, err)
	require.NotNil(t, got.Lease, "the state is of the lease the agent holds now")
	assert.Equal(t, int64(2), got.Lease.ID)
	assert.Nil(t, got.Ended)
}

func TestState_ALeaseThatKeepsChangingIsReadAFewTimesNotForever(t *testing.T) {
	var churn []*ticket.WorkerSnapshot
	for id := int64(1); id <= 20; id++ {
		churn = append(churn, leaseSnap(id, true))
	}
	tickets := &scriptedTickets{snapshots: churn}
	svc := worker.NewService(tickets, nil, nil, slog.DiscardHandler)

	got, err := svc.State(context.Background(), uuid.New(), uuid.New())

	require.NoError(t, err)
	assert.NotNil(t, got.Lease)
	assert.Equal(t, 6, tickets.calls, "three reads of the lease at either end, then it gives up")
}

func TestState_TheAttemptCarriesTheSessionARunRecorded(t *testing.T) {
	r := newRig(t)
	a := r.agent(t)
	r.claim(t, a, "server/")
	require.NoError(t, r.tickets.RecordSession(r.ctx, a, &ticket.AttemptSession{Harness: "claude", SessionID: "sess-1"}))

	got := r.state(t, a)

	assert.Equal(t, &ticket.AttemptSession{Harness: "claude", SessionID: "sess-1"}, got.Attempt.Session)
}

// A ticket handed to review can be closed as done without the agent (the
// server merges an approved change). The lease still ended as a review, but
// the supervisor must read the run as finished, not as a release to re-claim.
func TestState_AReviewedTicketClosedByTheServerReadsAsDone(t *testing.T) {
	r := newRig(t)
	a := r.agent(t)
	held := r.claim(t, a, "server/")
	_, err := r.tickets.Review(r.ctx, a, &ticket.ReviewParams{
		Reason: "reviewed by a person", PRURL: "https://github.com/acme/repo/pull/1", Handoff: "PR open.",
	})
	require.NoError(t, err)
	_, err = r.tickets.MoveMany(r.ctx, []int64{held.ID}, ticket.StatusDone)
	require.NoError(t, err)

	got := r.state(t, a)

	assert.Equal(t, &worker.TicketState{ID: held.ID, Status: ticket.StatusDone, Paths: []string{"server/"}}, got.Ticket)
	assert.Equal(t, &worker.EndState{Kind: ticket.LeaseFinished, Exit: ticket.ExitDone}, got.Ended)
}

// failingReviews answers every read with its error.
type failingReviews struct{ err error }

func (f failingReviews) OpenRequest(context.Context, uuid.UUID, int64, int) (*reviewreport.Request, error) {
	return nil, f.err
}

// A state that said "no request" when the request could not be read would
// leave the run waiting on a review its supervisor never starts; an ended
// lease has no request to read.
func TestState_AnUnreadableReviewRequestFailsTheReadAndAnEndedLeaseHasNone(t *testing.T) {
	r := newRig(t)
	agentID := r.agent(t)
	r.claim(t, agentID, "server/")
	_, err := worker.NewService(r.tickets, r.changes, failingReviews{errors.New("db down")}, slog.DiscardHandler).State(r.ctx, r.tenant, agentID)
	assert.Error(t, err)

	_, err = r.db.Exec(r.ctx, `UPDATE leases SET released_at = now(), release_reason = 'done' WHERE agent_id = $1`, agentID)
	require.NoError(t, err)
	got, err := worker.NewService(r.tickets, r.changes, failingReviews{errors.New("not read")}, slog.DiscardHandler).State(r.ctx, r.tenant, agentID)
	require.NoError(t, err, "an ended lease reads no request")
	assert.Nil(t, got.ReviewRequest)
}

// The review request is read for the held attempt, and goes once the
// supervisor's runs of its head cover every persona.
func TestState_HasTheOpenReviewRequestUntilTheSupervisorsRunsCloseIt(t *testing.T) {
	r := newRig(t)
	sup, slot := uuid.New(), uuid.New()
	_, err := r.db.Exec(r.ctx, `INSERT INTO agents (id, name, kind, slot_cap) VALUES ($1, 'sup', 'supervisor', 4)`, sup)
	require.NoError(t, err)
	_, err = r.db.Exec(r.ctx, `INSERT INTO agents (id, name, kind, supervisor_agent_id) VALUES ($1, 'slot', 'worker', $2)`, slot, sup)
	require.NoError(t, err)
	held := r.claim(t, slot, "server/")
	reviews := reviewreport.NewPostgresStore(r.db)
	svc := worker.NewService(r.tickets, r.changes, reviews, slog.DiscardHandler)
	state := func() *worker.State {
		got, err := svc.State(r.ctx, r.tenant, slot)
		require.NoError(t, err)
		return got
	}
	assert.Nil(t, state().ReviewRequest)

	const head = "89abcdef0123456789abcdef0123456789abcdef"
	_, err = reviews.RequestReview(r.ctx, slot, head, "Spin the widget.")
	require.NoError(t, err)
	got := state().ReviewRequest
	require.NotNil(t, got)
	assert.Equal(t, head, got.HeadSHA)
	assert.Equal(t, "Spin the widget.", got.Intent)

	at := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	post := func(personas ...string) {
		t.Helper()
		_, err := reviews.InsertRun(r.ctx, &reviewreport.Poster{Kind: reviewreport.PostedBySupervisor, ID: sup}, &report.Run{
			ID: uuid.New(), Kind: report.KindReview, Branch: ticket.BranchName(held.ID, 1), HeadSHA: head,
			Personas: personas, Verdict: "approve", StartedAt: at, FinishedAt: at,
		})
		require.NoError(t, err)
	}
	post("security")
	require.NotNil(t, state().ReviewRequest, "a run of a persona that is not required leaves it open")
	post(reviewreport.RequiredPersonas...)
	assert.Nil(t, state().ReviewRequest, "the supervisor's run closed it")
}
