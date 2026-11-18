package change_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/change"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

// holdSplitParent is an agent holding a ticket that was split into one part
// per status given, each part moved to that status before the claim.
func (f *fixture) holdSplitParent(partStatuses ...ticket.Status) (*holder, []int64) {
	f.t.Helper()
	var parts []int64
	for range partStatuses {
		part, err := f.tickets.Create(f.ctx, &ticket.CreateParams{
			Title: "part", Paths: []model.Path{{Path: "app/other/"}}, Status: ticket.StatusOpen, CreatedByUserID: &f.creator,
		})
		require.NoError(f.t, err)
		parts = append(parts, part.ID)
	}
	parent, err := f.tickets.Create(f.ctx, &ticket.CreateParams{
		Title: "split parent", Paths: []model.Path{{Path: "app/server/"}}, Status: ticket.StatusOpen,
		CreatedByUserID: &f.creator, BlockedOn: parts,
	})
	require.NoError(f.t, err)
	for i, status := range partStatuses {
		if status != ticket.StatusOpen {
			f.settle(parts[i], status)
		}
	}
	return f.claimParent(parent.ID), parts
}

// settle moves a ticket to a terminal status the way its work would: an
// agent holding it finishes it, or a person cancels it.
func (f *fixture) settle(id int64, status ticket.Status) {
	f.t.Helper()
	if status == ticket.StatusCancelled {
		_, err := f.tickets.MoveMany(f.ctx, []int64{id}, status)
		require.NoError(f.t, err)
		return
	}
	a := f.newAgent("part-worker")
	claimed, err := f.tickets.Claim(f.ctx, a, &id)
	require.NoError(f.t, err)
	require.NotNil(f.t, claimed.Ticket)
	_, err = f.tickets.Done(f.ctx, a, &ticket.DoneParams{Summary: "landed"})
	require.NoError(f.t, err)
}

func (f *fixture) claimParent(id int64) *holder {
	f.t.Helper()
	a := f.newAgent("parent-owner")
	claimed, err := f.tickets.Claim(f.ctx, a, &id)
	require.NoError(f.t, err)
	require.NotNil(f.t, claimed.Ticket, "the parent is claimable once its parts are settled")
	return &holder{agent: a, ticket: claimed.Ticket, attempt: claimed.Attempt.Number}
}

// pin records the base commit a claim pins when the host answers. Written
// directly: the claim's pinning needs the workspace wiring, which is not what
// is under test.
func (f *fixture) pin(h *holder) {
	f.t.Helper()
	_, err := f.database.Exec(f.ctx, `UPDATE tickets SET base_commit = $2 WHERE id = $1`, h.ticket.ID, baseOld)
	require.NoError(f.t, err)
}

// onHost says the holder's branch is on the host with n commits past the base.
func (f *fixture) onHost(h *holder, n int) {
	f.pin(h)
	f.host.AheadOf = map[string]int{f.branchOf(h): n}
}

func (f *fixture) finishNoChange(h *holder, reason string) (*ticket.Ticket, error) {
	return f.svc.FinishWithoutChange(f.ctx, f.tenant, h.agent, reason)
}

func TestFinishWithoutChange_ClosesASplitParentWhoseAllPartsAreSettled(t *testing.T) {
	f := newFixture(t)
	h, _ := f.holdSplitParent(ticket.StatusDone, ticket.StatusCancelled)
	f.onHost(h, 0)

	closed, err := f.finishNoChange(h, "all parts landed in #1 and #2")

	require.NoError(t, err)
	assert.Equal(t, ticket.StatusDone, closed.Status)
	assert.Nil(t, closed.PRURL)
	comments, err := f.tickets.ListComments(f.ctx, h.ticket.ID)
	require.NoError(t, err)
	var bodies []string
	for _, c := range comments {
		bodies = append(bodies, c.Body)
	}
	assert.Contains(t, bodies, "all parts landed in #1 and #2", "the reason is the closing comment")
	lines := f.log.audited(t, audit.TicketClosedNoChange)
	require.Len(t, lines, 1)
	assert.Equal(t, "all parts landed in #1 and #2", lines[0]["reason"])
}

func TestFinishWithoutChange_RefusesWhileAPartIsOpen(t *testing.T) {
	f := newFixture(t)
	h, parts := f.holdSplitParent(ticket.StatusDone)
	f.onHost(h, 0)
	late, err := f.tickets.Create(f.ctx, &ticket.CreateParams{
		Title: "late part", Paths: []model.Path{{Path: "app/other/"}}, Status: ticket.StatusOpen, CreatedByUserID: &f.creator,
	})
	require.NoError(t, err)
	_, err = f.tickets.AddDependency(f.ctx, ticket.UserAuthor(f.creator), h.ticket.ID, late.ID)
	require.NoError(t, err)
	require.Len(t, parts, 1)

	_, err = f.finishNoChange(h, "done")

	require.ErrorIs(t, err, change.ErrPartsOpen)
	assert.Contains(t, err.Error(), "#"+itoa(late.ID))
}

func TestFinishWithoutChange_RefusesABranchWithCommits(t *testing.T) {
	f := newFixture(t)
	h := f.hold(false)
	f.onHost(h, 2)

	_, err := f.finishNoChange(h, "nothing to do")

	require.ErrorIs(t, err, change.ErrBranchHasCommits)
	assert.Contains(t, err.Error(), f.branchOf(h))
	still, getErr := f.tickets.Get(f.ctx, h.ticket.ID)
	require.NoError(t, getErr)
	assert.NotEqual(t, ticket.StatusDone, still.Status)
}

func TestFinishWithoutChange_RefusesAnAttemptWithAChange(t *testing.T) {
	f := newFixture(t)
	h := f.hold(false)
	f.onHost(h, 0)
	f.open(h, true)

	_, err := f.finishNoChange(h, "nothing to do")

	require.ErrorIs(t, err, change.ErrHasChange)
	assert.Contains(t, err.Error(), mergedURL)
}

func TestFinishWithoutChange_ClosesAnInvestigationWithNoCommits(t *testing.T) {
	f := newFixture(t)
	h := f.hold(false)
	f.onHost(h, 0)

	closed, err := f.finishNoChange(h, "the behaviour is intended")

	require.NoError(t, err)
	assert.Equal(t, ticket.StatusDone, closed.Status)
}

func TestFinishWithoutChange_NeedsAReason(t *testing.T) {
	f := newFixture(t)
	h := f.hold(false)
	f.onHost(h, 0)

	_, err := f.finishNoChange(h, "  ")

	require.ErrorIs(t, err, change.ErrNoChangeReason)
}

func TestFinishWithoutChange_AHostThatCannotAnswerIsNotAPass(t *testing.T) {
	f := newFixture(t)
	h := f.hold(false)
	f.pin(h)
	f.host.AheadErr = codehost.ErrUnreachable

	_, err := f.finishNoChange(h, "nothing to do")

	require.ErrorIs(t, err, change.ErrNoChangeUnverified)
}

func TestFinishWithoutChange_ABranchNotOnTheHostIsNotAPass(t *testing.T) {
	f := newFixture(t)
	h := f.hold(false)
	f.pin(h)

	_, err := f.finishNoChange(h, "nothing to do")

	require.ErrorIs(t, err, change.ErrNoChangeUnverified)
}

func TestFinishWithoutChange_ATicketWithNoPinnedBaseIsNotAPass(t *testing.T) {
	f := newFixture(t)
	h := f.hold(false)
	f.host.AheadOf = map[string]int{f.branchOf(h): 0}

	_, err := f.finishNoChange(h, "nothing to do")

	require.ErrorIs(t, err, change.ErrNoChangeUnverified)
}

func TestFinishWithoutChange_AnUnpromotedAttemptCannotCloseTheTicket(t *testing.T) {
	f := newFixture(t)
	first := f.hold(false)
	second := f.shadowOf(first)
	f.onHost(second, 0)

	_, err := f.finishNoChange(second, "nothing to do")

	require.ErrorIs(t, err, change.ErrNoChangeUnpromoted)
	still, getErr := f.tickets.Get(f.ctx, first.ticket.ID)
	require.NoError(t, getErr)
	assert.NotEqual(t, ticket.StatusDone, still.Status)
}

// holdPathless is an agent holding a ticket with no paths, as a CI-flake
// report or an investigation is filed: it is never placed, so it has no base
// pin and no branch.
func (f *fixture) holdPathless() *holder {
	f.t.Helper()
	a := f.newAgent("flake-worker")
	created, err := f.tickets.Create(f.ctx, &ticket.CreateParams{
		Title: "CI flake: x", Status: ticket.StatusOpen, CreatedByUserID: &f.creator,
	})
	require.NoError(f.t, err)
	claimed, err := f.tickets.Claim(f.ctx, a, &created.ID)
	require.NoError(f.t, err)
	require.NotNil(f.t, claimed.Ticket)
	require.Nil(f.t, claimed.Ticket.BaseCommit, "a ticket with no paths is not pinned")
	return &holder{agent: a, ticket: claimed.Ticket, attempt: claimed.Attempt.Number}
}

func TestFinishWithoutChange_ClosesAPathlessTicketWithNoPinnedBase(t *testing.T) {
	f := newFixture(t)
	h := f.holdPathless()

	closed, err := f.finishNoChange(h, "fixed through #969")

	require.NoError(t, err)
	assert.Equal(t, ticket.StatusDone, closed.Status)
	lines := f.log.audited(t, audit.TicketClosedNoChange)
	require.Len(t, lines, 1)
	assert.Equal(t, "fixed through #969", lines[0]["reason"])
}

func TestFinishWithoutChange_APathlessTicketWithAChangeIsRefused(t *testing.T) {
	f := newFixture(t)
	h := f.holdPathless()
	_, err := f.database.Exec(f.ctx,
		`UPDATE attempts SET pr_url = $3 WHERE ticket_id = $1 AND number = $2`, h.ticket.ID, h.attempt, mergedURL)
	require.NoError(t, err)

	_, err = f.finishNoChange(h, "nothing to do")

	require.ErrorIs(t, err, change.ErrHasChange)
	assert.Contains(t, err.Error(), mergedURL)
}
