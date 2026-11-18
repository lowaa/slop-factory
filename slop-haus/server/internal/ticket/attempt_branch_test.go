package ticket_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost/codehosttest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/scope"
)

func (r *pinRig) claimResult(t *testing.T, agent uuid.UUID) *ticket.ClaimResult {
	t.Helper()
	res, err := r.svc.Claim(r.ctx, agent, nil)
	require.NoError(t, err)
	require.NotNil(t, res)
	require.NotNil(t, res.Ticket)
	return res
}

func (r *pinRig) branchKey(name string) codehosttest.BranchKey {
	return codehosttest.BranchKey{Repo: acmeApp, Branch: name}
}

func (r *pinRig) branchCreatedAt(t *testing.T, id int64, number int) *time.Time {
	t.Helper()
	var at *time.Time
	require.NoError(t, r.database.QueryRow(r.ctx,
		`SELECT branch_created_at FROM attempts WHERE ticket_id = $1 AND number = $2`, id, number).Scan(&at))
	return at
}

func TestClaim_MakesTheFirstAttemptsBranchAtTheBaseCommit(t *testing.T) {
	t.Parallel()
	r := newPinRig(t)
	tk := r.ticket(t, "Branch Me", "a/")

	res := r.claimResult(t, r.agent(t, "worker"))

	name := ticket.BranchName(tk.ID, 1)
	require.NotNil(t, res.Branch)
	assert.Equal(t, &ticket.AttemptBranch{Name: name, Managed: true, Ready: true}, res.Branch, "the promoted attempt is not Unpromoted")
	assert.Equal(t, headOne, r.host.BranchAt[r.branchKey(name)])
	assert.NotContains(t, r.host.BranchAt, r.branchKey(ticket.UnpromotedBase(tk.ID)), "the promoted attempt needs no base branch")
	assert.NotNil(t, r.branchCreatedAt(t, tk.ID, 1))
}

func TestClaim_ShadowsClaimMakesItsBranchAndTheTicketsBaseBranch(t *testing.T) {
	t.Parallel()
	r := newPinRig(t)
	tk := r.ticket(t, "Shadowed", "a/")
	first := r.agent(t, "first")
	r.claimResult(t, first)
	require.Equal(t, 2, r.addAttempt(t, tk.ID, true))

	res := r.claimResult(t, r.agent(t, "second"))

	name := ticket.BranchName(tk.ID, 2)
	require.NotNil(t, res.Branch)
	assert.True(t, res.Branch.Ready)
	assert.True(t, res.Branch.Unpromoted, "a shadow is told to catch up from the base branch")
	assert.Equal(t, name, res.Branch.Name)
	assert.Equal(t, headOne, r.host.BranchAt[r.branchKey(name)])
	assert.Equal(t, headOne, r.host.BranchAt[r.branchKey(ticket.UnpromotedBase(tk.ID))])
}

func TestClaim_AReclaimNeverMovesABranchThatHasAdvanced(t *testing.T) {
	t.Parallel()
	r := newPinRig(t)
	tk := r.ticket(t, "Advanced", "a/")
	first, second := r.agent(t, "first"), r.agent(t, "second")
	name := ticket.BranchName(tk.ID, 1)
	r.claimResult(t, first)
	// The first agent pushed work, and main moved on.
	r.host.BranchAt[r.branchKey(name)] = headTwo
	r.host.HeadOf[acmeApp] = &codehost.BranchHead{Branch: "main", SHA: "cccccccccccccccccccccccccccccccccccccccc"}
	r.release(t, first)

	t.Run("recorded as made: no host call at all", func(t *testing.T) {
		before := len(r.host.BranchCreates())

		res := r.claimResult(t, second)

		assert.True(t, res.Branch.Ready)
		assert.Equal(t, headTwo, r.host.BranchAt[r.branchKey(name)])
		assert.Len(t, r.host.BranchCreates(), before)
		r.release(t, second)
	})

	t.Run("not recorded (the record was lost): ensure leaves it where it is", func(t *testing.T) {
		_, err := r.database.Exec(r.ctx, `UPDATE attempts SET branch_created_at = NULL WHERE ticket_id = $1`, tk.ID)
		require.NoError(t, err)

		res := r.claimResult(t, second)

		assert.True(t, res.Branch.Ready)
		assert.Equal(t, headTwo, r.host.BranchAt[r.branchKey(name)], "the branch is not moved")
		assert.NotNil(t, r.branchCreatedAt(t, tk.ID, 1))
	})
}

func TestClaim_AHostThatIsDownLeavesTheBranchNotReadyAndTicketGetMakesIt(t *testing.T) {
	t.Parallel()
	r := newPinRig(t)
	tk := r.ticket(t, "Down", "a/")
	agent := r.agent(t, "worker")
	r.host.BranchErr = codehost.ErrUnreachable

	res := r.claimResult(t, agent)

	require.NotNil(t, res.Branch, "the claim stands")
	assert.Equal(t, &ticket.AttemptBranch{Name: ticket.BranchName(tk.ID, 1), Managed: true}, res.Branch)
	assert.Nil(t, r.branchCreatedAt(t, tk.ID, 1))

	r.host.BranchErr = nil
	detail, err := r.svc.DetailFor(r.ctx, tk.ID, agent)
	require.NoError(t, err)

	require.NotNil(t, detail.Branch)
	assert.True(t, detail.Branch.Ready)
	assert.Equal(t, headOne, r.host.BranchAt[r.branchKey(res.Branch.Name)])
	assert.NotNil(t, r.branchCreatedAt(t, tk.ID, 1))
}

func TestDetailFor_NamesNoBranchToAnAgentThatHoldsNothing(t *testing.T) {
	t.Parallel()
	r := newPinRig(t)
	tk := r.ticket(t, "Not held", "a/")

	detail, err := r.svc.DetailFor(r.ctx, tk.ID, r.agent(t, "reader"))

	require.NoError(t, err)
	assert.Nil(t, detail.Branch)
	assert.Empty(t, r.host.BranchCreates())
}

func TestClaim_ATicketThatCannotBePinnedFallsBackToTheAgentMadeBranch(t *testing.T) {
	t.Parallel()
	r := newPinRig(t)
	tk := r.ticket(t, "Unpinned", "a/")
	r.ws.repos = []scope.WorkspaceRepo{{Path: "/", Remote: "gitlab.example/acme/app"}}

	res := r.claimResult(t, r.agent(t, "worker"))

	assert.Equal(t, &ticket.AttemptBranch{Name: ticket.BranchName(tk.ID, 1)}, res.Branch)
	assert.Empty(t, r.host.BranchCreates())
	assert.Nil(t, r.branchCreatedAt(t, tk.ID, 1))
}

func TestClaim_AnOldBaseStartsEveryBranchAtItNotAtTheDefaultBranchHead(t *testing.T) {
	t.Parallel()
	r := newPinRig(t)
	r.knownRefs()
	tk, err := r.createWithRef(t, "v1.2.0")
	require.NoError(t, err)
	require.NotEqual(t, headOne, commitX)
	first := r.agent(t, "first")
	res := r.claimResult(t, first)
	require.Equal(t, tk.ID, res.Ticket.ID)
	require.Equal(t, 2, r.addAttempt(t, tk.ID, true))

	shadow := r.claimResult(t, r.agent(t, "second"))

	assert.Equal(t, commitX, r.host.BranchAt[r.branchKey(ticket.BranchName(tk.ID, 1))])
	assert.Equal(t, commitX, r.host.BranchAt[r.branchKey(ticket.BranchName(tk.ID, 2))])
	assert.Equal(t, commitX, r.host.BranchAt[r.branchKey(ticket.UnpromotedBase(tk.ID))])
	assert.True(t, shadow.Branch.Unpromoted)
}
