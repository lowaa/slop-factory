package ticket

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

// The human's moves on a ticket, each made in a transaction the caller
// already holds. Service's own methods for them (Approve, MarkReady,
// AddComment, ...) open one with InTransition and call these, so a caller
// that must make the move together with writes of its own -- approving a
// proposal and marking it approved -- composes the very code the human's
// route runs, and the two cannot differ in what they allow.
//
// Approving, requesting changes and commenting take the person as an
// argument; a status move or Ready is made as the person ctx names (WithUser),
// as it is on the routes, so the status change record is theirs.

// onNamedAttempt is tx for a person's decision about the attempt they named
// (nil: none, so the ticket's own, as it always was). The attempt is checked
// again here, in the decision's own transaction, since it was resolved before
// the transaction began and may have been closed since.
func onNamedAttempt(ctx context.Context, tx TransitionTx, ticketID int64, ref *AttemptRef) (TransitionTx, error) {
	if ref == nil {
		return tx, nil
	}
	if ref.TicketID != ticketID {
		return nil, fmt.Errorf("a decision on #%d addressed to %s", ticketID, ref)
	}
	if _, err := ResolveAttempt(ctx, tx.Querier(), ticketID, &ref.Number); err != nil {
		return nil, err
	}
	// With a single live attempt, the promoted one, the ticket's status and
	// fields are that attempt's, and the decision is the ticket-wide one it
	// always was.
	live, err := liveAttemptCount(ctx, tx.Querier(), ticketID)
	if err != nil {
		return nil, err
	}
	if live <= 1 {
		promoted, err := promotedAttempt(ctx, tx.Querier(), ticketID)
		if err == nil && promoted.Number == ref.Number {
			return tx, nil
		}
		if err != nil && !errors.Is(err, ErrNoPromotedAttempt) {
			return nil, err
		}
	}
	return tx.ForAttempt(ref), nil
}

// ApproveReview is Approve in tx, for the attempt the person named (nil: none).
func ApproveReview(ctx context.Context, tx TransitionTx, ticketID int64, attempt *AttemptRef, userID uuid.UUID, affinity time.Duration) (*Ticket, error) {
	on, err := onNamedAttempt(ctx, tx, ticketID, attempt)
	if err != nil {
		return nil, err
	}
	return decidedTicket(WithUser(ctx, userID), tx, on, ticketID, approval(userID, affinity, nil))
}

// decidedTicket is decideAndApply in on, answering with the ticket as tx (the
// unscoped transaction) shows it afterwards: what the board is told is the
// ticket's status, not the one attempt's.
func decidedTicket(ctx context.Context, tx, on TransitionTx, ticketID int64, decide ticketTransition) (*Ticket, error) {
	if _, err := decideAndApply(ctx, on, nil, ticketID, decide); err != nil {
		return nil, err
	}
	return tx.Ticket(ctx, ticketID)
}

// RequestReviewChanges is RequestChanges in tx, for the attempt the person
// named (nil: none). The comment is that attempt's.
func RequestReviewChanges(ctx context.Context, tx TransitionTx, ticketID int64, attempt *AttemptRef, userID uuid.UUID, comment string, affinity time.Duration) (*Ticket, error) {
	if strings.TrimSpace(comment) == "" {
		return nil, ErrEmptyComment
	}
	on, err := onNamedAttempt(ctx, tx, ticketID, attempt)
	if err != nil {
		return nil, err
	}
	return decidedTicket(WithUser(ctx, userID), tx, on, ticketID, changesRequested(userID, comment, affinity, nil))
}

// MarkTicketReady is MarkReady in tx.
func MarkTicketReady(ctx context.Context, tx TransitionTx, id int64) (*Ticket, error) {
	return moveOne(ctx, tx, id, MoveReady())
}

// MoveTicketTo is the board's status edit on one ticket in tx -- what
// MoveMany does for a batch of one.
func MoveTicketTo(ctx context.Context, tx TransitionTx, id int64, status Status) (*Ticket, error) {
	if !status.Valid() {
		return nil, fmt.Errorf("%w: status %q", ErrInvalidEnum, status)
	}
	return moveOne(ctx, tx, id, MoveTo(status))
}

// CommentAsUser is the signed-in user's comment in tx, with what it implies
// -- see humanCommentEffect.
//
// The comment is recorded on its own, ahead of the Change, so commentBarrier
// can sit between the two.
func CommentAsUser(ctx context.Context, tx TransitionTx, hc *HumanComment) (*HumanCommentResult, error) {
	if hc.Body == "" {
		return nil, ErrEmptyComment
	}
	ctx = WithUser(ctx, hc.UserID)
	result := &HumanCommentResult{}
	current, err := tx.Ticket(ctx, hc.TicketID)
	if err != nil {
		return nil, err
	}
	if hc.IdempotencyKey != "" {
		prior, err := commentByKey(ctx, tx.Querier(), current.ID, hc.IdempotencyKey)
		if err != nil {
			return nil, err
		}
		if prior != nil {
			return &HumanCommentResult{Comment: prior}, nil
		}
	}
	on, err := onNamedAttempt(ctx, tx, hc.TicketID, hc.Attempt)
	if err != nil {
		return nil, err
	}
	if result.Comment, err = tx.Comment(ctx, current.ID, &NewComment{By: UserAuthor(hc.UserID), Body: hc.Body, Attempt: hc.Attempt, IdempotencyKey: hc.IdempotencyKey}); err != nil {
		return nil, err
	}
	if hc.NeedsDiscussion != nil && commentBarrier != nil {
		commentBarrier()
	}
	change, in, err := commentEffect(ctx, tx, on, hc, current)
	if err != nil {
		return nil, err
	}
	if change != nil {
		if err := change.apply(ctx, in, nil, current.ID); err != nil {
			return nil, err
		}
	}
	if hc.NeedsDiscussion == nil {
		return result, nil
	}
	result.Ticket, err = tx.Ticket(ctx, current.ID)
	if err != nil {
		return nil, err
	}
	return result, nil
}

// commentEffect is what the comment does beyond being recorded, and the
// transaction to make it in. Answering a discussion is the attempt's: the one
// the person named, which `on` shows and writes alone. A comment naming none,
// on a ticket with several live attempts, is guidance for all of them and
// answers none; only a flag it carries is the ticket's, and `tx` writes that.
func commentEffect(ctx context.Context, tx, on TransitionTx, hc *HumanComment, current *Ticket) (*Change, TransitionTx, error) {
	answering := current
	in := tx
	switch {
	case hc.Attempt != nil:
		var err error
		if answering, err = on.Ticket(ctx, current.ID); err != nil {
			return nil, nil, err
		}
		in = on
	default:
		live, err := liveAttemptCount(ctx, tx.Querier(), current.ID)
		if err != nil {
			return nil, nil, err
		}
		if live > 1 {
			answering = nil
		}
	}
	if answering == nil || (hc.Attempt != nil && answering.Status != StatusDiscussion) {
		// Not an answer: at most the flag, written on the ticket as a whole.
		if hc.NeedsDiscussion == nil {
			return nil, tx, nil
		}
		change, err := settingNeedsDiscussion(*hc.NeedsDiscussion)(current)
		return change, tx, err
	}
	change, err := humanCommentEffect(hc.NeedsDiscussion, hc.Affinity)(answering)
	return change, in, err
}

func moveOne(ctx context.Context, tx TransitionTx, id int64, move Move) (*Ticket, error) {
	if err := makeMove(ctx, tx, []int64{id}, move); err != nil {
		return nil, single(err)
	}
	return tx.Ticket(ctx, id)
}

// EditTicketText is Service.Edit in tx: the person's change to a ticket's
// title or body, recorded as a revision with them as the editor, and refused
// as stale when params.BaseRevision is not the ticket's revision.
func EditTicketText(ctx context.Context, tx TransitionTx, id int64, userID uuid.UUID, params *EditParams) (*EditResult, error) {
	if err := params.validate(); err != nil {
		return nil, err
	}
	changed, err := editText(ctx, tx.Querier(), id, UserAuthor(userID), params)
	if err != nil {
		return nil, err
	}
	t, err := tx.Ticket(ctx, id)
	if err != nil {
		return nil, err
	}
	return &EditResult{Ticket: t, Changed: changed}, nil
}

// UpdateInTx is the board's edit of a ticket's fields (Service.Update) in
// tx: the status move, priority, paths and flags, written together and
// refused together. Service.Update runs it in a transition of its own, after
// PrepareUpdate; a caller that composes it with writes of its own does the
// same.
//
// Every edit is a transition, under the claim lock, even one that moves
// nothing: claims and agents' exits read these fields under that lock
// and decide on them -- Done reads requires_approval, a claim a
// candidate's paths -- and an edit committing between such a read and
// the write that follows would leave that write decided on a value no
// longer there.
//
// A status change is a batch of one -- the same decision and writes
// every bulk move makes, so a single edit and a bulk one cannot disagree
// on the rules -- and a flag write the same transition the comment route
// makes, so it takes a held ticket back as that does; the other fields
// are written in the same transaction.
func UpdateInTx(ctx context.Context, tx TransitionTx, id int64, params *UpdateParams) error {
	fields := *params
	fields.Status = nil
	fields.NeedsDiscussion = nil
	var moved *movedTickets
	if params.Status != nil {
		var err error
		if moved, err = applyMove(ctx, tx, []int64{id}, MoveTo(*params.Status)); err != nil {
			return single(err)
		}
	}
	var before []model.Path
	if params.Paths != nil {
		held, err := tx.Tickets(ctx, []int64{id})
		if err != nil {
			return err
		}
		if len(held) > 0 {
			before = held[0].Paths
		}
	}
	if err := tx.UpdateFields(ctx, id, &fields); err != nil {
		return err
	}
	if params.NeedsDiscussion != nil {
		// Decided on the ticket as the move left it: a ticket moved to,
		// or left in, in progress with the flag set is still taken back.
		if _, err := decideAndApply(ctx, tx, nil, id, settingNeedsDiscussion(*params.NeedsDiscussion)); err != nil {
			return err
		}
	}
	// Last, on the ticket as this edit leaves it -- moved, and with the
	// extended paths it kept -- so a ticket moved into review is checked
	// as holding its scope, and one taken from its agent as holding
	// none. A refusal rolls the whole edit back.
	switch {
	case params.Paths != nil:
		// New paths are a new claim on files whether or not the ticket
		// already held its old ones, so any holder is checked. This
		// subsumes the move's own check, which would judge the paths
		// being replaced.
		conflicts, err := tx.ScopeConflicts(ctx, []int64{id})
		if err != nil {
			return err
		}
		if len(conflicts) > 0 {
			return &ScopeConflictError{Blocked: conflicts[0]}
		}
		// A held ticket's agent reads its comments, and a scope edit is
		// otherwise silent: nothing else would tell it the files it may
		// touch changed under it. Written with the edit, so neither
		// exists without the other.
		if user := userFrom(ctx); user != nil {
			if body, changed := filedScopeChange(before, params.Paths); changed {
				if _, err := tx.Comment(ctx, id, &NewComment{By: UserAuthor(*user), Body: body}); err != nil {
					return err
				}
			}
		}
	case moved != nil:
		return single(moved.refuseGainedConflicts(ctx, tx))
	}
	return nil
}
