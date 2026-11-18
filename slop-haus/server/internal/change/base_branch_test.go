package change_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/change"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost/codehosttest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// shadowOf is a second attempt on first's ticket, whose agent holds it, with
// the ticket's base pinned.
func (f *fixture) shadowOf(first *holder) *holder { return f.addSibling(first) }

func (f *fixture) baseBranchKey(ticketID int64) codehosttest.BranchKey {
	return codehosttest.BranchKey{Repo: appRepo, Branch: ticket.UnpromotedBase(ticketID)}
}

func TestBaseBranch_AnUnpromotedAttemptsChangeTargetsTheTicketsBaseBranchAtThePinnedCommit(t *testing.T) {
	f := newFixture(t)
	first := f.hold(false)
	second := f.shadowOf(first)

	f.openAs(second, 8, true)

	opens := f.host.Opens()
	require.Len(t, opens, 1)
	want := ticket.UnpromotedBase(first.ticket.ID)
	assert.Equal(t, "slop-haus-base-ticket-"+itoa(first.ticket.ID), want)
	assert.Equal(t, want, opens[0].Request.Base)
	assert.Equal(t, want, f.currentOf(first, 2).Base)
	creates := f.host.BranchCreates()
	require.Len(t, creates, 1)
	assert.Equal(t, f.baseBranchKey(first.ticket.ID), creates[0].BranchKey)
	assert.Equal(t, baseOld, creates[0].SHA, "the commit the ticket pinned")
}

func TestBaseBranch_ThePromotedAttemptsChangeAndASingleAttemptTicketsTargetMain(t *testing.T) {
	f := newFixture(t)
	first := f.hold(false)

	f.openAs(first, 7, false)

	opens := f.host.Opens()
	require.Len(t, opens, 1)
	assert.Equal(t, "main", opens[0].Request.Base)
	assert.Empty(t, f.host.BranchCreates(), "no base branch for a ticket with one attempt")
}

func TestBaseBranch_ShadowsOfOneTicketShareTheBranchAndItIsCreatedOnceAtTheSameCommit(t *testing.T) {
	f := newFixture(t)
	first := f.hold(false)
	second := f.shadowOf(first)

	f.openAs(second, 8, true)
	// A superseding change asks for the branch again.
	f.recordUnderOldName(second)
	f.host.Opened = codehost.ChangeRef{Host: hostName, Owner: "acme", Repo: "app", Number: 9}
	_, err := f.svc.Open(f.ctx, f.tenant, second.agent, &change.OpenInput{Branch: f.branchOf(second), Title: "Again", Draft: true})
	require.NoError(t, err)

	creates := f.host.BranchCreates()
	require.Len(t, creates, 2)
	assert.Equal(t, creates[0], creates[1], "a retried create is the same request, which the host answers as success")
	assert.Len(t, f.host.BranchAt, 1)
}

func TestBaseBranch_ABranchAtAnotherCommitIsAnErrorAndNothingIsOpened(t *testing.T) {
	f := newFixture(t)
	first := f.hold(false)
	second := f.shadowOf(first)
	f.host.BranchAt = map[codehosttest.BranchKey]string{f.baseBranchKey(first.ticket.ID): baseNew}

	f.host.Opened.Number = 8
	_, err := f.svc.Open(f.ctx, f.tenant, second.agent, &change.OpenInput{Branch: f.branchOf(second), Title: "T", Draft: true})

	require.ErrorIs(t, err, codehost.ErrBranchConflict)
	assert.Contains(t, err.Error(), baseNew)
	assert.Contains(t, err.Error(), baseOld)
	assert.Empty(t, f.host.Opens())
	_, err = f.store.Current(f.ctx, first.ticket.ID, 2)
	require.ErrorIs(t, err, change.ErrNoChange)
}

func TestBaseBranch_AnUnpromotedAttemptOnATicketWithNoPinnedBaseIsRefused(t *testing.T) {
	f := newFixture(t)
	first := f.hold(false)
	second := f.shadowOf(first)
	_, err := f.database.Exec(f.ctx, `UPDATE tickets SET base_commit = NULL WHERE id = $1`, first.ticket.ID)
	require.NoError(t, err)

	_, err = f.svc.Open(f.ctx, f.tenant, second.agent, &change.OpenInput{Branch: f.branchOf(second), Title: "T", Draft: true})

	require.ErrorIs(t, err, change.ErrInvalid)
	assert.Contains(t, err.Error(), "no pinned base commit")
	assert.Empty(t, f.host.BranchCreates())
	assert.Empty(t, f.host.Opens())
}

func TestBaseBranch_ACreateFailureOpensNothing(t *testing.T) {
	f := newFixture(t)
	first := f.hold(false)
	second := f.shadowOf(first)
	f.host.BranchErr = codehost.ErrUnreachable

	_, err := f.svc.Open(f.ctx, f.tenant, second.agent, &change.OpenInput{Branch: f.branchOf(second), Title: "T", Draft: true})

	require.ErrorIs(t, err, codehost.ErrUnreachable)
	assert.Empty(t, f.host.Opens())
}

func TestStops_ChangeOpenReadyIsRefusedForAnUnpromotedAttemptAndAllowedForThePromotedOne(t *testing.T) {
	f := newFixture(t)
	first := f.hold(true)
	second := f.shadowOf(first)
	f.openAs(second, 8, true)

	_, err := f.svc.Open(f.ctx, f.tenant, second.agent, &change.OpenInput{Branch: f.branchOf(second), Title: "T", Draft: false})

	require.ErrorIs(t, err, ticket.ErrAttemptAwaitsChoice)
	assert.Contains(t, err.Error(), "ticket_finish")
	assert.True(t, f.currentOf(first, 2).Draft, "the draft was not taken out of draft")
	assert.Empty(t, f.host.Readied())

	opened := f.openAs(first, 7, false)
	assert.False(t, opened.Draft, "the promoted attempt opens ready")
}

func TestStops_TheIntegrationLockIsRefusedForAnUnpromotedAttemptAndAllowedForThePromotedOne(t *testing.T) {
	f := newFixture(t)
	first := f.hold(false)
	second := f.shadowOf(first)

	for name, take := range map[string]func() error{
		"lock": func() error { return f.tickets.AcquireIntegrationLock(f.ctx, second.agent) },
		"wait": func() error { return f.tickets.AwaitIntegrationLock(f.ctx, second.agent) },
	} {
		err := take()
		require.ErrorIs(t, err, ticket.ErrAttemptAwaitsChoice, name)
		assert.Contains(t, err.Error(), "ticket_finish", name)
	}
	state, err := f.tickets.IntegrationLockState(f.ctx)
	require.NoError(t, err)
	assert.Nil(t, state.Holder, "the lock was not taken")
	assert.Empty(t, state.Queue, "the unpromoted attempt did not join the queue")

	require.NoError(t, f.tickets.AcquireIntegrationLock(f.ctx, first.agent), "the promoted attempt takes it")
}

func TestStops_AMergeIsRefusedForAnUnpromotedAttemptsDraftEvenWithTheLockInHand(t *testing.T) {
	f := newFixture(t)
	first := f.hold(false)
	second := f.shadowOf(first)
	f.openAs(second, 8, true)
	_, err := f.database.Exec(f.ctx, `
		INSERT INTO locks (name, holder_agent_id, holder_lease_id)
		SELECT $1, $2, id FROM leases WHERE agent_id = $2 AND released_at IS NULL`, ticket.IntegrationLockName, second.agent)
	require.NoError(t, err)

	_, err = f.merge(second, "")

	require.ErrorIs(t, err, ticket.ErrAttemptAwaitsChoice)
	assert.Empty(t, f.host.Merges())
}

func TestStops_AMergeOfAnUnpromotedAttemptsDraftIsRefusedAsADraftOncePromotedAndRetargeted(t *testing.T) {
	f := newFixture(t)
	first := f.hold(false)
	second := f.shadowOf(first)
	f.openAs(second, 8, true)
	f.promote(first, 2)
	_, err := f.database.Exec(f.ctx, `UPDATE ticket_changes SET base_branch = 'main' WHERE ticket_id = $1 AND attempt_number = 2`, first.ticket.ID)
	require.NoError(t, err)
	f.lock(second)

	_, err = f.merge(second, "")

	require.ErrorIs(t, err, change.ErrDraft)
	assert.Empty(t, f.host.Merges())
}

func TestBaseBranch_ReviewOfAnUnpromotedChangeIsAgainstItsOwnBaseBranch(t *testing.T) {
	f := newFixture(t)
	first := f.hold(true)
	second := f.shadowOf(first)
	f.openAs(second, 8, true)
	base := ticket.UnpromotedBase(first.ticket.ID)
	f.host.DiffOf = map[string]*codehost.Diff{headA: {Base: base, BaseSHA: baseOld, MergeBaseSHA: baseOld, Files: []codehost.FileDiff{modified("a.go", hunkOne)}}}

	_, err := f.svc.Review(f.ctx, f.tenant, second.agent, &ticket.ReviewParams{Reason: "reviewed by a person", PRURL: pr2, Handoff: "ready"})
	require.NoError(t, err)

	rec := f.currentOf(first, 2)
	assert.Equal(t, headA, rec.ReviewedHead)
	assert.Equal(t, baseOld, rec.ReviewedBase, "the diff against the ticket's base branch was fingerprinted, not refused as another base")
	var notes int
	require.NoError(t, f.database.QueryRow(f.ctx, `SELECT count(*) FROM ticket_comments WHERE ticket_id = $1 AND body LIKE '%against `+base+`%'`, first.ticket.ID).Scan(&notes))
	assert.Equal(t, 1, notes, "the hand-over note names the branch the changes are against")
}

func TestBaseBranch_ABranchNamedLikeTheBaseBranchIsRefusedAsAHead(t *testing.T) {
	f := newFixture(t)
	first := f.hold(false)
	second := f.shadowOf(first)

	_, err := f.svc.Open(f.ctx, f.tenant, second.agent, &change.OpenInput{Branch: ticket.UnpromotedBase(first.ticket.ID), Title: "T", Draft: true})

	require.ErrorIs(t, err, change.ErrInvalid)
	assert.Empty(t, f.host.BranchCreates())
	assert.Empty(t, f.host.Opens())
}
