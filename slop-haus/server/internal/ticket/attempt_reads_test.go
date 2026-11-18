package ticket_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// setAttempt changes the ticket's attempt 1 directly. The ticket keeps its
// own values, so what a read returns says which of the two it came from.
func (f *fixture) setAttempt(t *testing.T, ticketID int64, set string, args ...any) {
	t.Helper()
	_, err := f.database.Exec(f.ctx,
		`UPDATE attempts SET `+set+` WHERE ticket_id = $1 AND number = 1`, append([]any{ticketID}, args...)...)
	require.NoError(t, err)
}

const attemptPR = "https://github.com/o/r/pull/77"

func TestTicketReads_TakeTheMovedFieldsFromThePromotedAttempt(t *testing.T) {
	t.Parallel()
	soon := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	then := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	cases := []struct {
		name string
		set  string
	}{
		{"resume agent and affinity", "resume_agent_id = $2, affinity_until = $3"},
		{"pr url", "pr_url = $2"},
		{"approval", "approved_at = $2, approved_by_user_id = $3"},
		{"changes requested", "changes_requested_at = $2"},
		{"review started", "review_started_at = $2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			agentID := f.agent(t, "asker")
			created := f.ticket(t, "Diverged", "a/")

			switch tc.name {
			case "resume agent and affinity":
				f.setAttempt(t, created.ID, tc.set, agentID, soon)
			case "pr url":
				f.setAttempt(t, created.ID, tc.set, attemptPR)
			case "approval":
				f.setAttempt(t, created.ID, tc.set, then, f.human)
			default:
				f.setAttempt(t, created.ID, tc.set, then)
			}

			get, err := f.svc.Get(f.ctx, created.ID)
			require.NoError(t, err)
			list, err := f.svc.List(f.ctx, &ticket.ListFilter{IDs: []int64{created.ID}})
			require.NoError(t, err)
			summaries, err := f.svc.List(f.ctx, &ticket.ListFilter{IDs: []int64{created.ID}, Summary: true})
			require.NoError(t, err)
			require.Len(t, list, 1)
			require.Len(t, summaries, 1)

			for name, got := range map[string]*ticket.Ticket{"get": get, "list": &list[0], "summary": &summaries[0]} {
				switch tc.name {
				case "resume agent and affinity":
					require.NotNil(t, got.ResumeAgentID, name)
					assert.Equal(t, agentID, *got.ResumeAgentID, name)
					require.NotNil(t, got.AffinityUntil, name)
					assert.True(t, got.AffinityUntil.Equal(soon), name)
				case "pr url":
					require.NotNil(t, got.PRURL, name)
					assert.Equal(t, attemptPR, *got.PRURL, name)
				case "approval":
					require.NotNil(t, got.ApprovedAt, name)
					require.NotNil(t, got.ApprovedByUserID, name)
					assert.Equal(t, f.human, *got.ApprovedByUserID, name)
				case "changes requested":
					assert.NotNil(t, got.ChangesRequestedAt, name)
				case "review started":
					assert.NotNil(t, got.ReviewStartedAt, name)
				}
			}
		})
	}
}

func TestTicketReads_ATicketWithoutAPromotedAttemptHasNoReviewState(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	created := f.ticket(t, "Unchosen", "a/")
	f.setAttempt(t, created.ID, "pr_url = $2, promoted_at = NULL", attemptPR)

	got, err := f.svc.Get(f.ctx, created.ID)
	require.NoError(t, err)
	assert.Nil(t, got.PRURL)
	assert.Equal(t, created.Title, got.Title, "the ticket itself still reads")
}

func TestPRLinkFilter_MatchesTheAttemptsPRNotTheTickets(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	created := f.ticket(t, "Linked", "a/")
	_, err := f.database.Exec(f.ctx, `UPDATE attempts SET pr_url = 'https://github.com/o/r/pull/5' WHERE ticket_id = $1 AND number = 1`, created.ID)
	require.NoError(t, err)
	f.setAttempt(t, created.ID, "pr_url = $2", attemptPR)

	found, err := f.svc.List(f.ctx, &ticket.ListFilter{PRLink: attemptPR})
	require.NoError(t, err)
	require.Len(t, found, 1)
	assert.Equal(t, created.ID, found[0].ID)

	missed, err := f.svc.List(f.ctx, &ticket.ListFilter{PRLink: "https://github.com/o/r/pull/5"})
	require.NoError(t, err)
	assert.Empty(t, missed)
}

func TestAwaitingPostReview_ReadsApprovalFromTheAttempt(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder")
	created := f.ticket(t, "Merged unreviewed", "a/")
	_, err := f.svc.Claim(f.ctx, a, nil)
	require.NoError(t, err)
	_, err = f.svc.Done(f.ctx, a, &ticket.DoneParams{Summary: "merged"})
	require.NoError(t, err)

	awaiting := func() []ticket.Ticket {
		got, err := f.svc.List(f.ctx, &ticket.ListFilter{AwaitingPostReview: true})
		require.NoError(t, err)
		return got
	}
	require.Len(t, awaiting(), 1)

	f.setAttempt(t, created.ID, "approved_at = now(), approved_by_user_id = $2", f.human)
	assert.Empty(t, awaiting(), "an approval on the attempt has been looked at")
}

func TestClaim_AffinityIsReadFromTheAttempt(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	asker, other := f.agent(t, "asker"), f.agent(t, "other")
	created := f.ticket(t, "Waiting on its asker", "a/")
	f.setAttempt(t, created.ID, "resume_agent_id = $2, affinity_until = now() + interval '1 hour'", asker)

	got, err := f.svc.Claim(f.ctx, other, nil)
	require.NoError(t, err)
	assert.Nil(t, got.Ticket, "a live affinity for another enabled agent holds the ticket back")

	got, err = f.svc.Claim(f.ctx, asker, nil)
	require.NoError(t, err)
	require.NotNil(t, got.Ticket)
	assert.Equal(t, created.ID, got.Ticket.ID)
}

func TestClaim_AReviewHoldsItsScopeForTheAttemptsResumeAgent(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a1, a2, resume := f.agent(t, "builder-1"), f.agent(t, "builder-2"), f.agent(t, "resume")
	held := f.ticket(t, "In review", "server/")
	f.ticket(t, "Contended", "server/x.go")

	_, err := f.svc.Claim(f.ctx, a1, &held.ID)
	require.NoError(t, err)
	_, err = f.svc.Review(f.ctx, a1, &ticket.ReviewParams{Reason: "reviewed by a person", PRURL: reviewPR, Handoff: "PR open."})
	require.NoError(t, err)
	f.setAttempt(t, held.ID, "resume_agent_id = $2", resume)

	result, err := f.svc.Claim(f.ctx, a2, nil)
	require.NoError(t, err)
	require.NotNil(t, result.Blocked)
	assert.Equal(t, resume, result.Blocked.HolderAgentID)
}

func TestLockState_HolderPRIsTheAttempts(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	m := f.mergers(t, "holder")
	require.NoError(t, f.svc.AcquireIntegrationLock(f.ctx, m[0].id))
	_, err := f.database.Exec(f.ctx, `UPDATE attempts SET pr_url = 'https://github.com/o/r/pull/5' WHERE ticket_id = $1 AND number = 1`, m[0].ticket.ID)
	require.NoError(t, err)
	f.setAttempt(t, m[0].ticket.ID, "pr_url = $2", attemptPR)

	state, err := f.svc.IntegrationLockState(f.ctx)
	require.NoError(t, err)
	require.NotNil(t, state.Holder)
	assert.Equal(t, attemptPR, state.Holder.PRURL)
}

func TestHeldAttempt_NamesTheAttemptOfTheAgentsLease(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	store := newStore(f.database)
	a, b := f.agent(t, "holder"), f.agent(t, "bystander")
	created := f.ticket(t, "Held", "a/")
	other := f.ticket(t, "Not held", "b/")

	_, err := store.HeldAttempt(f.ctx, a, created.ID)
	require.ErrorIs(t, err, ticket.ErrNoLease, "before it claims")

	_, err = f.svc.Claim(f.ctx, a, &created.ID)
	require.NoError(t, err)
	ref, err := store.HeldAttempt(f.ctx, a, created.ID)
	require.NoError(t, err)
	assert.Equal(t, &ticket.AttemptRef{TicketID: created.ID, Number: 1}, ref)

	_, err = f.database.Exec(f.ctx, `INSERT INTO attempts (ticket_id, number, status) VALUES ($1, 2, 'queued')`, created.ID)
	require.NoError(t, err)
	_, err = f.database.Exec(f.ctx, `UPDATE leases SET attempt_number = 2 WHERE agent_id = $1`, a)
	require.NoError(t, err)
	ref, err = store.HeldAttempt(f.ctx, a, created.ID)
	require.NoError(t, err)
	assert.Equal(t, 2, ref.Number, "read from the lease, not assumed")

	_, err = store.HeldAttempt(f.ctx, b, created.ID)
	require.ErrorIs(t, err, ticket.ErrNoLease, "another agent's lease names nothing for this one")
	_, err = store.HeldAttempt(f.ctx, a, other.ID)
	require.ErrorIs(t, err, ticket.ErrNoLease, "nor does a lease on another ticket")

	f.expire(t, a)
	_, err = store.HeldAttempt(f.ctx, a, created.ID)
	require.ErrorIs(t, err, ticket.ErrNoLease, "a lapsed lease is not held")
}

func TestTicketReads_ChooseThePromotedAttemptAmongSeveral(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	created := f.ticket(t, "Two attempts", "a/")
	const secondPR = "https://github.com/o/r/pull/78"
	f.setAttempt(t, created.ID, "pr_url = $2", attemptPR)
	_, err := f.database.Exec(f.ctx,
		`INSERT INTO attempts (ticket_id, number, status, pr_url) VALUES ($1, 2, 'queued', $2)`, created.ID, secondPR)
	require.NoError(t, err)

	filter := &ticket.ListFilter{IDs: []int64{created.ID}}
	list, err := f.svc.List(f.ctx, filter)
	require.NoError(t, err)
	require.Len(t, list, 1, "an unpromoted attempt adds no rows")
	require.NotNil(t, list[0].PRURL)
	assert.Equal(t, attemptPR, *list[0].PRURL)

	f.setAttempt(t, created.ID, "promoted_at = NULL")
	_, err = f.database.Exec(f.ctx, `UPDATE attempts SET promoted_at = now() WHERE ticket_id = $1 AND number = 2`, created.ID)
	require.NoError(t, err)
	got, err := f.svc.Get(f.ctx, created.ID)
	require.NoError(t, err)
	require.NotNil(t, got.PRURL)
	assert.Equal(t, secondPR, *got.PRURL, "the read follows the promotion")

	counts, err := newStore(f.database).CountTickets(f.ctx, &ticket.ListFilter{PRLink: secondPR})
	require.NoError(t, err)
	assert.Equal(t, 1, counts[ticket.StatusOpen])
	counts, err = newStore(f.database).CountTickets(f.ctx, &ticket.ListFilter{PRLink: attemptPR})
	require.NoError(t, err)
	assert.Zero(t, counts[ticket.StatusOpen], "the demoted attempt's PR no longer matches")
}

// The scope a ticket in review holds is owed to the attempt that is in
// review, which need not be the promoted one.
func TestClaim_AReviewViaAShadowHoldsItsScopeForTheShadowsResumeAgent(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a1, resume := f.agent(t, "builder"), f.agent(t, "resume")
	held := f.ticket(t, "Shadow in review", "server/")
	f.addAttempt(t, held.ID, true)
	contended := f.ticket(t, "Contended", "server/x.go")
	_, err := f.database.Exec(f.ctx,
		`UPDATE attempts SET status = 'review', resume_agent_id = $2 WHERE ticket_id = $1 AND number = 2`, held.ID, resume)
	require.NoError(t, err)
	_, err = f.database.Exec(f.ctx, `UPDATE tickets SET status = 'review' WHERE id = $1`, held.ID)
	require.NoError(t, err)

	result, err := f.svc.Claim(f.ctx, a1, &contended.ID)
	require.NoError(t, err)
	require.NotNil(t, result.Blocked)
	assert.Equal(t, held.ID, result.Blocked.ConflictingTicketID)
	assert.Equal(t, resume, result.Blocked.HolderAgentID)
}
