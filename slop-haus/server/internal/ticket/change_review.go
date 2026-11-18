package ticket

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost"
)

// A review or merge made on a ticket's change (pull request) on the code
// host, rather than on the board, reaches here through a webhook
// (internal/githubhook). The rules are the board's own -- Approve,
// RequestChanges, a person moving the ticket to done -- plus what it takes
// to trust a report from elsewhere: finding the ticket from the change
// alone, and refusing a review from an earlier cycle.

var (
	// ErrNoTicketForPR is a PR no ticket reported, or none in a state the
	// event applies to.
	ErrNoTicketForPR = errors.New("ticket: no ticket for this PR")
	// ErrAmbiguousPR is a PR more than one ticket could mean. Guessing
	// would approve or close the wrong one, so none is touched.
	ErrAmbiguousPR = errors.New("ticket: more than one ticket has this PR")
	// ErrStaleReview is a review submitted before the ticket's current
	// review began: it was of an earlier version of the change.
	ErrStaleReview = errors.New("ticket: the review predates the ticket's current review")
	// ErrNotMergeable is a merge reported for a ticket that is neither in
	// review nor approved and waiting -- most often, one an agent holds to
	// merge it, whose own change_merge will close it -- or whose PR is no
	// longer the one merged.
	ErrNotMergeable = errors.New("ticket: ticket is not in review or approved and unclaimed")
)

// PRReview is a person's review of a ticket's change, given on the host.
type PRReview struct {
	PR     codehost.ChangeRef
	UserID uuid.UUID
	// SubmittedAt is when the host says the review was submitted.
	SubmittedAt time.Time
	// Comment is RequestChanges' comment; unused by an approval.
	Comment string
}

// ApprovePR is Approve for the one ticket in review with this PR.
func (s *Service) ApprovePR(ctx context.Context, r *PRReview) (*Ticket, error) {
	t, err := s.ticketForPR(ctx, r.PR, ErrNotInReview, inReview)
	if err != nil {
		return nil, err
	}
	var recorded *Approval
	approved, err := s.transitionMatched(WithUser(ctx, r.UserID), t, r.PR, approval(r.UserID, s.affinity, &r.SubmittedAt),
		func(tx TransitionTx) (err error) {
			recorded, err = recordedApproval(ctx, tx.Querier(), &AttemptRef{TicketID: t.ID, Number: t.MatchedAttempt})
			return err
		})
	if err != nil {
		return nil, err
	}
	s.NotifyApproved(ctx, recorded)
	return approved, nil
}

// RequestChangesPR is RequestChanges for the one ticket in review with this
// PR.
func (s *Service) RequestChangesPR(ctx context.Context, r *PRReview) (*Ticket, error) {
	if strings.TrimSpace(r.Comment) == "" {
		return nil, ErrEmptyComment
	}
	t, err := s.ticketForPR(ctx, r.PR, ErrNotInReview, inReview)
	if err != nil {
		return nil, err
	}
	return s.transitionMatched(WithUser(ctx, r.UserID), t, r.PR, changesRequested(r.UserID, r.Comment, s.affinity, &r.SubmittedAt), nil)
}

// CloseMergedPR is a person having merged a ticket's PR themselves: the
// ticket moves to done, as their status edit on the board would, if it is
// in review or approved and not yet claimed. A ticket an agent holds is
// left alone -- it is merging, and its change_merge closes it.
//
// A ticket waiting in the merge queue derives to open though it is unclaimed,
// so it is accepted too, and its queue row is removed in the same transaction.
// A row the runner has begun (it has a step) is the runner's: its reconcile
// finishes the ticket, and closing it here would race it.
// A ticket the queue holds (in_merge_queue) is mergeable, but its row must be
// removable here too. Open plus a row is how the release before the status
// showed a queued ticket.
func (s *Service) CloseMergedPR(ctx context.Context, pr codehost.ChangeRef, userID uuid.UUID) (*Ticket, error) {
	t, err := s.ticketForPR(ctx, pr, ErrNotMergeable, func(t *Ticket) bool { return mergeable(t) || t.Status == StatusOpen })
	if err != nil {
		return nil, err
	}
	unapproved := !mergeable(t) || t.Status == StatusInMergeQueue
	return s.transitionMatched(WithUser(ctx, userID), t, pr, closeMergedFrom(s.hosts, pr, userID, true), func(tx TransitionTx) error {
		dequeued, err := tx.Dequeue(ctx, &DequeueRequest{
			TicketID: t.ID, Reason: DequeueMergedOutside,
			Detail: "merged outside the queue by user " + userID.String(),
		})
		if err != nil {
			return err
		}
		if unapproved && !dequeued {
			return fmt.Errorf("%w: #%d is %s and not waiting to land", ErrNotMergeable, t.ID, t.Status)
		}
		return nil
	})
}

// transitionMatched is transition on a ticket ticketForPR matched, carrying
// the matched attempt onto the ticket it returns. The match was read before
// the transition's lock, so decide runs only if, under it, the promoted
// attempt still reports the PR: a promotion that moved in between must not
// turn a review of the old attempt's PR into the new one's approval.
func (s *Service) transitionMatched(ctx context.Context, matched *Ticket, pr codehost.ChangeRef, decide ticketTransition, after func(tx TransitionTx) error) (*Ticket, error) {
	if matchBarrier != nil {
		matchBarrier()
	}
	t, err := s.transitionThen(ctx, matched.ID, func(current *Ticket) (*Change, error) {
		if !hasPR(s.hosts, current, pr) {
			return nil, &AttemptNotPromotedError{Attempt: AttemptRef{TicketID: matched.ID, Number: matched.MatchedAttempt}}
		}
		return decide(current)
	}, after)
	if err != nil {
		return nil, err
	}
	t.MatchedAttempt = matched.MatchedAttempt
	return t, nil
}

func inReview(t *Ticket) bool { return t.Status == StatusReview }

// mergeable is whether a merge made on the host may close t. Judged again
// under the claim lock by closeMergedFrom, since t was read before it.
func mergeable(t *Ticket) bool {
	switch t.Status {
	case StatusReview, StatusInMergeQueue:
		return true
	case StatusOpen, StatusBlocked:
		return t.ApprovedAt != nil
	}
	return false
}

// matchBarrier is a test-only seam, nil in production: it runs between
// matching a PR to its ticket and the transition, so a test can move the
// ticket's promotion there.
var matchBarrier func()

// PRAttemptStore finds attempts by their PR. A separate interface from Store
// so the webhook match does not edit the Store both it and in-flight work
// share; PostgresStore is the one implementation.
type PRAttemptStore interface {
	AttemptsForPR(ctx context.Context, link string) ([]PRAttempt, error)
}

// ErrAttemptNotPromoted is a PR that only an attempt which is not promoted
// reports. Its PR is a draft for the ticket's base branch, and promoting the
// attempt is the approval, so a review or merge of it moves nothing.
var ErrAttemptNotPromoted = errors.New("ticket: attempt is not promoted")

// AttemptNotPromotedError says which ticket and attempt a PR was matched to.
type AttemptNotPromotedError struct {
	Attempt AttemptRef
}

func (e *AttemptNotPromotedError) Error() string {
	return fmt.Sprintf("ticket: #%d attempt %d is not promoted; promoting it is the approval", e.Attempt.TicketID, e.Attempt.Number)
}

func (e *AttemptNotPromotedError) Is(target error) bool { return target == ErrAttemptNotPromoted }

// hasPR is whether t's reported PR is pr.
func hasPR(hosts *codehost.Registry, t *Ticket, pr codehost.ChangeRef) bool {
	return t.PRURL != nil && linkIsPR(hosts, *t.PRURL, pr)
}

// linkIsPR is whether a reported PR link is pr.
func linkIsPR(hosts *codehost.Registry, link string, pr codehost.ChangeRef) bool {
	ref, ok := hosts.ParseChangeURL(link)
	return ok && ref == pr
}

// ticketForPR picks the one ticket whose promoted attempt has this PR that
// eligible accepts, and returns it with MatchedAttempt set. None eligible,
// with exactly one ticket on the PR, is that ticket's notEligible error,
// which says which ticket it was. A PR only unpromoted attempts report is
// ErrAttemptNotPromoted, naming the attempt; otherwise ErrNoTicketForPR.
func (s *Service) ticketForPR(ctx context.Context, pr codehost.ChangeRef, notEligible error, eligible func(*Ticket) bool) (*Ticket, error) {
	finder, ok := s.store.(PRAttemptStore)
	if !ok {
		return nil, errors.New("ticket: the store cannot find attempts by PR")
	}
	link := s.hosts.ChangeURL(pr)
	found, err := finder.AttemptsForPR(ctx, link)
	if err != nil {
		return nil, err
	}
	promoted := map[int64]int{}
	unpromoted := map[int64][]int{}
	for _, a := range found {
		switch {
		case !linkIsPR(s.hosts, a.PRURL, pr):
		case a.Promoted:
			promoted[a.TicketID] = a.Number
		default:
			unpromoted[a.TicketID] = append(unpromoted[a.TicketID], a.Number)
		}
	}

	var chosen []*Ticket
	var ineligible *Ticket
	for _, id := range sortedKeys(promoted) {
		t, err := s.store.GetTicket(ctx, id)
		if err != nil {
			return nil, err
		}
		t.MatchedAttempt = promoted[id]
		if eligible(t) {
			chosen = append(chosen, t)
		} else {
			ineligible = t
		}
	}
	// One ticket reporting the PR on several attempts leaves it unsaid which
	// one a review is of; and the transition below can only check the PR, not
	// the attempt, still holds it.
	for _, id := range sortedKeys(promoted) {
		if len(unpromoted[id]) > 0 {
			return nil, fmt.Errorf("%w: %s", ErrAmbiguousPR, link)
		}
	}
	// A ticket that reports the PR only on an unpromoted attempt counts as
	// another ticket on it: acting on the chosen one could be the wrong call.
	only := unpromotedOnly(unpromoted, promoted)
	switch {
	case len(chosen) == 1 && len(only) == 0:
		return chosen[0], nil
	case len(chosen) > 0, len(only) > 0 && len(promoted)+len(only) > 1:
		return nil, fmt.Errorf("%w: %s", ErrAmbiguousPR, link)
	}
	if ineligible != nil && len(promoted) == 1 {
		return nil, fmt.Errorf("%w: #%d is %s", notEligible, ineligible.ID, ineligible.Status)
	}
	for id, numbers := range only {
		if len(numbers) > 1 {
			return nil, fmt.Errorf("%w: %s", ErrAmbiguousPR, link)
		}
		return nil, &AttemptNotPromotedError{Attempt: AttemptRef{TicketID: id, Number: numbers[0]}}
	}
	return nil, fmt.Errorf("%w: %s", ErrNoTicketForPR, link)
}

// unpromotedOnly is the tickets that report the PR on unpromoted attempts
// and on no promoted one.
func unpromotedOnly(unpromoted map[int64][]int, promoted map[int64]int) map[int64][]int {
	out := map[int64][]int{}
	for id, numbers := range unpromoted {
		if _, has := promoted[id]; !has {
			out[id] = numbers
		}
	}
	return out
}

func sortedKeys(m map[int64]int) []int64 {
	keys := make([]int64, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
