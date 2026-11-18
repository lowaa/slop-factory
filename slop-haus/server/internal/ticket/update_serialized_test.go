package ticket_test

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

// Every field edit is serialized with the transitions and claims that read
// those fields, by the claim lock they all take. Each test here parks a
// claim or an agent's exit inside its transaction, under that lock, after it
// has read what it decides on, and sends the edit meanwhile.

// holdPoint parks the first operation to reach its barrier until release.
type holdPoint struct {
	reached chan struct{}
	leave   chan struct{}
	taken   atomic.Bool
}

func newHoldPoint() *holdPoint {
	return &holdPoint{reached: make(chan struct{}), leave: make(chan struct{})}
}

// barrier is what a test installs as the seam. Only the first caller parks;
// any later one -- the edit under test reaches the same seam -- passes
// straight through, without waiting on the first as a sync.Once would: an
// edit that wrongly ran inside the window would otherwise hang the test
// rather than fail it.
func (h *holdPoint) barrier() {
	if !h.taken.CompareAndSwap(false, true) {
		return
	}
	close(h.reached)
	<-h.leave
}

func (h *holdPoint) release() { close(h.leave) }

// editWhileHeld waits for h to be reached, sends params as an edit of id,
// and returns as whileHeld does.
func (f *fixture) editWhileHeld(t *testing.T, h *holdPoint, id int64, params *ticket.UpdateParams) (edited <-chan error, finishedEarly bool) {
	t.Helper()
	return f.whileHeld(t, h, func() error {
		_, err := f.svc.Update(f.ctx, id, params)
		return err
	})
}

// whileHeld waits for h to be reached, runs write, and returns once write
// is either queued on a lock (serialized) or back already (the race) --
// observed states, not a sleep standing in for one. finishedEarly reports
// the latter; write's result arrives on done either way. The caller
// inspects the window, then releases h.
func (f *fixture) whileHeld(t *testing.T, h *holdPoint, write func() error) (done <-chan error, finishedEarly bool) {
	t.Helper()
	<-h.reached
	result := make(chan error, 1)
	go func() { result <- write() }()
	deadline := time.Now().Add(10 * time.Second)
	for f.lockWaiters(t) == 0 {
		select {
		case err := <-result:
			// Put it back for the caller, who still reads done.
			result <- err
			return result, true
		case <-time.After(5 * time.Millisecond):
		}
		require.True(t, time.Now().Before(deadline), "the write neither waited nor finished")
	}
	return result, false
}

// editDuringDone parks agentID's Done after it has loaded its ticket, under
// the claim lock, and sends params as an edit of that ticket meanwhile. It
// returns Done's result, whether the edit came back inside Done's window,
// and the ticket as read inside it, once both have finished.
func (f *fixture) editDuringDone(t *testing.T, agentID uuid.UUID, id int64, params *ticket.UpdateParams) (doneErr error, early bool, midway *ticket.Ticket) {
	t.Helper()
	h := newHoldPoint()
	defer ticket.SetDecideBarrier(h.barrier)()
	finished := make(chan error, 1)
	go func() {
		_, err := f.svc.Done(f.ctx, agentID, &ticket.DoneParams{Summary: "built"})
		finished <- err
	}()

	edited, early := f.editWhileHeld(t, h, id, params)
	midway = f.get(t, id)
	h.release()

	doneErr = <-finished
	require.NoError(t, <-edited)
	return doneErr, early, midway
}

// The race this ticket is about. Done has read requires_approval=false
// under the claim lock; the human then turns the gate on. If the edit could
// commit in that window, Done would go on to close the ticket past a gate
// that was already on when it wrote. Serialized, the edit waits for Done
// and lands on the ticket Done decided on -- Done never acts on a state the
// human has since changed.
//
// Verified both ways it can break: with Update writing the field outside
// the claim lock, the edit finishes inside Done's window and the ticket
// closes with the gate on; writing it first and taking the lock after, the
// edit waits but the gate is already seen on inside the window.
func TestUpdate_TurningOnApprovalWaitsForADoneThatHasAlreadyDecided(t *testing.T) {
	f := newFixture(t)
	a := f.agent(t, "builder")
	held := f.ticket(t, "Ungated", "server/")
	f.claim(t, a)

	gate := true
	doneErr, early, midway := f.editDuringDone(t, a, held.ID, &ticket.UpdateParams{RequiresApproval: &gate})

	require.NoError(t, doneErr)
	assert.False(t, early, "the edit finished while Done held the claim lock")
	assert.False(t, midway.RequiresApproval, "the gate was turned on under a Done that had read it off")
	got := f.get(t, held.ID)
	assert.Equal(t, ticket.StatusDone, got.Status)
	assert.True(t, got.RequiresApproval, "and lands once Done has finished")
}

// Done's other gate, needs_discussion, read the same way: Done has read it
// set and is refusing, and the human clears it meanwhile. The edit waits
// for the refusal rather than land under a decision made on the flag it
// clears.
//
// Verified both ways it can break, as above: the edit finishes inside
// Done's window, or waits with the flag already seen cleared inside it.
func TestUpdate_ClearingNeedsDiscussionWaitsForADoneThatHasAlreadyDecided(t *testing.T) {
	f := newFixture(t)
	a := f.agent(t, "planner")
	held, err := f.svc.Create(f.ctx, &ticket.CreateParams{
		Title: "A direction", Paths: []model.Path{{Path: "server/"}}, NeedsDiscussion: true,
	})
	require.NoError(t, err)
	f.claim(t, a)

	cleared := false
	doneErr, early, midway := f.editDuringDone(t, a, held.ID, &ticket.UpdateParams{NeedsDiscussion: &cleared})

	assert.ErrorIs(t, doneErr, ticket.ErrNeedsDiscussion, "Done decided on the flag it read")
	assert.False(t, early, "the edit finished while Done held the claim lock")
	assert.True(t, midway.NeedsDiscussion, "the flag was cleared under a Done that had read it set")
	got := f.get(t, held.ID)
	assert.False(t, got.NeedsDiscussion, "and the clear lands once Done has finished")
	assert.Equal(t, ticket.StatusInProgress, got.Status, "leaving the agent working")
}

// The same gate the other way round: once the edit has committed, Done
// sees it. Sequential, so it holds with or without the lock; it pins that
// the serialized edit is a real write rather than one Done ignores.
func TestUpdate_ADoneAfterApprovalIsTurnedOnIsRefused(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder")
	held := f.ticket(t, "Ungated", "server/")
	f.claim(t, a)

	gate := true
	_, err := f.svc.Update(f.ctx, held.ID, &ticket.UpdateParams{RequiresApproval: &gate})
	require.NoError(t, err)

	_, err = f.svc.Done(f.ctx, a, &ticket.DoneParams{Summary: "built"})
	assert.ErrorIs(t, err, ticket.ErrNotApproved)
	assert.Equal(t, ticket.StatusInProgress, f.get(t, held.ID).Status)
}

// A claim reads a candidate's paths and priority under the claim lock, and
// decides on them -- whether its scope is free, and which candidate first. An edit to any of them must wait for that
// claim rather than change what it is deciding on: the paths case is the
// worst, as a claim checking a candidate's old scope could grant it over
// the new one another agent's scope overlaps. Here the new paths overlap
// nothing, so the edit lands once the claim has granted; one that overlaps
// is refused then -- see
// TestUpdate_APathsEditWaitsForAClaimAndIsCheckedAgainstWhatItGranted.
//
// Verified both ways it can break: with Update writing these fields
// outside the claim lock, every case finishes inside the claim's window;
// writing them first and taking the lock after, every case is seen written
// there.
func TestUpdate_AFieldEditWaitsForAClaimThatHasReadTheTicket(t *testing.T) {
	high := model.PriorityHigh
	cases := []struct {
		name string
		edit *ticket.UpdateParams
		// unchanged reports whether the ticket still reads as before the
		// edit.
		unchanged func(*ticket.Ticket) bool
	}{{
		name: "paths",
		edit: &ticket.UpdateParams{Paths: []model.Path{{Path: "web/"}}},
		unchanged: func(got *ticket.Ticket) bool {
			return len(got.Paths) == 1 && got.Paths[0].Path == "client/"
		},
	}, {
		name:      "priority",
		edit:      &ticket.UpdateParams{Priority: &high},
		unchanged: func(got *ticket.Ticket) bool { return got.Priority == model.PriorityNormal },
	}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			holder, claimer := f.agent(t, "holder"), f.agent(t, "claimer")
			f.ticket(t, "Held", "server/")
			f.claim(t, holder)
			candidate, err := f.svc.Create(f.ctx, &ticket.CreateParams{
				Title: "Candidate", Paths: []model.Path{{Path: "client/"}},
			})
			require.NoError(t, err)
			require.True(t, tc.unchanged(candidate), "the case's own precondition")

			h := newHoldPoint()
			defer ticket.SetClaimBarrier(h.barrier)()
			claimed := make(chan error, 1)
			go func() {
				_, err := f.svc.Claim(f.ctx, claimer, &candidate.ID)
				claimed <- err
			}()

			edited, early := f.editWhileHeld(t, h, candidate.ID, tc.edit)
			midway := f.get(t, candidate.ID)
			h.release()

			require.NoError(t, <-claimed)
			require.NoError(t, <-edited)
			assert.False(t, early, "the edit finished while the claim held the claim lock")
			assert.True(t, tc.unchanged(midway), "the edit was written under a claim deciding on the ticket")
			assert.False(t, tc.unchanged(f.get(t, candidate.ID)), "and lands once the claim has finished")
		})
	}
}

// A paths edit on a held ticket is checked against the scopes held as it
// commits. A claim parked after reading its candidate, about to grant a
// scope the edit would overlap, is one the edit waits for -- and is then
// refused by, rather than both landing and two agents holding the same
// files.
//
// Verified both ways it can break: with the check outside the claim lock,
// the edit is written inside the claim's window; with no check, it is
// written once the claim has granted.
func TestUpdate_APathsEditWaitsForAClaimAndIsCheckedAgainstWhatItGranted(t *testing.T) {
	f := newFixture(t)
	edited, _ := f.holding(t, "edited", false, "b/")
	candidate := f.ticket(t, "Candidate", "a/")
	claimer := f.agent(t, "claimer")

	h := newHoldPoint()
	defer ticket.SetClaimBarrier(h.barrier)()
	claimed := make(chan *ticket.ClaimResult, 1)
	go func() {
		result, err := f.svc.Claim(f.ctx, claimer, &candidate.ID)
		assert.NoError(t, err)
		claimed <- result
	}()

	edit, early := f.editWhileHeld(t, h, edited.ID, &ticket.UpdateParams{Paths: scopes("a/x.go")})
	midway := f.get(t, edited.ID)
	h.release()

	result := <-claimed
	editErr := <-edit
	assert.False(t, early, "the edit finished while the claim held the claim lock")
	assert.Equal(t, []string{"b/"}, pathNames(midway), "the edit was written under a claim deciding on scopes")
	require.NotNil(t, result.Ticket, "the claim, first, is granted")
	requireScopeConflict(t, editErr, edited.ID, candidate.ID, "a/")
	assert.Equal(t, []string{"b/"}, pathNames(f.get(t, edited.ID)), "and the edit, checked after it, is refused")
}

// The same edit while the holder's own exit is under way: parked after the
// release has read the ticket, the edit waits for it, and is then checked
// as the ticket lands -- open, holding nothing -- not as it was read.
//
// Verified: with the check outside the claim lock, the edit is refused on
// the lease the release was about to end.
func TestUpdate_APathsEditWaitsForAnExitAndIsCheckedAsTheTicketLands(t *testing.T) {
	f := newFixture(t)
	f.holding(t, "other", false, "a/")
	edited, holder := f.ticket(t, "Edited", "b/"), f.agent(t, "holder")
	_, err := f.svc.Claim(f.ctx, holder, &edited.ID)
	require.NoError(t, err)

	h := newHoldPoint()
	defer ticket.SetDecideBarrier(h.barrier)()
	released := make(chan error, 1)
	go func() {
		_, err := f.svc.Release(f.ctx, holder, "handing back", nil)
		released <- err
	}()

	edit, early := f.editWhileHeld(t, h, edited.ID, &ticket.UpdateParams{Paths: scopes("a/x.go")})
	midway := f.get(t, edited.ID)
	h.release()

	require.NoError(t, <-released)
	require.NoError(t, <-edit, "checked as it landed, open")
	assert.False(t, early, "the edit finished while the release held the claim lock")
	assert.Equal(t, []string{"b/"}, pathNames(midway))
	got := f.get(t, edited.ID)
	assert.Equal(t, ticket.StatusOpen, got.Status)
	assert.Equal(t, []string{"a/x.go"}, pathNames(got))
}

// A claim chooses its candidates by the roles they require, under the
// claim lock. A role change must wait for a claim that has read them,
// rather than land between that read and the grant: the claim then grants
// on the requirement it read, and the change applies to the claims after
// it.
//
// Verified: with SetRequiredRoles in its own transaction off the claim
// lock, the change is written inside the claim's window.
func TestSetRequiredRoles_WaitsForAClaimThatHasReadTheTicket(t *testing.T) {
	f := newRoleFixture(t)
	deploy := f.role(t, "deploy")
	candidate := f.ticket(t, "Candidate", "a/")
	claimer := f.agent(t, "claimer")

	h := newHoldPoint()
	defer ticket.SetClaimBarrier(h.barrier)()
	claimed := make(chan *ticket.ClaimResult, 1)
	go func() {
		result, err := f.svc.Claim(f.ctx, claimer, &candidate.ID)
		assert.NoError(t, err)
		claimed <- result
	}()

	set, early := f.whileHeld(t, h, func() error {
		_, err := f.svc.SetRequiredRoles(f.ctx, testPerson(), candidate.ID, &ticket.RoleRequirement{IDs: []uuid.UUID{deploy}})
		return err
	})
	midway := f.get(t, candidate.ID)
	h.release()

	result := <-claimed
	require.NoError(t, <-set)
	assert.False(t, early, "the role change finished while the claim held the claim lock")
	assert.Empty(t, midway.RequiredRoles, "the roles changed under a claim deciding on them")
	require.NotNil(t, result.Ticket, "the claim grants on the requirement it read")
	assert.Len(t, f.get(t, candidate.ID).RequiredRoles, 1, "and the change lands after it")
}
