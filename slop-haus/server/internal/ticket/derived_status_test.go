package ticket_test

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

func (f *fixture) attemptStatuses(t *testing.T, id int64) map[int]string {
	t.Helper()
	rows, err := f.database.Query(f.ctx, `SELECT number, status FROM attempts WHERE ticket_id = $1`, id)
	require.NoError(t, err)
	defer rows.Close()
	got := map[int]string{}
	for rows.Next() {
		var n int
		var status string
		require.NoError(t, rows.Scan(&n, &status))
		got[n] = status
	}
	require.NoError(t, rows.Err())
	return got
}

// attemptHistory is attempt_status_changes as "<attempt>:<from>><to>" in the
// order they were written.
func (f *fixture) attemptHistory(t *testing.T, id int64) []string {
	t.Helper()
	rows, err := f.database.Query(f.ctx, `
		SELECT attempt_number, COALESCE(from_status, ''), to_status
		  FROM attempt_status_changes WHERE ticket_id = $1 ORDER BY id`, id)
	require.NoError(t, err)
	defer rows.Close()
	var got []string
	for rows.Next() {
		var n int
		var from, to string
		require.NoError(t, rows.Scan(&n, &from, &to))
		got = append(got, fmt.Sprintf("%d:%s>%s", n, from, to))
	}
	require.NoError(t, rows.Err())
	return got
}

func (f *fixture) ticketHistory(t *testing.T, id int64) []string {
	t.Helper()
	var got []string
	for _, c := range f.history(t, id) {
		got = append(got, c.From+">"+c.To)
	}
	return got
}

// unpromote leaves the ticket with no promoted attempt, the state before any
// attempt has started that the add-attempt endpoint will create.
func (f *fixture) unpromote(t *testing.T, id int64) {
	t.Helper()
	_, err := f.database.Exec(f.ctx, `UPDATE attempts SET promoted_at = NULL WHERE ticket_id = $1`, id)
	require.NoError(t, err)
}

func (f *fixture) setAttemptStatus(t *testing.T, id int64, number int, status string) {
	t.Helper()
	_, err := f.database.Exec(f.ctx, `UPDATE attempts SET status = $3 WHERE ticket_id = $1 AND number = $2`, id, number, status)
	require.NoError(t, err)
}

// running is a ticket whose attempts 1 and 2 are held by two agents.
func (f *fixture) running(t *testing.T) (id int64, a1, a2 uuid.UUID) {
	t.Helper()
	a1, a2 = f.agent(t, "first"), f.agent(t, "second")
	made := f.ticket(t, "Two attempts", "a/")
	f.addAttempt(t, made.ID, false)
	require.NotNil(t, f.claim(t, a1))
	require.NotNil(t, f.claim(t, a2))
	require.Equal(t, 1, f.leasedAttempt(t, a1))
	require.Equal(t, 2, f.leasedAttempt(t, a2))
	return made.ID, a1, a2
}

// The ticket's status is worked out from its attempts: every row of the
// table, with the attempt and ticket histories each transition wrote.
func TestDerivedStatus_Table(t *testing.T) {
	t.Parallel()
	type tc struct {
		name           string
		run            func(t *testing.T, f *fixture) int64
		attempts       map[int]string
		ticket         ticket.Status
		attemptChanges []string
		ticketChanges  []string
	}
	cases := []tc{
		{"one attempt claimed", func(t *testing.T, f *fixture) int64 {
			made := f.ticket(t, "One", "a/")
			require.NotNil(t, f.claim(t, f.agent(t, "a")))
			return made.ID
		}, map[int]string{1: "in_progress"}, ticket.StatusInProgress,
			[]string{"1:>queued", "1:queued>in_progress"}, []string{">open", "open>in_progress"}},
		{"one attempt released", func(t *testing.T, f *fixture) int64 {
			made := f.ticket(t, "One", "a/")
			a := f.agent(t, "a")
			f.claim(t, a)
			f.release(t, a)
			return made.ID
		}, map[int]string{1: "queued"}, ticket.StatusOpen,
			[]string{"1:>queued", "1:queued>in_progress", "1:in_progress>queued"}, []string{">open", "open>in_progress", "in_progress>open"}},
		{"one attempt in discussion", func(t *testing.T, f *fixture) int64 {
			made := f.ticket(t, "One", "a/")
			a := f.agent(t, "a")
			f.claim(t, a)
			f.discuss(t, a)
			return made.ID
		}, map[int]string{1: "discussion"}, ticket.StatusDiscussion,
			[]string{"1:>queued", "1:queued>in_progress", "1:in_progress>discussion"}, []string{">open", "open>in_progress", "in_progress>discussion"}},
		{"one attempt in review", func(t *testing.T, f *fixture) int64 {
			made := f.gated(t, "One", "a/")
			a := f.agent(t, "a")
			f.claim(t, a)
			require.NoError(t, f.svc.AcquireIntegrationLock(f.ctx, a))
			f.review(t, a)
			return made.ID
		}, map[int]string{1: "review"}, ticket.StatusReview,
			[]string{"1:>queued", "1:queued>in_progress", "1:in_progress>review"}, []string{">open", "open>in_progress", "in_progress>review"}},
		{"promoted attempt done", func(t *testing.T, f *fixture) int64 {
			made := f.ticket(t, "One", "a/")
			a := f.agent(t, "a")
			f.claim(t, a)
			_, err := f.svc.Done(f.ctx, a, &ticket.DoneParams{Summary: "Done."})
			require.NoError(t, err)
			return made.ID
		}, map[int]string{1: "done"}, ticket.StatusDone,
			[]string{"1:>queued", "1:queued>in_progress", "1:in_progress>done"}, []string{">open", "open>in_progress", "in_progress>done"}},
		{"two attempts running", func(t *testing.T, f *fixture) int64 {
			id, _, _ := f.running(t)
			return id
		}, map[int]string{1: "in_progress", 2: "in_progress"}, ticket.StatusInProgress,
			[]string{"1:>queued", "2:>queued", "1:queued>in_progress", "2:queued>in_progress"}, []string{">open", "open>in_progress"}},
		{"a sibling that is done does not finish the ticket", func(t *testing.T, f *fixture) int64 {
			id, _, a2 := f.running(t)
			_, err := f.svc.Done(f.ctx, a2, &ticket.DoneParams{Summary: "Done."})
			require.NoError(t, err)
			return id
		}, map[int]string{1: "in_progress", 2: "done"}, ticket.StatusInProgress,
			[]string{"1:>queued", "2:>queued", "1:queued>in_progress", "2:queued>in_progress", "2:in_progress>done"}, []string{">open", "open>in_progress"}},
		{"promoted done outranks a running sibling", func(t *testing.T, f *fixture) int64 {
			id, a1, _ := f.running(t)
			_, err := f.svc.Done(f.ctx, a1, &ticket.DoneParams{Summary: "Done."})
			require.NoError(t, err)
			return id
		}, map[int]string{1: "done", 2: "in_progress"}, ticket.StatusDone,
			[]string{"1:>queued", "2:>queued", "1:queued>in_progress", "2:queued>in_progress", "1:in_progress>done"}, []string{">open", "open>in_progress", "in_progress>done"}},
		{"discussion outranks queued: the queued attempt stays claimable", func(t *testing.T, f *fixture) int64 {
			id, a1, a2 := f.running(t)
			f.release(t, a1)
			f.discuss(t, a2)
			assert.NotNil(t, f.peek(t, f.agent(t, "third")).Ticket, "attempt 1 can still be claimed")
			return id
		}, map[int]string{1: "queued", 2: "discussion"}, ticket.StatusDiscussion,
			[]string{"1:>queued", "2:>queued", "1:queued>in_progress", "2:queued>in_progress", "1:in_progress>queued", "2:in_progress>discussion"},
			[]string{">open", "open>in_progress", "in_progress>discussion"}},
		{"discussion outranks review", func(t *testing.T, f *fixture) int64 {
			id, a1, a2 := f.running(t)
			f.discuss(t, a2)
			_, err := f.svc.Review(f.ctx, a1, &ticket.ReviewParams{Reason: "reviewed by a person", PRURL: reviewPR, Handoff: "PR open."})
			require.NoError(t, err)
			return id
		}, map[int]string{1: "review", 2: "discussion"}, ticket.StatusDiscussion,
			[]string{"1:>queued", "2:>queued", "1:queued>in_progress", "2:queued>in_progress", "2:in_progress>discussion", "1:in_progress>review"},
			[]string{">open", "open>in_progress", "in_progress>discussion"}},
		{"review with a sibling in review too", func(t *testing.T, f *fixture) int64 {
			id, a1, a2 := f.running(t)
			_, err := f.svc.Review(f.ctx, a2, &ticket.ReviewParams{Reason: "reviewed by a person", PRURL: reviewPR, Handoff: "PR open."})
			require.NoError(t, err)
			_, err = f.svc.Review(f.ctx, a1, &ticket.ReviewParams{Reason: "reviewed by a person", PRURL: reviewPR, Handoff: "PR open."})
			require.NoError(t, err)
			return id
		}, map[int]string{1: "review", 2: "review"}, ticket.StatusReview,
			[]string{"1:>queued", "2:>queued", "1:queued>in_progress", "2:queued>in_progress", "2:in_progress>review", "1:in_progress>review"},
			[]string{">open", "open>in_progress", "in_progress>review"}},
		{"lease expiry of attempt 2 while attempt 1 runs", func(t *testing.T, f *fixture) int64 {
			id, _, a2 := f.running(t)
			f.expire(t, a2)
			third := f.agent(t, "third")
			require.NotNil(t, f.claim(t, third), "the expired attempt is offered again")
			assert.Equal(t, 2, f.leasedAttempt(t, third))
			return id
		}, map[int]string{1: "in_progress", 2: "in_progress"}, ticket.StatusInProgress,
			[]string{"1:>queued", "2:>queued", "1:queued>in_progress", "2:queued>in_progress", "2:in_progress>queued", "2:queued>in_progress"},
			[]string{">open", "open>in_progress"}},
		{"the disable path releases attempt 2 while attempt 1 runs", func(t *testing.T, f *fixture) int64 {
			id, _, a2 := f.running(t)
			require.NoError(t, f.svc.ReleaseLeasesFor(f.ctx, a2, "disabled", time.Now().Add(time.Hour)))
			third := f.agent(t, "third")
			require.NotNil(t, f.claim(t, third))
			assert.Equal(t, 2, f.leasedAttempt(t, third))
			return id
		}, map[int]string{1: "in_progress", 2: "in_progress"}, ticket.StatusInProgress,
			[]string{"1:>queued", "2:>queued", "1:queued>in_progress", "2:queued>in_progress", "2:in_progress>queued", "2:queued>in_progress"},
			[]string{">open", "open>in_progress"}},
		{"both leases expire", func(t *testing.T, f *fixture) int64 {
			id, a1, a2 := f.running(t)
			f.expire(t, a1)
			f.expire(t, a2)
			f.claim(t, f.agent(t, "third")) // runs the sweep
			return id
		}, map[int]string{1: "in_progress", 2: "queued"}, ticket.StatusInProgress,
			[]string{"1:>queued", "2:>queued", "1:queued>in_progress", "2:queued>in_progress", "1:in_progress>queued", "2:in_progress>queued", "1:queued>in_progress"},
			[]string{">open", "open>in_progress", "in_progress>open", "open>in_progress"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			id := c.run(t, f)
			assert.Equal(t, c.attempts, f.attemptStatuses(t, id))
			assert.Equal(t, c.ticket, f.status(t, id))
			assert.Equal(t, c.attemptChanges, f.attemptHistory(t, id))
			assert.Equal(t, c.ticketChanges, f.ticketHistory(t, id))
		})
	}
}

// A ticket with nothing live and no attempt promoted has no status to show;
// the write is refused, naming the ticket, and nothing changes.
func TestDerivedStatus_NothingLiveAndNonePromotedIsRefused(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	made := f.ticket(t, "Abandoned", "a/")
	f.addAttempt(t, made.ID, false)
	f.unpromote(t, made.ID)
	f.setAttemptStatus(t, made.ID, 1, "closed")
	f.setAttemptStatus(t, made.ID, 2, "closed")
	attempts, attemptHistory, ticketHistory := f.attemptStatuses(t, made.ID), f.attemptHistory(t, made.ID), f.ticketHistory(t, made.ID)

	_, err := f.setStatus(t, made.ID, ticket.StatusInProgress)

	require.ErrorIs(t, err, ticket.ErrNoLiveAttempt)
	assert.Contains(t, err.Error(), fmt.Sprintf("#%d", made.ID))
	assert.Equal(t, ticket.StatusOpen, f.status(t, made.ID), "rolled back")
	assert.Equal(t, attempts, f.attemptStatuses(t, made.ID))
	assert.Equal(t, attemptHistory, f.attemptHistory(t, made.ID))
	assert.Equal(t, ticketHistory, f.ticketHistory(t, made.ID))
}

// A person's move with no attempt promoted writes every unfinished attempt,
// and leaves the finished ones.
func TestDerivedStatus_APersonsMoveWithNonePromotedWritesEveryUnfinishedAttempt(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	made := f.ticket(t, "Unchosen", "a/")
	f.addAttempt(t, made.ID, false)
	f.addAttempt(t, made.ID, false)
	f.unpromote(t, made.ID)
	f.setAttemptStatus(t, made.ID, 3, "closed")

	_, err := f.setStatus(t, made.ID, ticket.StatusInProgress)
	require.NoError(t, err)

	assert.Equal(t, map[int]string{1: "in_progress", 2: "in_progress", 3: "closed"}, f.attemptStatuses(t, made.ID))
	assert.Equal(t, ticket.StatusInProgress, f.status(t, made.ID))
}

func TestDerivedStatus_CancelClosesEveryLiveAttempt(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	id, _, _ := f.running(t)
	f.addAttempt(t, id, true)

	_, err := f.setStatus(t, id, ticket.StatusCancelled)
	require.NoError(t, err)

	assert.Equal(t, map[int]string{1: "closed", 2: "closed", 3: "closed"}, f.attemptStatuses(t, id))
	assert.Equal(t, ticket.StatusCancelled, f.status(t, id))
}

// A person's move writes the promoted attempt; a sibling whose lease the move
// takes away goes back to the queue rather than being left in progress with
// no one to claim it.
func TestDerivedStatus_APersonsMoveRequeuesTheSiblingItRevokes(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	id, _, a2 := f.running(t)

	_, err := f.setStatus(t, id, ticket.StatusDiscussion)
	require.NoError(t, err)

	assert.Equal(t, map[int]string{1: "discussion", 2: "queued"}, f.attemptStatuses(t, id))
	assert.Equal(t, ticket.StatusDiscussion, f.status(t, id), "attempt 1 waits on a person; attempt 2 is still claimable")
	require.NotNil(t, f.claim(t, a2))
	assert.Equal(t, 2, f.leasedAttempt(t, a2))
}

func TestDerivedStatus_CommentsAndVariantWithNonePromoted(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	made := f.ticket(t, "Unchosen", "a/")
	f.addAttempt(t, made.ID, false)
	f.unpromote(t, made.ID)
	// Work has started, so a person's comment would otherwise go to the promoted attempt.
	_, err := f.database.Exec(f.ctx, `UPDATE attempts SET status = 'in_progress' WHERE ticket_id = $1 AND number = 1`, made.ID)
	require.NoError(t, err)
	_, err = f.database.Exec(f.ctx, `INSERT INTO ticket_status_changes (tenant_id, ticket_id, from_status, to_status, priority, changed_at, actor_kind)
		SELECT tenant_id, id, 'open', 'in_progress', priority, now(), 'system' FROM tickets WHERE id = $1`, made.ID)
	require.NoError(t, err)

	_, err = f.svc.AddComment(f.ctx, made.ID, ticket.UserAuthor(f.human), "A word.")
	require.NoError(t, err)
	var attemptNumber *int
	require.NoError(t, f.database.QueryRow(f.ctx,
		`SELECT attempt_number FROM ticket_comments WHERE ticket_id = $1 AND body = 'A word.'`, made.ID).Scan(&attemptNumber))
	assert.Nil(t, attemptNumber, "the comment is the ticket's")

	_, err = f.svc.SetAttemptVariant(f.ctx, f.human, made.ID, nil)
	require.ErrorIs(t, err, ticket.ErrAttemptRequired)
}

// The streak is the ticket's: a release by attempt 2 holds the whole ticket
// out of the queue, whatever attempt 1 is doing.
func TestDerivedStatus_ACooldownReleaseByAnotherAttempt(t *testing.T) {
	t.Parallel()
	f := newCooldownFixture(t, testPolicy)
	a1, a2, third := f.agent(t, "first"), f.agent(t, "second"), f.agent(t, "third")
	made := f.ticket(t, "Gated", "a/")
	f.addAttempt(t, made.ID, false)
	f.clock.set(time.Now())
	require.NotNil(t, f.claim(t, a1))
	require.NotNil(t, f.claim(t, a2))

	out, err := f.svc.ReleaseWith(f.ctx, a2, ticket.ReleaseParams{Reason: "gate not met"})
	require.NoError(t, err)
	require.NotNil(t, out.RetryAt)

	assert.Equal(t, map[int]string{1: "in_progress", 2: "queued"}, f.attemptStatuses(t, made.ID))
	assert.Equal(t, ticket.StatusInProgress, f.status(t, made.ID), "attempt 1 is running")
	_, until := f.streak(t, made.ID)
	require.NotNil(t, until, "the cooldown is on the ticket")
	assert.False(t, f.claimable(t, third), "and no attempt of it is offered")

	out, err = f.svc.ReleaseWith(f.ctx, a1, ticket.ReleaseParams{Reason: "gate not met"})
	require.NoError(t, err)
	require.NotNil(t, out.RetryAt)
	assert.Equal(t, map[int]string{1: "queued", 2: "queued"}, f.attemptStatuses(t, made.ID))
	assert.Equal(t, ticket.StatusBlocked, f.status(t, made.ID), "nothing runs, and the cooldown holds it")
}

func TestDerivedStatus_EscalationMovesTheReleasingAttempt(t *testing.T) {
	t.Parallel()
	policy := ticket.DefaultCooldownPolicy()
	policy.EscalateAfter = 1
	f := newCooldownFixture(t, policy)
	a1, a2 := f.agent(t, "first"), f.agent(t, "second")
	made := f.ticket(t, "Gated", "a/")
	f.addAttempt(t, made.ID, false)
	require.NotNil(t, f.claim(t, a1))
	require.NotNil(t, f.claim(t, a2))

	out, err := f.svc.ReleaseWith(f.ctx, a2, ticket.ReleaseParams{Reason: "gate not met"})
	require.NoError(t, err)
	require.True(t, out.Escalated)

	assert.Equal(t, map[int]string{1: "in_progress", 2: "discussion"}, f.attemptStatuses(t, made.ID))
	assert.Equal(t, ticket.StatusDiscussion, f.status(t, made.ID), "attempt 2 waits on a person while attempt 1 runs")
}

// Two exits of one ticket's attempts at once are serialised by the claim lock,
// so the second derives the ticket's status from the first's committed
// attempt. The first is held inside its transaction until the second is seen
// waiting on the advisory lock in pg_locks: without the lock the second would
// run past it, never be seen waiting, and fail this test.
func TestDerivedStatus_ConcurrentExitsOfTwoAttempts(t *testing.T) {
	f := newFixture(t)
	id, a1, a2 := f.running(t)

	inside, proceed := make(chan struct{}), make(chan struct{})
	var once, unblock sync.Once
	release := func() { unblock.Do(func() { close(proceed) }) }
	defer release()
	defer ticket.SetDecideBarrier(func() {
		once.Do(func() { close(inside) })
		<-proceed
	})()

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, err := f.svc.Discuss(f.ctx, a1, "Which shape?", "half done")
		assert.NoError(t, err)
	}()
	<-inside
	go func() {
		defer wg.Done()
		_, err := f.svc.Release(f.ctx, a2, "test", nil)
		assert.NoError(t, err)
	}()
	require.Eventually(t, func() bool {
		var waiting int
		require.NoError(t, f.database.QueryRow(f.ctx,
			`SELECT count(*) FROM pg_locks WHERE locktype = 'advisory' AND NOT granted`).Scan(&waiting))
		return waiting > 0
	}, 30*time.Second, 10*time.Millisecond, "the second exit waits on the claim lock")
	release()
	wg.Wait()

	assert.Equal(t, map[int]string{1: "discussion", 2: "queued"}, f.attemptStatuses(t, id))
	assert.Equal(t, ticket.StatusDiscussion, f.status(t, id))
}

// Waiting on a person outranks a sibling that can be claimed: attempt 1 in
// review keeps the ticket in review, so its scope stays held and a person's
// decision on it is still accepted, while attempt 2 is claimable.
func TestDerivedStatus_ReviewOutranksAQueuedSibling(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a1, a2, other := f.agent(t, "first"), f.agent(t, "second"), f.agent(t, "other")
	made := f.gated(t, "Gated", "a/")
	require.NotNil(t, f.claim(t, a1))
	require.NoError(t, f.svc.AcquireIntegrationLock(f.ctx, a1))
	f.review(t, a1)
	f.addAttempt(t, made.ID, true)
	require.Equal(t, ticket.StatusReview, f.status(t, made.ID))

	rival := f.ticket(t, "Rival", "a/")
	claimed := f.claim(t, a2)
	require.NotNil(t, claimed, "the queued attempt of a ticket in review is claimed")
	assert.Equal(t, made.ID, claimed.ID)
	assert.Equal(t, 2, f.leasedAttempt(t, a2))
	assert.Equal(t, ticket.StatusReview, f.status(t, made.ID), "claiming a sibling does not overwrite review")
	assert.Equal(t, map[int]string{1: "review", 2: "in_progress"}, f.attemptStatuses(t, made.ID))

	assert.Nil(t, f.claim(t, other), "attempt 1's scope is still held")
	assert.Equal(t, ticket.StatusOpen, f.status(t, rival.ID))

}

// Attempt 1 in review and attempt 2 queued: the ticket shows review, so the
// scope stays held against another ticket, a person's decision is accepted,
// and attempt 2 can be claimed (Claim and Peek).
func TestDerivedStatus_ReviewPlusQueuedSibling(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a1, a2, other, third := f.agent(t, "first"), f.agent(t, "second"), f.agent(t, "other"), f.agent(t, "third")
	made := f.gated(t, "Gated", "a/")
	f.addAttempt(t, made.ID, false)
	require.NotNil(t, f.claim(t, a1))
	require.NotNil(t, f.claim(t, a2))
	require.NoError(t, f.svc.AcquireIntegrationLock(f.ctx, a1))
	f.review(t, a1)
	f.release(t, a2)

	assert.Equal(t, map[int]string{1: "review", 2: "queued"}, f.attemptStatuses(t, made.ID))
	assert.Equal(t, ticket.StatusReview, f.status(t, made.ID))
	peeked := f.peek(t, third)
	require.NotNil(t, peeked.Ticket, "attempt 2 is offered to a probe")
	assert.Equal(t, made.ID, peeked.Ticket.ID)

	rival := f.ticket(t, "Rival", "a/")
	rivalClaim, err := f.svc.Claim(f.ctx, other, &rival.ID)
	require.NoError(t, err)
	assert.Nil(t, rivalClaim.Ticket, "attempt 1's paths stay held against another ticket")
	require.NotNil(t, rivalClaim.Blocked)
	assert.Equal(t, made.ID, rivalClaim.Blocked.ConflictingTicketID)

	decided := f.requestChanges(t, made.ID)
	assert.Equal(t, ticket.StatusOpen, decided.Status, "attempt 1 is requeued next to attempt 2")
	assert.Equal(t, map[int]string{1: "queued", 2: "queued"}, f.attemptStatuses(t, made.ID))
}

// Attempt 1 in discussion and attempt 2 queued: a person's answer requeues
// attempt 1, and attempt 2 stays claimable throughout.
func TestDerivedStatus_DiscussionPlusQueuedSibling(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a1, a2, third := f.agent(t, "first"), f.agent(t, "second"), f.agent(t, "third")
	made := f.ticket(t, "Asked", "a/")
	f.addAttempt(t, made.ID, false)
	require.NotNil(t, f.claim(t, a1))
	require.NotNil(t, f.claim(t, a2))
	f.discuss(t, a1)
	f.release(t, a2)

	assert.Equal(t, ticket.StatusDiscussion, f.status(t, made.ID))
	require.NotNil(t, f.peek(t, third).Ticket, "attempt 2 is offered to a probe")

	f.humanReplyTo(t, made.ID, 1)

	assert.Equal(t, map[int]string{1: "queued", 2: "queued"}, f.attemptStatuses(t, made.ID))
	assert.Equal(t, ticket.StatusOpen, f.status(t, made.ID))
	require.NotNil(t, f.claim(t, a2))
	assert.Equal(t, 2, f.leasedAttempt(t, a2))
}

// A sibling finishing while the promoted attempt runs does not close the
// ticket; the promoted attempt finishing does.
func TestDerivedStatus_OnlyTheTicketFinishingClosesIt(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	id, a1, a2 := f.running(t)

	_, err := f.svc.Done(f.ctx, a2, &ticket.DoneParams{Summary: "Done."})
	require.NoError(t, err)
	assert.Nil(t, f.get(t, id).ClosedAt, "the ticket is still in progress")
	var attemptClosed bool
	require.NoError(t, f.database.QueryRow(f.ctx,
		`SELECT closed_at IS NOT NULL FROM attempts WHERE ticket_id = $1 AND number = 2`, id).Scan(&attemptClosed))
	assert.True(t, attemptClosed, "the attempt's own closed_at is set")

	_, err = f.svc.Done(f.ctx, a1, &ticket.DoneParams{Summary: "Done."})
	require.NoError(t, err)
	assert.NotNil(t, f.get(t, id).ClosedAt)
}

// A closed_at left on a ticket that is not terminal is cleared by the next
// time its status is worked out.
func TestDerivedStatus_ANonTerminalStatusClearsClosedAt(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	id, _, a2 := f.running(t)
	f.setClosedAt(t, id, "2026-01-01T00:00:00Z")

	f.release(t, a2)

	assert.Equal(t, ticket.StatusInProgress, f.status(t, id))
	assert.Nil(t, f.get(t, id).ClosedAt)
}
