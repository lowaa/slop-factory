package ticket

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
)

// A caller that is not a worker (a console, a conductor, a person) says which
// attempt of a ticket it means with an optional number beside the ticket's id.
// ResolveAttempt turns that number into an AttemptRef once, at the edge, and
// everything below takes the ref. A worker never names one: (agent, ticket)
// already names its attempt (heldAttempt), and a number it supplied could
// only be a way to reach a sibling.

var (
	// ErrNoSuchAttempt is a named attempt the ticket does not have.
	ErrNoSuchAttempt = errors.New("ticket: no such attempt")
	// ErrAttemptNotLive is a named attempt that has finished or been closed.
	ErrAttemptNotLive = errors.New("ticket: the attempt is not live")
	// ErrAttemptAmbiguous is no attempt named on a ticket with several live
	// ones. The error is an *AmbiguousAttemptError, which lists them.
	ErrAttemptAmbiguous = errors.New("ticket: the ticket has several live attempts")
	// ErrWorkerNamedAttempt is an attempt named by a worker, or by an agent
	// that holds the ticket's lease for a different one.
	ErrWorkerNamedAttempt = errors.New("ticket: a worker works the attempt it holds and never names one: leave out attempt")
)

// String is how an attempt is shown to people and agents, never as a letter.
func (r AttemptRef) String() string { return fmt.Sprintf("#%d · attempt %d", r.TicketID, r.Number) }

// AmbiguousAttemptError lists the live attempts a caller has to choose from.
type AmbiguousAttemptError struct {
	Live []AttemptRef
}

func (e *AmbiguousAttemptError) Error() string {
	names := make([]string, len(e.Live))
	for i, ref := range e.Live {
		names[i] = ref.String()
	}
	return fmt.Sprintf("ticket: the ticket has several live attempts; name one with attempt: %s", strings.Join(names, ", "))
}

func (e *AmbiguousAttemptError) Is(target error) bool { return target == ErrAttemptAmbiguous }

// addressedAttempt is the attempt a board request means: the one place a
// person's `attempt` parameter becomes an AttemptRef, so everything below takes
// the ref. Huma has no optional number in a query, so 0 stands for none
// (attempts are numbered from 1).
func (h *Handler) addressedAttempt(ctx context.Context, ticketID int64, param int) (*AttemptRef, error) {
	var named *int
	if param != 0 {
		named = &param
	}
	ref, err := h.svc.ResolveAttempt(ctx, ticketID, named)
	if err != nil {
		return nil, h.mapError(err)
	}
	return ref, nil
}

// addressedCommentAttempt is addressedAttempt for a comment: none named is not
// ambiguous, since a comment on a ticket with several live attempts goes to all
// of them (commentTargetFor). Only a named attempt is resolved and checked.
func (h *Handler) addressedCommentAttempt(ctx context.Context, ticketID int64, param int) (*AttemptRef, error) {
	if param == 0 {
		return nil, nil
	}
	return h.addressedAttempt(ctx, ticketID, param)
}

// attemptState is what addressing needs to know of an attempt.
type attemptState struct {
	number   int
	status   string
	promoted bool
}

// live is whether the attempt can still be worked on, answered or decided:
// everything but closed and done. An attempt in review is live -- it is the one
// a person decides on -- which is why this is not AddAttempt's cap, that
// counts the attempts taking up an agent. The status column's CHECK allows
// these six values, so a negation names the same set as listing four.
func (a attemptState) live() bool { return AttemptStatus(a.status).Live() }

func attemptStates(ctx context.Context, q db.Querier, ticketID int64) ([]attemptState, error) {
	rows, err := q.Query(ctx, `SELECT number, status, promoted_at IS NOT NULL FROM attempts WHERE ticket_id = $1 ORDER BY number`, ticketID)
	if err != nil {
		return nil, fmt.Errorf("read the attempts: %w", err)
	}
	states, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (attemptState, error) {
		var a attemptState
		return a, r.Scan(&a.number, &a.status, &a.promoted)
	})
	if err != nil {
		return nil, fmt.Errorf("read the attempts: %w", err)
	}
	return states, nil
}

// ResolveAttempt is the attempt a console, conductor or person means.
//
// With named set, it is that attempt, which must exist and be live. With none
// named, a ticket with several live attempts is refused with the list; with
// one or none, the result is nil and the caller's own default stands, so a
// ticket with a single attempt behaves exactly as it did before attempts
// could be named.
func ResolveAttempt(ctx context.Context, q db.Querier, ticketID int64, named *int) (*AttemptRef, error) {
	states, err := attemptStates(ctx, q, ticketID)
	if err != nil {
		return nil, err
	}
	return chooseAttempt(ticketID, states, named)
}

// ResolveLiveAttempt is ResolveAttempt for a caller that records the attempt
// rather than defaulting it: with none named it is the ticket's one live
// attempt, so what is recorded is the attempt a single-attempt ticket's caller
// meant without saying. Nil when the ticket has no live attempt.
func ResolveLiveAttempt(ctx context.Context, q db.Querier, ticketID int64, named *int) (*AttemptRef, error) {
	states, err := attemptStates(ctx, q, ticketID)
	if err != nil {
		return nil, err
	}
	ref, err := chooseAttempt(ticketID, states, named)
	if err != nil || ref != nil {
		return ref, err
	}
	for _, a := range states {
		if a.live() {
			return &AttemptRef{TicketID: ticketID, Number: a.number}, nil
		}
	}
	return nil, nil
}

// ResolveAttempt is the package-level ResolveAttempt for a caller with no
// transaction of its own: a read that takes no lock.
func (s *Service) ResolveAttempt(ctx context.Context, ticketID int64, named *int) (*AttemptRef, error) {
	attempts, err := s.store.ListAttempts(ctx, ticketID)
	if err != nil {
		return nil, err
	}
	states := make([]attemptState, len(attempts))
	for i, a := range attempts {
		states[i] = attemptState{number: a.Number, status: a.Status}
	}
	return chooseAttempt(ticketID, states, named)
}

func chooseAttempt(ticketID int64, states []attemptState, named *int) (*AttemptRef, error) {
	// A ticket with no attempts (every real one has one) is not found by the
	// write this precedes, which says so in its own words.
	var live []AttemptRef
	for _, a := range states {
		ref := AttemptRef{TicketID: ticketID, Number: a.number}
		if a.live() {
			live = append(live, ref)
		}
		if named != nil && a.number == *named {
			if !a.live() {
				return nil, fmt.Errorf("%s: %w", ref, ErrAttemptNotLive)
			}
			return &ref, nil
		}
	}
	if named != nil {
		return nil, fmt.Errorf("#%d has no attempt %d: %w", ticketID, *named, ErrNoSuchAttempt)
	}
	if len(live) > 1 {
		return nil, &AmbiguousAttemptError{Live: live}
	}
	return nil, nil
}

// liveAttemptCount is how many attempts of the ticket are live.
func liveAttemptCount(ctx context.Context, q db.Querier, ticketID int64) (int, error) {
	states, err := attemptStates(ctx, q, ticketID)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, a := range states {
		if a.live() {
			n++
		}
	}
	return n, nil
}

// BranchName is the branch an attempt's work goes on: `ticket-42-a1` for
// every attempt, the first included. It is a function of two values that never
// change (ticket ids, and attempt numbers, which are never reused), so it needs
// no record and a title edit cannot move it. It has no `/`, so no other branch
// can sit on its prefix and block it.
func BranchName(ticketID int64, attempt int) string {
	return fmt.Sprintf("ticket-%d-a%d", ticketID, attempt)
}

// ParseBranchName is BranchName's inverse: the ticket and attempt a branch
// is for, when it is named exactly as BranchName names one. Anything else (a
// prefix or suffix, a sign, a leading zero, a zero) is no attempt's, so a
// branch maps to at most one attempt and that attempt back to the same name.
func ParseBranchName(branch string) (ticketID int64, attempt int, ok bool) {
	rest, found := strings.CutPrefix(branch, "ticket-")
	if !found {
		return 0, 0, false
	}
	idText, numText, found := strings.Cut(rest, "-a")
	if !found {
		return 0, 0, false
	}
	id, idOK := canonicalPositive(idText, 64)
	n, nOK := canonicalPositive(numText, 32)
	if !idOK || !nOK {
		return 0, 0, false
	}
	return id, int(n), true
}

// canonicalPositive parses s as %d would print a positive number that fits
// in bitSize bits: digits only, no leading zero.
func canonicalPositive(s string, bitSize int) (int64, bool) {
	if s == "" || s[0] == '0' {
		return 0, false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, false
		}
	}
	v, err := strconv.ParseInt(s, 10, bitSize)
	return v, err == nil
}
