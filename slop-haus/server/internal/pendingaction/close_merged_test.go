package pendingaction_test

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

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/pendingaction"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// closerFake stands in for the code host and the ticket service's close,
// which ticket's own tests cover. It records what approving asked of it.
type closerFake struct {
	facts *ticket.MergeFacts
	err   error
	calls []closeCall
}

type closeCall struct {
	ticketID int64
	person   uuid.UUID
	confirm  bool
}

func (c *closerFake) MergeFacts(context.Context, int64) (*ticket.MergeFacts, error) {
	return c.facts, c.err
}

func (c *closerFake) CloseAsMerged(_ context.Context, ticketID int64, person uuid.UUID, confirm bool) (*ticket.CloseAsMergedResult, error) {
	c.calls = append(c.calls, closeCall{ticketID, person, confirm})
	if c.err != nil {
		return nil, c.err
	}
	return &ticket.CloseAsMergedResult{}, nil
}

type closeMergedRig struct {
	*decider
	owner, member *authn.User
	owners        *ownersFake
	auto          *autoSwitch
	closer        *closerFake
	ticketID      int64
}

func newCloseMergedRig(t *testing.T, opts ...pendingaction.Option) *closeMergedRig {
	t.Helper()
	d := newDecider(t)
	r := &closeMergedRig{decider: d, auto: &autoSwitch{}, owners: &ownersFake{owners: map[uuid.UUID]bool{}}, closer: &closerFake{}}
	r.owner = &authn.User{ID: dbtest.NewUser(t, d.database), TenantID: d.tenant}
	r.member = &authn.User{ID: dbtest.NewUser(t, d.database), TenantID: d.tenant}
	r.owners.owners[r.owner.ID] = true
	d.svc = pendingaction.NewService(pendingaction.NewPostgresStore(d.database), pendingaction.Config{}, slog.DiscardHandler,
		append([]pendingaction.Option{pendingaction.WithApproval(d.tickets, d.grants), pendingaction.WithRoles(r.owners),
			pendingaction.WithMergeCloser(r.closer), pendingaction.WithAutoApprove(ownerResolver{user: r.owner.ID}, r.auto)}, opts...)...)
	d.svc.SetNow(d.clock.Now)
	r.ticketID = r.ticketIn(t, ticket.StatusOpen)
	return r
}

func (r *closeMergedRig) propose(t *testing.T) *pendingaction.Action {
	t.Helper()
	a, err := r.svc.Propose(r.ctx, &pendingaction.ProposeParams{
		AgentID: r.console, TicketID: r.ticketID, Kind: pendingaction.KindCloseMerged,
		Payload: pendingaction.Payload{Body: "merged by hand"},
	})
	require.NoError(t, err)
	return a
}

func TestCloseMergedNeedsAReasonAndNothingElse(t *testing.T) {
	r := newCloseMergedRig(t)
	for name, payload := range map[string]pendingaction.Payload{
		"no reason":    {},
		"with status":  {Body: "x", Status: ticket.StatusParked},
		"with decison": {Body: "x", Decision: pendingaction.DecisionApprove},
	} {
		_, err := r.svc.Propose(r.ctx, &pendingaction.ProposeParams{AgentID: r.console, TicketID: r.ticketID, Kind: pendingaction.KindCloseMerged, Payload: payload})
		assert.ErrorIs(t, err, pendingaction.ErrInvalid, name)
	}
	_, err := r.svc.Propose(r.ctx, &pendingaction.ProposeParams{AgentID: r.console, Kind: pendingaction.KindCloseMerged, Payload: pendingaction.Payload{Body: "x"}})
	assert.ErrorIs(t, err, pendingaction.ErrInvalid, "it is about a ticket")
	r.exec(t, `UPDATE tickets SET status = 'done' WHERE id = $1`, r.ticketID)
	_, err = r.svc.Propose(r.ctx, &pendingaction.ProposeParams{AgentID: r.console, TicketID: r.ticketID, Kind: pendingaction.KindCloseMerged, Payload: pendingaction.Payload{Body: "x"}})
	assert.ErrorIs(t, err, pendingaction.ErrInvalid, "a finished ticket has nothing to close")
}

func TestCloseMergedClosesNothingUntilAnOwnerApproves(t *testing.T) {
	r := newCloseMergedRig(t)
	p := r.propose(t)
	assert.Equal(t, pendingaction.StatePending, p.State)
	assert.Empty(t, r.closer.calls, "proposing closes nothing")

	_, err := r.svc.Approve(r.ctx, r.member, p.ID)
	require.ErrorIs(t, err, pendingaction.ErrNotApprover)
	assert.Empty(t, r.closer.calls, "a member's approval closes nothing")

	approved, err := r.svc.Approve(r.ctx, r.owner, p.ID)
	require.NoError(t, err)
	assert.Equal(t, pendingaction.StateApproved, approved.State)
	assert.Equal(t, []closeCall{{r.ticketID, r.owner.ID, true}}, r.closer.calls,
		"the owner's approval is the confirmation of what the close warns about")
}

func TestCloseMergedIsNeverAutoApproved(t *testing.T) {
	r := newCloseMergedRig(t)
	r.auto.on.Store(true)
	p := r.propose(t)
	assert.Equal(t, pendingaction.StatePending, p.State)
	assert.False(t, p.AutoApproved())
	assert.NotContains(t, pendingaction.AutoApprovableKinds(), pendingaction.KindCloseMerged)
	assert.Empty(t, r.closer.calls)
}

func TestCloseMergedRefusedByTheCloseFailsTheProposal(t *testing.T) {
	r := newCloseMergedRig(t)
	p := r.propose(t)
	r.closer.err = ticket.ErrNotMergedIntoBase

	failed, err := r.svc.Approve(r.ctx, r.owner, p.ID)
	require.ErrorIs(t, err, pendingaction.ErrRefused)
	require.ErrorIs(t, err, ticket.ErrNotMergedIntoBase)
	assert.Equal(t, pendingaction.StateFailed, failed.State)
	assert.Contains(t, failed.Outcome, "not merged into")
}

func TestCloseMergedStaleWhenTheTicketMovedOnNeverAsksTheHost(t *testing.T) {
	r := newCloseMergedRig(t)
	p := r.propose(t)
	r.exec(t, `UPDATE tickets SET status = 'parked', revision = revision + 1 WHERE id = $1`, r.ticketID)

	_, err := r.svc.Approve(r.ctx, r.owner, p.ID)
	require.ErrorIs(t, err, pendingaction.ErrStale)
	assert.Empty(t, r.closer.calls)
}

func TestCloseMergedViewShowsWhatApprovingAccepts(t *testing.T) {
	r := newCloseMergedRig(t)
	p := r.propose(t)
	r.closer.facts = &ticket.MergeFacts{Base: "main", Merged: true, MergedIntoBase: true, MergedHead: "abcdef1234567890", Checks: ticket.ChecksFailed}

	v, err := r.svc.ViewOf(r.ctx, r.owner, p.ID)
	require.NoError(t, err)
	require.NotNil(t, v.CloseMerged)
	assert.Empty(t, v.CloseMerged.Problem)
	assert.Len(t, v.CloseMerged.Confirmations, 2, "failed checks and a head nobody approved")

	r.closer.facts = &ticket.MergeFacts{Base: "main"}
	v, err = r.svc.ViewOf(r.ctx, r.owner, p.ID)
	require.NoError(t, err)
	assert.Contains(t, v.CloseMerged.Problem, "main")

	r.closer.err = errors.New("host down")
	v, err = r.svc.ViewOf(r.ctx, r.owner, p.ID)
	require.NoError(t, err)
	assert.Contains(t, v.CloseMerged.Problem, "host down")
}

// A concurrent approval that closed the ticket first is settling the
// proposal as approved; this one must not fail it over the ticket's being done.
func TestCloseMergedLosingTheRaceLeavesTheProposalToTheWinner(t *testing.T) {
	r := newCloseMergedRig(t)
	p := r.propose(t)
	r.closer.err = ticket.ErrTicketFinished

	_, err := r.svc.Approve(r.ctx, r.owner, p.ID)

	require.ErrorIs(t, err, pendingaction.ErrNotPending)
	got, err := r.svc.Get(r.ctx, p.ID)
	require.NoError(t, err)
	assert.Equal(t, pendingaction.StatePending, got.State)
}

// rendezvousCloser answers MergeFacts only once every expected caller is
// inside it, so calls made one after another never get an answer; a call
// that gives up when its context ends is told so.
type rendezvousCloser struct {
	closerFake
	arrived sync.WaitGroup
	release chan struct{}
	mu      sync.Mutex
	asked   map[int64]bool
}

func (c *rendezvousCloser) MergeFacts(ctx context.Context, ticketID int64) (*ticket.MergeFacts, error) {
	c.mu.Lock()
	if c.asked == nil {
		c.asked = map[int64]bool{}
	}
	c.asked[ticketID] = true
	c.mu.Unlock()
	c.arrived.Done()
	select {
	case <-c.release:
		return &ticket.MergeFacts{Base: "main", Merged: true, MergedIntoBase: true}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (r *closeMergedRig) proposeOnNewTicket(t *testing.T) {
	t.Helper()
	_, err := r.svc.Propose(r.ctx, &pendingaction.ProposeParams{
		AgentID: r.console, TicketID: r.ticketIn(t, ticket.StatusOpen), Kind: pendingaction.KindCloseMerged,
		Payload: pendingaction.Payload{Body: "merged by hand"},
	})
	require.NoError(t, err)
}

func TestCloseMergedListAsksTheHostForAllProposalsAtOnce(t *testing.T) {
	const n = 5
	slow := &rendezvousCloser{release: make(chan struct{})}
	slow.arrived.Add(n)
	r := newCloseMergedRig(t, pendingaction.WithMergeFactsDeadline(time.Minute))
	r.svc = rebuildWithCloser(t, r, slow)
	for range n {
		r.proposeOnNewTicket(t)
	}
	go func() { slow.arrived.Wait(); close(slow.release) }()

	views, err := r.svc.Views(r.ctx, &pendingaction.ListFilter{})
	require.NoError(t, err)
	require.Len(t, views, n)
	for _, v := range views {
		assert.Empty(t, v.CloseMerged.Problem)
		assert.True(t, slow.asked[*v.TicketID], "each proposal's own ticket was checked")
	}
	assert.Len(t, slow.asked, n)
}

func TestCloseMergedListGivesUpOnASlowHostButStillListsEverything(t *testing.T) {
	const n = 5
	stuck := &rendezvousCloser{release: make(chan struct{})}
	stuck.arrived.Add(n)
	r := newCloseMergedRig(t)
	r.svc = rebuildWithCloser(t, r, stuck, pendingaction.WithMergeFactsDeadline(50*time.Millisecond))
	for range n {
		r.proposeOnNewTicket(t)
	}

	start := time.Now()
	views, err := r.svc.Views(r.ctx, &pendingaction.ListFilter{})
	require.NoError(t, err)
	assert.Less(t, time.Since(start), 5*time.Second)
	require.Len(t, views, n)
	for _, v := range views {
		assert.Contains(t, v.CloseMerged.Problem, "couldn't check")
	}
}

func TestCloseMergedApprovingAsksTheHostEvenAfterAListing(t *testing.T) {
	r := newCloseMergedRig(t)
	p := r.propose(t)
	r.closer.facts = &ticket.MergeFacts{Base: "main", Merged: true, MergedIntoBase: true}
	_, err := r.svc.Views(r.ctx, &pendingaction.ListFilter{})
	require.NoError(t, err)

	_, err = r.svc.Approve(r.ctx, r.owner, p.ID)
	require.NoError(t, err)
	assert.Len(t, r.closer.calls, 1, "the close runs its own host check; the listing's facts are not trusted")
}

func rebuildWithCloser(t *testing.T, r *closeMergedRig, c pendingaction.MergeCloser, opts ...pendingaction.Option) *pendingaction.Service {
	t.Helper()
	svc := pendingaction.NewService(pendingaction.NewPostgresStore(r.database), pendingaction.Config{}, slog.DiscardHandler,
		append([]pendingaction.Option{pendingaction.WithApproval(r.tickets, r.grants), pendingaction.WithRoles(r.owners), pendingaction.WithMergeCloser(c)}, opts...)...)
	svc.SetNow(r.clock.Now)
	return svc
}
