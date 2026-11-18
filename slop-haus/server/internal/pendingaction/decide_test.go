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

	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit/audittest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/pendingaction"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// grantsFake says what the test tells it: whether an agent has a live
// full-access grant is OAuth's to decide, which these tests are not about.
type grantsFake struct {
	mu   sync.Mutex
	gone map[uuid.UUID]bool
}

func (g *grantsFake) HasFullAccessGrant(_ context.Context, _, agentID uuid.UUID) (bool, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return !g.gone[agentID], nil
}

func (g *grantsFake) revoke(id uuid.UUID) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.gone == nil {
		g.gone = map[uuid.UUID]bool{}
	}
	g.gone[id] = true
}

type decider struct {
	*fixture
	svc      *pendingaction.Service
	grants   *grantsFake
	events   *audittest.Counter
	approver *authn.User
	console  uuid.UUID
}

func newDecider(t *testing.T) *decider {
	t.Helper()
	return newDeciderOn(t, dbtest.WithDB(t))
}

func newDeciderOn(t *testing.T, database *db.DB) *decider {
	t.Helper()
	f := newFixtureOn(t, pendingaction.Config{}, database)
	d := &decider{fixture: f, grants: &grantsFake{}}
	recorder, counter := audittest.NewRecorder()
	d.events = counter
	d.svc = pendingaction.NewService(pendingaction.NewPostgresStore(f.database), pendingaction.Config{}, slog.DiscardHandler,
		pendingaction.WithApproval(f.tickets, d.grants),
		pendingaction.WithEvents(recorder))
	d.svc.SetNow(f.clock.Now)
	d.approver = &authn.User{ID: dbtest.NewUser(t, f.database), TenantID: f.tenant, AuthTime: f.clock.Now()}
	d.console = f.newAgent(t, "console-one", true)
	return d
}

func (d *decider) propose(t *testing.T, params *pendingaction.ProposeParams) *pendingaction.Action {
	t.Helper()
	params.AgentID = d.console
	a, err := d.svc.Propose(d.ctx, params)
	require.NoError(t, err)
	return a
}

func (d *decider) proposeIn(t *testing.T, status ticket.Status, kind pendingaction.Kind, payload pendingaction.Payload) *pendingaction.Action {
	t.Helper()
	return d.propose(t, &pendingaction.ProposeParams{TicketID: d.ticketIn(t, status), Kind: kind, Payload: payload})
}

func (d *decider) stateOf(t *testing.T, a *pendingaction.Action) *pendingaction.Action {
	t.Helper()
	got, err := d.svc.Get(d.ctx, a.ID)
	require.NoError(t, err)
	return got
}

type statusChange struct{ From, To, Actor, Cause string }

func (d *decider) lastChange(t *testing.T, ticketID int64) (statusChange, *uuid.UUID) {
	t.Helper()
	var c statusChange
	var who *uuid.UUID
	require.NoError(t, d.database.QueryRow(d.ctx, `
		SELECT COALESCE(from_status, ''), to_status, actor_kind, actor_id, COALESCE(cause, '')
		  FROM ticket_status_changes WHERE ticket_id = $1 ORDER BY id DESC LIMIT 1`, ticketID).
		Scan(&c.From, &c.To, &c.Actor, &who, &c.Cause))
	return c, who
}

func (d *decider) comments(t *testing.T, ticketID int64) []ticket.Comment {
	t.Helper()
	got, err := d.tickets.ListComments(d.ctx, ticketID)
	require.NoError(t, err)
	return got
}

func TestApprovingADiscussionAnswerPostsItAsTheApproverAndRequeues(t *testing.T) {
	d := newDecider(t)
	p := d.proposeIn(t, ticket.StatusDiscussion, pendingaction.KindDiscussionAnswer, pendingaction.Payload{Body: "use option B"})

	settled, err := d.svc.Approve(d.ctx, d.approver, p.ID)
	require.NoError(t, err)
	assert.Equal(t, pendingaction.StateApproved, settled.State)
	require.NotNil(t, settled.DecidedByUserID)
	assert.Equal(t, d.approver.ID, *settled.DecidedByUserID)

	assert.Equal(t, ticket.StatusOpen, d.ticketStatus(t, *p.TicketID), "re-queued, as the person's own answer does")
	comments := d.comments(t, *p.TicketID)
	require.Len(t, comments, 1)
	require.NotNil(t, comments[0].AuthorUserID, "a human comment, not the console's")
	assert.Equal(t, d.approver.ID, *comments[0].AuthorUserID)
	assert.Nil(t, comments[0].AuthorAgentID)
	assert.True(t, strings.HasPrefix(comments[0].Body, "use option B"))
	assert.Contains(t, comments[0].Body, "Drafted by console-one")

	change, who := d.lastChange(t, *p.TicketID)
	assert.Equal(t, "user", change.Actor)
	require.NotNil(t, who)
	assert.Equal(t, d.approver.ID, *who)
	assert.Equal(t, "proposal:"+p.ID.String(), change.Cause)
	assert.Equal(t, 1, d.events.Count(audit.ProposalApproved))
}

func TestApprovingReadyMovesAPreparingTicketIntoTheQueue(t *testing.T) {
	d := newDecider(t)
	p := d.proposeIn(t, ticket.StatusPreparing, pendingaction.KindReady, pendingaction.Payload{})

	_, err := d.svc.Approve(d.ctx, d.approver, p.ID)
	require.NoError(t, err)
	assert.Equal(t, ticket.StatusOpen, d.ticketStatus(t, *p.TicketID))
	change, _ := d.lastChange(t, *p.TicketID)
	assert.Equal(t, "proposal:"+p.ID.String(), change.Cause)
}

func TestApprovingAStatusProposalMovesItWithTheReasonAsAComment(t *testing.T) {
	for _, to := range []ticket.Status{ticket.StatusParked, ticket.StatusCancelled} {
		t.Run(string(to), func(t *testing.T) {
			d := newDecider(t)
			p := d.proposeIn(t, ticket.StatusOpen, pendingaction.KindStatus, pendingaction.Payload{Status: to, Body: "superseded"})

			_, err := d.svc.Approve(d.ctx, d.approver, p.ID)
			require.NoError(t, err)
			assert.Equal(t, to, d.ticketStatus(t, *p.TicketID))
			comments := d.comments(t, *p.TicketID)
			require.Len(t, comments, 1)
			assert.Contains(t, comments[0].Body, "superseded")
			assert.Contains(t, comments[0].Body, "Drafted by console-one")
		})
	}
}

// reviewTicket is a ticket in review as a real one is left by an agent.
func (d *decider) reviewTicket(t *testing.T) int64 {
	t.Helper()
	id := d.ticketIn(t, ticket.StatusReview)
	d.exec(t, `UPDATE attempts SET pr_url = 'https://github.com/o/r/pull/1', review_started_at = $2 WHERE ticket_id = $1 AND number = 1`,
		id, d.clock.Now())
	return id
}

func TestApprovingAReviewDecision(t *testing.T) {
	t.Run("approve", func(t *testing.T) {
		d := newDecider(t)
		id := d.reviewTicket(t)
		p := d.propose(t, &pendingaction.ProposeParams{TicketID: id, Kind: pendingaction.KindReviewDecision,
			Payload: pendingaction.Payload{Decision: pendingaction.DecisionApprove}})

		_, err := d.svc.Approve(d.ctx, d.approver, p.ID)
		require.NoError(t, err)
		got, err := d.tickets.Get(d.ctx, id)
		require.NoError(t, err)
		require.NotNil(t, got.ApprovedByUserID)
		assert.Equal(t, d.approver.ID, *got.ApprovedByUserID, "approved by the person, not the console")
		comments := d.comments(t, id)
		require.Len(t, comments, 1)
		require.NotNil(t, comments[0].AuthorUserID)
		assert.Equal(t, d.approver.ID, *comments[0].AuthorUserID)
		assert.Contains(t, comments[0].Body, "Drafted by console-one")
	})
	t.Run("request changes", func(t *testing.T) {
		d := newDecider(t)
		id := d.reviewTicket(t)
		p := d.propose(t, &pendingaction.ProposeParams{TicketID: id, Kind: pendingaction.KindReviewDecision,
			Payload: pendingaction.Payload{Decision: pendingaction.DecisionRequestChanges, Body: "add a test"}})

		_, err := d.svc.Approve(d.ctx, d.approver, p.ID)
		require.NoError(t, err)
		assert.Equal(t, ticket.StatusOpen, d.ticketStatus(t, id), "back to the queue")
		comments := d.comments(t, id)
		require.NotEmpty(t, comments)
		assert.Contains(t, comments[len(comments)-1].Body, "add a test")
		change, who := d.lastChange(t, id)
		assert.Equal(t, "proposal:"+p.ID.String(), change.Cause)
		require.NotNil(t, who)
		assert.Equal(t, d.approver.ID, *who)
	})
}

func TestApprovingRefusesAProposalWhoseTicketMovedOn(t *testing.T) {
	tests := map[string]func(t *testing.T, d *decider, ticketID int64){
		"status changed": func(t *testing.T, d *decider, id int64) {
			d.exec(t, `UPDATE tickets SET status = 'open' WHERE id = $1`, id)
		},
		"text edited": func(t *testing.T, d *decider, id int64) {
			d.exec(t, `UPDATE tickets SET revision = revision + 1 WHERE id = $1`, id)
		},
		"PR changed": func(t *testing.T, d *decider, id int64) {
			d.exec(t, `UPDATE attempts SET pr_url = 'https://github.com/o/r/pull/2' WHERE ticket_id = $1 AND number = 1`, id)
		},
		"review started again": func(t *testing.T, d *decider, id int64) {
			d.exec(t, `UPDATE attempts SET review_started_at = $2 WHERE ticket_id = $1 AND number = 1`, id, d.clock.Now().Add(time.Hour))
		},
	}
	for name, change := range tests {
		t.Run(name, func(t *testing.T) {
			d := newDecider(t)
			id := d.reviewTicket(t)
			p := d.propose(t, &pendingaction.ProposeParams{TicketID: id, Kind: pendingaction.KindReviewDecision,
				Payload: pendingaction.Payload{Decision: pendingaction.DecisionApprove}})
			change(t, d, id)
			before, err := d.tickets.Get(d.ctx, id)
			require.NoError(t, err)

			settled, err := d.svc.Approve(d.ctx, d.approver, p.ID)
			require.ErrorIs(t, err, pendingaction.ErrStale)
			require.NotNil(t, settled)
			assert.Equal(t, pendingaction.StateStale, settled.State)
			assert.NotEmpty(t, settled.Outcome)
			after, err := d.tickets.Get(d.ctx, id)
			require.NoError(t, err)
			assert.Nil(t, after.ApprovedByUserID, "nothing was applied")
			assert.Equal(t, before.Status, after.Status)
			assert.Equal(t, 1, d.events.Count(audit.ProposalRefused))
		})
	}
}

// A comment is not a change to the ticket the way a proposal's approval
// depends on it: routine replies must not make every proposal stale.
func TestACommentOnTheTicketDoesNotMakeAProposalStale(t *testing.T) {
	d := newDecider(t)
	p := d.proposeIn(t, ticket.StatusPreparing, pendingaction.KindReady, pendingaction.Payload{})
	_, err := d.tickets.AddComment(d.ctx, *p.TicketID, ticket.UserAuthor(d.approver.ID), "a note")
	require.NoError(t, err)

	_, err = d.svc.Approve(d.ctx, d.approver, p.ID)
	require.NoError(t, err)
}

func TestApprovingRefusesATicketAnAgentNowHolds(t *testing.T) {
	d := newDecider(t)
	p := d.proposeIn(t, ticket.StatusOpen, pendingaction.KindStatus, pendingaction.Payload{Status: ticket.StatusParked, Body: "later"})
	worker := d.newAgent(t, "worker", false)
	claimed, err := d.tickets.Claim(d.ctx, worker, p.TicketID)
	require.NoError(t, err)
	require.NotNil(t, claimed.Ticket, "the worker holds the ticket")
	// Claiming changes the ticket's status, which alone would make it stale;
	// put back what the basis saw so it is the lease that is under test.
	d.exec(t, `UPDATE tickets SET status = 'open' WHERE id = $1`, p.TicketID)

	settled, err := d.svc.Approve(d.ctx, d.approver, p.ID)
	require.ErrorIs(t, err, pendingaction.ErrStale)
	assert.Contains(t, settled.Outcome, "holds the ticket")
	assert.Equal(t, ticket.StatusOpen, d.ticketStatus(t, *p.TicketID))
}

func TestApprovingRefusesAProposerThatIsNoLongerAConsole(t *testing.T) {
	tests := map[string]func(d *decider, t *testing.T){
		"disabled": func(d *decider, t *testing.T) {
			d.exec(t, `UPDATE agents SET disabled_at = now() WHERE id = $1`, d.console)
		},
		"no live grant": func(d *decider, t *testing.T) { d.grants.revoke(d.console) },
	}
	for name, undo := range tests {
		t.Run(name, func(t *testing.T) {
			d := newDecider(t)
			p := d.proposeIn(t, ticket.StatusPreparing, pendingaction.KindReady, pendingaction.Payload{})
			undo(d, t)

			settled, err := d.svc.Approve(d.ctx, d.approver, p.ID)
			require.ErrorIs(t, err, pendingaction.ErrVoided)
			assert.Equal(t, pendingaction.StateVoided, settled.State)
			assert.Equal(t, ticket.StatusPreparing, d.ticketStatus(t, *p.TicketID), "nothing was applied")
		})
	}
}

func TestAReplayedApprovalAppliesOnce(t *testing.T) {
	d := newDecider(t)
	p := d.proposeIn(t, ticket.StatusDiscussion, pendingaction.KindDiscussionAnswer, pendingaction.Payload{Body: "yes"})

	_, err := d.svc.Approve(d.ctx, d.approver, p.ID)
	require.NoError(t, err)
	_, err = d.svc.Approve(d.ctx, d.approver, p.ID)
	require.ErrorIs(t, err, pendingaction.ErrNotPending)
	assert.Len(t, d.comments(t, *p.TicketID), 1)
}

func TestConcurrentApprovalsApplyOnce(t *testing.T) {
	d := newDecider(t)
	p := d.proposeIn(t, ticket.StatusDiscussion, pendingaction.KindDiscussionAnswer, pendingaction.Payload{Body: "yes"})

	const racers = 6
	start := make(chan struct{})
	errs := make([]error, racers)
	var wg sync.WaitGroup
	for i := range racers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, errs[i] = d.svc.Approve(d.ctx, d.approver, p.ID)
		}()
	}
	close(start)
	wg.Wait()

	succeeded := 0
	for _, err := range errs {
		if err == nil {
			succeeded++
		} else {
			assert.ErrorIs(t, err, pendingaction.ErrNotPending)
		}
	}
	assert.Equal(t, 1, succeeded)
	assert.Len(t, d.comments(t, *p.TicketID), 1, "the answer was posted once")
}

func TestAnExpiredProposalCannotBeApproved(t *testing.T) {
	d := newDecider(t)
	p := d.proposeIn(t, ticket.StatusPreparing, pendingaction.KindReady, pendingaction.Payload{})
	d.clock.Advance(pendingaction.DefaultTTL + time.Second)

	_, err := d.svc.Approve(d.ctx, d.approver, p.ID)
	require.ErrorIs(t, err, pendingaction.ErrNotPending)
	assert.Equal(t, ticket.StatusPreparing, d.ticketStatus(t, *p.TicketID))
}

// failingWrites is the ticket service's transaction with the ticket's own
// row refusing every write: the point at which a rule of the ticket's, which
// the approval does not know, would refuse -- after the approval's comment is
// already in.
type failingWrites struct{ *ticket.Service }

type refusingTx struct{ ticket.TransitionTx }

func (refusingTx) WriteTicket(context.Context, int64, *ticket.TicketWrite) error {
	return ticket.ErrNotInReview
}

func (f failingWrites) InTransition(ctx context.Context, fn func(tx ticket.TransitionTx) error) error {
	return f.Service.InTransition(ctx, func(tx ticket.TransitionTx) error { return fn(refusingTx{tx}) })
}

// A refusal by the ticket's own rules rolls back whatever the approval had
// begun, and is recorded as failed.
func TestAProposalTheTicketRulesRefuseIsRecordedAsFailedAndChangesNothing(t *testing.T) {
	d := newDecider(t)
	d.svc = pendingaction.NewService(pendingaction.NewPostgresStore(d.database), pendingaction.Config{}, slog.DiscardHandler,
		pendingaction.WithApproval(failingWrites{d.tickets}, d.grants))
	d.svc.SetNow(d.clock.Now)
	id := d.reviewTicket(t)
	p := d.propose(t, &pendingaction.ProposeParams{TicketID: id, Kind: pendingaction.KindReviewDecision,
		Payload: pendingaction.Payload{Decision: pendingaction.DecisionApprove}})

	settled, err := d.svc.Approve(d.ctx, d.approver, p.ID)
	require.ErrorIs(t, err, pendingaction.ErrRefused)
	require.ErrorIs(t, err, ticket.ErrNotInReview)
	assert.Equal(t, pendingaction.StateFailed, settled.State)
	assert.Contains(t, settled.Outcome, "not in review")
	assert.Empty(t, d.comments(t, id), "the approval's own comment was rolled back with it")
}

func TestRejectingRecordsTheReasonAndChangesNothing(t *testing.T) {
	d := newDecider(t)
	p := d.proposeIn(t, ticket.StatusPreparing, pendingaction.KindReady, pendingaction.Payload{})

	rejected, err := d.svc.Reject(d.ctx, d.approver, p.ID, "not yet")
	require.NoError(t, err)
	assert.Equal(t, pendingaction.StateRejected, rejected.State)
	assert.Equal(t, "rejected: not yet", rejected.Outcome)
	assert.Equal(t, ticket.StatusPreparing, d.ticketStatus(t, *p.TicketID))

	_, err = d.svc.Reject(d.ctx, d.approver, p.ID, "")
	require.ErrorIs(t, err, pendingaction.ErrNotPending)
	_, err = d.svc.Approve(d.ctx, d.approver, p.ID)
	require.ErrorIs(t, err, pendingaction.ErrNotPending)
	assert.Equal(t, 1, d.events.Count(audit.ProposalRejected))
}

func TestRejectAllRejectsEveryPendingProposal(t *testing.T) {
	d := newDecider(t)
	a := d.proposeIn(t, ticket.StatusPreparing, pendingaction.KindReady, pendingaction.Payload{})
	b := d.proposeIn(t, ticket.StatusDiscussion, pendingaction.KindDiscussionAnswer, pendingaction.Payload{Body: "x"})
	decided := d.proposeIn(t, ticket.StatusPreparing, pendingaction.KindReady, pendingaction.Payload{})
	_, err := d.svc.Approve(d.ctx, d.approver, decided.ID)
	require.NoError(t, err)

	rejected, err := d.svc.RejectAll(d.ctx, d.approver, nil, "clearing the queue")
	require.NoError(t, err)
	assert.Len(t, rejected, 2)
	assert.Equal(t, pendingaction.StateRejected, d.stateOf(t, a).State)
	assert.Equal(t, pendingaction.StateRejected, d.stateOf(t, b).State)
	assert.Equal(t, pendingaction.StateApproved, d.stateOf(t, decided).State, "a decided one is left as it was")
}

func TestApproveAllTakesOneBatchableKindAndReportsEachResult(t *testing.T) {
	d := newDecider(t)
	good := d.proposeIn(t, ticket.StatusPreparing, pendingaction.KindReady, pendingaction.Payload{})
	moved := d.proposeIn(t, ticket.StatusPreparing, pendingaction.KindReady, pendingaction.Payload{})
	d.exec(t, `UPDATE tickets SET revision = revision + 1 WHERE id = $1`, moved.TicketID)
	answer := d.proposeIn(t, ticket.StatusDiscussion, pendingaction.KindDiscussionAnswer, pendingaction.Payload{Body: "x"})

	results, err := d.svc.ApproveAll(d.ctx, d.approver, pendingaction.KindReady, nil)
	require.NoError(t, err)
	byID := map[uuid.UUID]pendingaction.Result{}
	for _, r := range results {
		byID[r.ID] = r
	}
	require.Len(t, byID, 2, "only the ready proposals")
	assert.Equal(t, pendingaction.StateApproved, byID[good.ID].State)
	assert.NoError(t, byID[good.ID].Err)
	assert.Equal(t, pendingaction.StateStale, byID[moved.ID].State)
	assert.ErrorIs(t, byID[moved.ID].Err, pendingaction.ErrStale)
	assert.Equal(t, pendingaction.StatePending, d.stateOf(t, answer).State, "another kind is not touched")
}

func TestBatchesNamingProposalsDecideOnlyThose(t *testing.T) {
	d := newDecider(t)
	shown := d.proposeIn(t, ticket.StatusPreparing, pendingaction.KindReady, pendingaction.Payload{})
	hidden := d.proposeIn(t, ticket.StatusPreparing, pendingaction.KindReady, pendingaction.Payload{})
	rejectShown := d.proposeIn(t, ticket.StatusDiscussion, pendingaction.KindDiscussionAnswer, pendingaction.Payload{Body: "x"})
	rejectHidden := d.proposeIn(t, ticket.StatusDiscussion, pendingaction.KindDiscussionAnswer, pendingaction.Payload{Body: "y"})

	results, err := d.svc.ApproveAll(d.ctx, d.approver, pendingaction.KindReady, []uuid.UUID{shown.ID})
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, shown.ID, results[0].ID)
	assert.Equal(t, pendingaction.StateApproved, d.stateOf(t, shown).State)
	assert.Equal(t, pendingaction.StatePending, d.stateOf(t, hidden).State, "a proposal the batch did not name is left alone")

	rejected, err := d.svc.RejectAll(d.ctx, d.approver, []uuid.UUID{rejectShown.ID}, "")
	require.NoError(t, err)
	require.Len(t, rejected, 1)
	assert.Equal(t, pendingaction.StateRejected, d.stateOf(t, rejectShown).State)
	assert.Equal(t, pendingaction.StatePending, d.stateOf(t, rejectHidden).State)
}

func TestBatchesNamingNothingDecideNothing(t *testing.T) {
	d := newDecider(t)
	ready := d.proposeIn(t, ticket.StatusPreparing, pendingaction.KindReady, pendingaction.Payload{})

	results, err := d.svc.ApproveAll(d.ctx, d.approver, pendingaction.KindReady, []uuid.UUID{})
	require.NoError(t, err)
	assert.Empty(t, results)
	rejected, err := d.svc.RejectAll(d.ctx, d.approver, []uuid.UUID{}, "")
	require.NoError(t, err)
	assert.Empty(t, rejected)
	assert.Equal(t, pendingaction.StatePending, d.stateOf(t, ready).State, "an empty list is not \"everything\"")
}

func TestBatchesNamingTooManyProposalsAreRefused(t *testing.T) {
	d := newDecider(t)
	ids := make([]uuid.UUID, pendingaction.MaxBatch+1)
	_, err := d.svc.ApproveAll(d.ctx, d.approver, pendingaction.KindReady, ids)
	require.ErrorIs(t, err, pendingaction.ErrInvalid)
	_, err = d.svc.RejectAll(d.ctx, d.approver, ids, "")
	require.ErrorIs(t, err, pendingaction.ErrInvalid)
}

func TestApproveAllRefusesKindsThatAreNeverBatched(t *testing.T) {
	d := newDecider(t)
	cancel := d.proposeIn(t, ticket.StatusOpen, pendingaction.KindStatus, pendingaction.Payload{Status: ticket.StatusCancelled, Body: "x"})
	review := d.propose(t, &pendingaction.ProposeParams{TicketID: d.reviewTicket(t), Kind: pendingaction.KindReviewDecision,
		Payload: pendingaction.Payload{Decision: pendingaction.DecisionApprove}})

	for _, kind := range []pendingaction.Kind{pendingaction.KindStatus, pendingaction.KindReviewDecision, "anything"} {
		_, err := d.svc.ApproveAll(d.ctx, d.approver, kind, nil)
		require.ErrorIs(t, err, pendingaction.ErrInvalid, kind)
	}
	assert.Equal(t, pendingaction.StatePending, d.stateOf(t, cancel).State)
	assert.Equal(t, pendingaction.StatePending, d.stateOf(t, review).State)
}

func TestAnotherTenantsProposalIsNotFound(t *testing.T) {
	d := newDecider(t)
	p := d.proposeIn(t, ticket.StatusPreparing, pendingaction.KindReady, pendingaction.Payload{})
	otherTenant := dbtest.NewTenant(t, d.database)
	otherCtx := db.WithTenant(context.Background(), otherTenant)
	stranger := &authn.User{ID: d.approver.ID, TenantID: otherTenant, AuthTime: d.clock.Now()}

	_, err := d.svc.Approve(otherCtx, stranger, p.ID)
	require.ErrorIs(t, err, pendingaction.ErrNotFound)
	_, err = d.svc.Reject(otherCtx, stranger, p.ID, "")
	require.ErrorIs(t, err, pendingaction.ErrNotFound)
	rejected, err := d.svc.RejectAll(otherCtx, stranger, nil, "")
	require.NoError(t, err)
	assert.Empty(t, rejected)
	assert.Equal(t, pendingaction.StatePending, d.stateOf(t, p).State)
	assert.Equal(t, ticket.StatusPreparing, d.ticketStatus(t, *p.TicketID))
}

func TestApprovingNeedsApprovalToBeConfigured(t *testing.T) {
	f := newFixture(t, pendingaction.Config{})
	console := f.newAgent(t, "console", true)
	p, err := f.svc.Propose(f.ctx, &pendingaction.ProposeParams{AgentID: console, TicketID: f.ticketIn(t, ticket.StatusPreparing),
		Kind: pendingaction.KindReady})
	require.NoError(t, err)

	_, err = f.svc.Approve(f.ctx, &authn.User{ID: uuid.New(), TenantID: f.tenant}, p.ID)
	require.ErrorIs(t, err, pendingaction.ErrNotConfigured)
}

func TestProposingIsAuditedWithoutTheProposersText(t *testing.T) {
	d := newDecider(t)
	d.proposeIn(t, ticket.StatusDiscussion, pendingaction.KindDiscussionAnswer, pendingaction.Payload{Body: "SECRET-PROSE"})

	assert.Equal(t, 1, d.events.Count(audit.ProposalCreated))
}

func TestViewsDescribeTheProposalAndSayWhenItWentStale(t *testing.T) {
	d := newDecider(t)
	p := d.proposeIn(t, ticket.StatusPreparing, pendingaction.KindReady, pendingaction.Payload{})

	views, err := d.svc.Views(d.ctx, &pendingaction.ListFilter{})
	require.NoError(t, err)
	require.Len(t, views, 1)
	assert.Equal(t, "console-one", views[0].AgentName)
	assert.Equal(t, "a ticket", views[0].TicketTitle)
	assert.Contains(t, views[0].ActionLine, "#")
	assert.Empty(t, views[0].StaleReason)

	d.exec(t, `UPDATE tickets SET revision = revision + 1 WHERE id = $1`, p.TicketID)
	views, err = d.svc.Views(d.ctx, &pendingaction.ListFilter{})
	require.NoError(t, err)
	assert.Contains(t, views[0].StaleReason, "edited")
}

func TestTheActionLineIgnoresTheProposersText(t *testing.T) {
	loud := &pendingaction.Action{TicketID: ptr(int64(7)), Kind: pendingaction.KindStatus,
		Payload: pendingaction.Payload{Status: ticket.StatusCancelled, Body: "This only parks it, harmless"}}
	line := pendingaction.ActionLine(loud, ticket.StatusOpen)
	assert.Contains(t, line, "Cancel #7")
	assert.NotContains(t, line, "harmless")
}

func TestARejectionReasonIsTrimmedAndBounded(t *testing.T) {
	d := newDecider(t)
	p := d.proposeIn(t, ticket.StatusPreparing, pendingaction.KindReady, pendingaction.Payload{})

	_, err := d.svc.Reject(d.ctx, d.approver, p.ID, strings.Repeat("x", pendingaction.MaxReasonBytes+1))
	require.ErrorIs(t, err, pendingaction.ErrInvalid)
	assert.Equal(t, pendingaction.StatePending, d.stateOf(t, p).State)

	rejected, err := d.svc.Reject(d.ctx, d.approver, p.ID, "  \n ")
	require.NoError(t, err)
	assert.Equal(t, "rejected", rejected.Outcome, "a blank reason says only that it was rejected")
}

func TestListFiltersByKind(t *testing.T) {
	d := newDecider(t)
	ready := d.proposeIn(t, ticket.StatusPreparing, pendingaction.KindReady, pendingaction.Payload{})
	d.proposeIn(t, ticket.StatusDiscussion, pendingaction.KindDiscussionAnswer, pendingaction.Payload{Body: "x"})

	kind := pendingaction.KindReady
	got, err := d.svc.List(d.ctx, &pendingaction.ListFilter{Kind: &kind})
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, ready.ID, got[0].ID)
	all, err := d.svc.List(d.ctx, &pendingaction.ListFilter{})
	require.NoError(t, err)
	assert.Len(t, all, 2)
}

func TestTheActionLineSaysWhatEachKindDoes(t *testing.T) {
	for name, tc := range map[string]struct {
		action pendingaction.Action
		now    ticket.Status
		want   []string
	}{
		"answer":  {pendingaction.Action{Kind: pendingaction.KindDiscussionAnswer}, ticket.StatusDiscussion, []string{"Post the text below", "#7", "back in the queue"}},
		"ready":   {pendingaction.Action{Kind: pendingaction.KindReady}, ticket.StatusPreparing, []string{"Move #7 out of preparing"}},
		"park":    {pendingaction.Action{Kind: pendingaction.KindStatus, Payload: pendingaction.Payload{Status: ticket.StatusParked}}, ticket.StatusOpen, []string{"Park #7 (now open)"}},
		"approve": {pendingaction.Action{Kind: pendingaction.KindReviewDecision, Payload: pendingaction.Payload{Decision: pendingaction.DecisionApprove}}, ticket.StatusReview, []string{"Approve the change under review on #7", "merge"}},
		"changes": {pendingaction.Action{Kind: pendingaction.KindReviewDecision, Payload: pendingaction.Payload{Decision: pendingaction.DecisionRequestChanges}}, ticket.StatusReview, []string{"Send #7 back from review"}},
	} {
		tc.action.TicketID = ptr(int64(7))
		line := pendingaction.ActionLine(&tc.action, tc.now)
		for _, w := range tc.want {
			assert.Contains(t, line, w, name)
		}
	}
}

// Approving takes the proposer's row, then the ticket's, as proposing does,
// so an approval and a re-proposal on one ticket cannot each hold what the
// other wants. This is a smoke test of that: whether the interleaving that
// would deadlock comes up is down to timing, so it does not prove the order,
// which LockForDecision's comment records.
func TestApprovingWhileReProposingNeverDeadlocks(t *testing.T) {
	d := newDecider(t)
	for range 5 {
		id := d.ticketIn(t, ticket.StatusDiscussion)
		first := d.propose(t, &pendingaction.ProposeParams{TicketID: id, Kind: pendingaction.KindDiscussionAnswer,
			Payload: pendingaction.Payload{Body: "one"}})

		start := make(chan struct{})
		var wg sync.WaitGroup
		var approveErr, proposeErr error
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			_, approveErr = d.svc.Approve(d.ctx, d.approver, first.ID)
		}()
		go func() {
			defer wg.Done()
			<-start
			_, proposeErr = d.svc.Propose(d.ctx, &pendingaction.ProposeParams{AgentID: d.console, TicketID: id,
				Kind: pendingaction.KindDiscussionAnswer, Payload: pendingaction.Payload{Body: "two"}})
		}()
		close(start)
		wg.Wait()

		if approveErr != nil {
			assert.ErrorIs(t, approveErr, pendingaction.ErrNotPending)
		}
		if proposeErr != nil {
			assert.ErrorIs(t, proposeErr, pendingaction.ErrInvalid, "the approval won, and the ticket left discussion")
		}
	}
}

func TestApprovingAnAnswerUsesTheTicketServicesAffinityWindow(t *testing.T) {
	d := newDecider(t)
	d.svc = pendingaction.NewService(pendingaction.NewPostgresStore(d.database), pendingaction.Config{}, slog.DiscardHandler,
		pendingaction.WithApproval(
			ticket.NewService(ticket.NewPostgresStore(d.database), slog.DiscardHandler, ticket.WithResumeAffinity(90*time.Minute)),
			d.grants))
	d.svc.SetNow(d.clock.Now)
	p := d.proposeIn(t, ticket.StatusDiscussion, pendingaction.KindDiscussionAnswer, pendingaction.Payload{Body: "use option B"})
	d.exec(t, `UPDATE attempts SET resume_agent_id = $2 WHERE ticket_id = $1 AND number = 1`, p.TicketID, d.newAgent(t, "asker", false))

	_, err := d.svc.Approve(d.ctx, d.approver, p.ID)
	require.NoError(t, err)

	got, err := d.tickets.Get(d.ctx, *p.TicketID)
	require.NoError(t, err)
	require.NotNil(t, got.AffinityUntil, "the asker has first claim")
	// Past the 30 minute default, so only the configured window satisfies it.
	assert.Greater(t, time.Until(*got.AffinityUntil), 60*time.Minute)
}
