package ticket

import (
	"context"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
)

// SetClaimBarrier installs the test-only barrier inside the claim
// transaction, and returns a function restoring the previous value. See
// claimBarrier in lease_store.go for why this seam is necessary.
func SetClaimBarrier(fn func()) func() {
	previous := claimBarrier
	claimBarrier = fn
	return func() { claimBarrier = previous }
}

// SetDepsBarrier installs the test-only barrier between the cycle check
// and the insert in AddDependency, and returns a function restoring the
// previous value. See depsBarrier in blockers_store.go.
func SetDepsBarrier(fn func()) func() {
	previous := depsBarrier
	depsBarrier = fn
	return func() { depsBarrier = previous }
}

// SetEditBarrier installs the test-only barrier between reading a ticket's
// text and writing the edit, and returns a function restoring the previous
// value. See editBarrier in revisions_store.go.
func SetEditBarrier(fn func()) func() {
	previous := editBarrier
	editBarrier = fn
	return func() { editBarrier = previous }
}

// SetCommentBarrier installs the test-only barrier between a human
// comment's insert and its needs_discussion write, and returns a function
// restoring the previous value. See commentBarrier in service.go.
func SetCommentBarrier(fn func()) func() {
	previous := commentBarrier
	commentBarrier = fn
	return func() { commentBarrier = previous }
}

// SetDecideBarrier installs the test-only barrier between a transition's
// load of the ticket and its decision, and returns a function restoring the
// previous value. See decideBarrier in service.go.
func SetDecideBarrier(fn func()) func() {
	previous := decideBarrier
	decideBarrier = fn
	return func() { decideBarrier = previous }
}

// CloseMerged is CloseMergedPR on a ticket already matched to the PR, so a
// test can change the ticket between the match and the close.
func (s *Service) CloseMerged(ctx context.Context, ticketID int64, pr codehost.ChangeRef, userID uuid.UUID) (*Ticket, error) {
	return s.transition(ctx, ticketID, closeMergedFrom(s.hosts, pr, userID, false))
}

// SetNarrowBarrier installs the test-only barrier inside the narrowing
// transaction, and returns a function restoring the previous value. See
// narrowBarrier in lease_ops_store.go.
func SetNarrowBarrier(fn func()) func() {
	previous := narrowBarrier
	narrowBarrier = fn
	return func() { narrowBarrier = previous }
}

// ListQuery is the SQL and arguments of one read of a list, to EXPLAIN.
func ListQuery(filter *ListFilter) (string, []any, error) { return listQuery(filter) }

// AddAttempt inserts a queued attempt on a ticket by SQL, numbered as the
// add-attempt endpoint will number it, and returns its number. The endpoint
// is a later part; tests of the claim need a ticket with several attempts
// before it exists.
func AddAttempt(ctx context.Context, q db.Querier, ticketID int64, shadow bool) (int, error) {
	var number int
	if err := q.QueryRow(ctx, `
		UPDATE tickets SET next_attempt_number = next_attempt_number + 1
		 WHERE id = $1 RETURNING next_attempt_number - 1`, ticketID).Scan(&number); err != nil {
		return 0, err
	}
	_, err := q.Exec(ctx, `INSERT INTO attempts (ticket_id, number, status, shadow) VALUES ($1, $2, 'queued', $3)`,
		ticketID, number, shadow)
	return number, err
}

// SetAddBarrier installs the test-only barrier inside AddAttempt's
// transaction, and returns a function restoring the previous value. See
// addBarrier in attempt_add.go.
func SetAddBarrier(fn func()) func() {
	previous := addBarrier
	addBarrier = fn
	return func() { addBarrier = previous }
}

// SetMatchBarrier installs the test-only barrier between matching a PR and
// transitioning its ticket, and returns a function restoring the previous
// value. See matchBarrier in change_review.go.
func SetMatchBarrier(fn func()) func() {
	previous := matchBarrier
	matchBarrier = fn
	return func() { matchBarrier = previous }
}

// TicketToBody is a ticket as the lists serve it.
func TicketToBody(t Ticket) TicketBody { return ticketToBody(t) }

// SetPromoteBarrier installs the test-only barrier inside PromoteAttempt's
// transaction, and returns a function restoring the previous value. See
// promoteBarrier in attempt_promote.go.
func SetPromoteBarrier(fn func()) func() {
	previous := promoteBarrier
	promoteBarrier = fn
	return func() { promoteBarrier = previous }
}

// CommentSettingNeedsDiscussion is the signed-in user's comment together
// with a new value for needs_discussion, in one transaction under the claim
// lock. As two requests an agent could claim the ticket between them --
// the asker of a discussion the comment just answered is often already
// waiting to -- and start building on a ticket the human meant to be
// planned.
//
// Setting it on a ticket an agent is working also withdraws the ticket
// from that agent, who is told to stop on its next call; clearing it does
// not, since an agent planning can carry on and ask as it would have.
func (s *Service) CommentSettingNeedsDiscussion(ctx context.Context, ticketID int64, userID uuid.UUID, body string, needsDiscussion bool) (*HumanCommentResult, error) {
	if body == "" {
		return nil, ErrEmptyComment
	}
	return s.addHumanComment(ctx, &HumanComment{
		TicketID: ticketID, UserID: userID, Body: body, Affinity: s.affinity,
		NeedsDiscussion: &needsDiscussion,
	})
}
