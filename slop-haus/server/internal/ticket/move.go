package ticket

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

// Move is a human's status change: a free-form move to a status, under
// Status.humanMove's rules, or Ready, the one way out of Preparing into the
// queue. A single ticket's move is a batch of one, so the board's single and
// bulk actions share one code path -- and so one set of rules -- rather than
// two kept in step by hand.
type Move struct {
	to    Status
	ready bool
}

// MoveTo is the board's free-form status edit to status.
func MoveTo(status Status) Move { return Move{to: status} }

// MoveReady is the human's Ready: open, or blocked while any dependency is
// unfinished.
func MoveReady() Move { return Move{ready: true} }

// check reports why a ticket in current may not be moved, or nil. It looks
// at nothing but the ticket's own status, which is what makes a batch's
// outcome independent of the order its tickets are named in.
func (m Move) check(current Status) error {
	if m.ready {
		if current != StatusPreparing {
			return ErrNotPreparing
		}
		return nil
	}
	return humanMove(current, m.to)
}

// change is what m does to a ticket check has let it move. Every move
// re-evaluates what is blocked -- a ticket finished or cancelled may be the
// last thing others wait on -- and takes the ticket from any agent holding
// it, unless it is being moved to in progress: unblocking only ever turns
// blocked into open, so in progress is the one status a move lands on that
// leaves a lease standing (see revokesLease).
func (m Move) change() *Change {
	if m.ready {
		return &Change{Ticket: TicketWrite{Status: ToQueue()}, Unblock: true, Revoke: true}
	}
	w := TicketWrite{Status: To(m.to)}
	// Open is the queue, so it resolves like every other way in: blocked
	// while a dependency is unfinished, rather than an open ticket nothing
	// can claim.
	if m.to == StatusOpen {
		w.Status = ToQueue()
	}
	// closed_at tracks the status rather than being set by the caller, so
	// the two can't disagree about whether a ticket is finished.
	if m.to.IsTerminal() {
		w.Closing = Closes()
	} else {
		w.Closing = Reopens()
	}
	// Preparing and Parked carry no first claim: whoever asked in an
	// earlier discussion has no special right to a ticket the human is
	// reshaping, or has set aside.
	if m.to == StatusPreparing || m.to == StatusParked {
		w.FirstClaim = FirstClaimCleared()
	}
	// However a ticket enters review, what is being reviewed may have
	// changed since any earlier outcome.
	if m.to == StatusReview {
		w.Review = ReviewStarts()
	}
	// A ticket taken out of done is going to change, so a look at the change
	// it had is no longer a look at the one it will merge.
	w.ClearsPostReview = m.to != StatusDone
	return &Change{Ticket: w, Unblock: true, Revoke: revokesLease(m.to)}
}

// decide judges m on every ticket of a batch, as loaded in the batch's
// transaction, and returns their Changes in id order -- the order they are
// written in, as SetPriority locks them, so two batches over the same
// tickets cannot deadlock on each other's rows. Or, if any ticket is
// refused, a *BatchRefusedError naming every refusal, in that order too.
// ids holds no duplicates; a ticket missing from loaded was not found.
func (m Move) decide(ids []int64, loaded []Ticket) ([]TicketChange, error) {
	byID := make(map[int64]*Ticket, len(loaded))
	for i := range loaded {
		byID[loaded[i].ID] = &loaded[i]
	}
	var changes []TicketChange
	var refusals []Refusal
	for _, id := range slices.Sorted(slices.Values(ids)) {
		t, ok := byID[id]
		if !ok {
			refusals = append(refusals, Refusal{TicketID: id, Err: ErrNotFound, move: &m})
			continue
		}
		if err := m.check(t.Status); err != nil {
			refusals = append(refusals, Refusal{TicketID: id, From: t.Status, Err: err, move: &m})
			continue
		}
		changes = append(changes, TicketChange{TicketID: id, Change: m.change()})
	}
	if len(refusals) > 0 {
		return nil, &BatchRefusedError{Refusals: refusals}
	}
	return changes, nil
}

// AllowedMoves is every status the free-form edit may take a ticket in s
// to, in board order -- judged by the same check a move makes, so what
// the board offers is what the server accepts. It includes s itself: the
// rule allows staying put, and the detail page's select shows the current
// value from this list.
func AllowedMoves(s Status) []Status {
	var allowed []Status
	for _, to := range model.Statuses() {
		if MoveTo(to).check(s) == nil {
			allowed = append(allowed, to)
		}
	}
	return allowed
}

// CanReady reports whether Ready applies to a ticket in s.
func CanReady(s Status) bool {
	return MoveReady().check(s) == nil
}

// Refusal is one ticket a batch could not act on, and why.
type Refusal struct {
	TicketID int64
	// From is the status the ticket was in; empty when it was not found.
	From Status
	// Err is the sentinel naming the rule: ErrNotFound, ErrNotPreparing,
	// ErrLeavePreparing, ErrNotParkable, ErrLeaveParked or ErrLeaveReview
	// -- or a *ScopeConflictError, which names the ticket and target
	// itself.
	Err  error
	move *Move
}

// Error is the single-ticket routes' long-standing wording, so a batch of
// one says what those routes always have.
func (r *Refusal) Error() string {
	var conflict *ScopeConflictError
	if errors.As(r.Err, &conflict) {
		return conflict.Error()
	}
	if r.move != nil && !r.move.ready && !errors.Is(r.Err, ErrNotFound) {
		return fmt.Sprintf("%s: #%d to %s", r.Err, r.TicketID, r.move.to)
	}
	return fmt.Sprintf("%s: #%d", r.Err, r.TicketID)
}

func (r *Refusal) Unwrap() error { return r.Err }

// Reason is the refusal as the board shows it beside the other tickets of
// a batch: which ticket, the status it is in, and the rule.
func (r *Refusal) Reason() string {
	rule := strings.TrimPrefix(r.Err.Error(), "ticket: ")
	if r.From == "" {
		return fmt.Sprintf("#%d: %s", r.TicketID, rule)
	}
	return fmt.Sprintf("#%d %s: %s", r.TicketID, strings.ToLower(statusLabel(r.From)), rule)
}

// BatchRefusedError is a batch that did nothing, because at least one of
// its tickets was refused. Every ticket is checked before failing, so it
// names all of them, not just the first. errors.Is matches any of their
// sentinels.
type BatchRefusedError struct {
	Refusals []Refusal
}

func (e *BatchRefusedError) Error() string {
	reasons := make([]string, len(e.Refusals))
	for i := range e.Refusals {
		reasons[i] = e.Refusals[i].Reason()
	}
	return fmt.Sprintf("ticket: %d ticket(s) refused, nothing changed: %s", len(e.Refusals), strings.Join(reasons, "; "))
}

func (e *BatchRefusedError) Unwrap() []error {
	errs := make([]error, len(e.Refusals))
	for i := range e.Refusals {
		errs[i] = &e.Refusals[i]
	}
	return errs
}

// single unwraps a batch of one's refusal, so the single-ticket routes
// return the error they always have.
func single(err error) error {
	var batch *BatchRefusedError
	if errors.As(err, &batch) && len(batch.Refusals) == 1 {
		return &batch.Refusals[0]
	}
	return err
}

// notFound refuses each of ids missing from found.
func notFound(ids, found []int64) []Refusal {
	present := make(map[int64]bool, len(found))
	for _, id := range found {
		present[id] = true
	}
	var refusals []Refusal
	for _, id := range ids {
		if !present[id] {
			refusals = append(refusals, Refusal{TicketID: id, Err: ErrNotFound})
		}
	}
	return refusals
}

// inOrder is tickets in the order ids names them, failing if any is missing.
func inOrder(ids []int64, tickets []Ticket) ([]Ticket, error) {
	byID := make(map[int64]Ticket, len(tickets))
	for _, t := range tickets {
		byID[t.ID] = t
	}
	out := make([]Ticket, 0, len(ids))
	for _, id := range ids {
		t, ok := byID[id]
		if !ok {
			return nil, fmt.Errorf("%w: #%d", ErrNotFound, id)
		}
		out = append(out, t)
	}
	return out, nil
}
