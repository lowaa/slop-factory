package change

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

var (
	// ErrNoChangeReason is a no-change close with no reason: the reason is
	// the only record of why nothing was merged.
	ErrNoChangeReason = errors.New("change: closing a ticket with no change needs a reason, recorded as its closing comment")
	// ErrNoChangeUnpromoted is an unpromoted attempt closing the ticket: the
	// ticket's outcome is a human's choice between attempts.
	ErrNoChangeUnpromoted = errors.New("change: this attempt is not the ticket's promoted one, so it cannot close the ticket; hand it over with ticket_finish and a handoff")
	// ErrHasChange is a no-change close of an attempt that has a change.
	ErrHasChange = errors.New("change: this attempt has a change, so the ticket is not closed without one")
	// ErrBranchHasCommits is a no-change close of an attempt whose branch has
	// work on it.
	ErrBranchHasCommits = errors.New("change: this attempt's branch has commits beyond the ticket's base commit, so the ticket is not closed without a change")
	// ErrPartsOpen is a no-change close of a ticket still blocked by tickets
	// that are neither done nor cancelled, such as a split parent whose parts
	// have not all landed.
	ErrPartsOpen = errors.New("change: tickets this one waits on are not done or cancelled yet")
	// ErrNoChangeUnverified is a no-change close that cannot be checked: the
	// ticket has no pinned base commit, or the host did not say.
	ErrNoChangeUnverified = errors.New("change: slop-haus could not verify that this attempt has no commits; this may be temporary, call again in a minute")
)

// FinishWithoutChange closes the held ticket as done when there is verifiably
// no change to land: the attempt has no change recorded or reported, its
// branch has no commits past the ticket's pinned base commit (the host is
// asked), and every ticket it waits on is done or cancelled. The reason is
// the closing comment and goes in the audit record. Anything else is refused,
// naming what is still there.
//
// The checks and the close are not one transaction (the host check is a
// network call): a change opened, a blocker added or a commit pushed in
// between is not seen. The attempt's own agent is the one calling this, so
// only a person adding a blocker meanwhile can race it.
//
// A split parent, or an investigation that concludes with nothing to change,
// ends this way: ticket.Done wants nothing to be merged, but without a check
// an agent could close a ticket with unmerged work.
func (s *Service) FinishWithoutChange(ctx context.Context, tenant, agentID uuid.UUID, reason string) (*ticket.Ticket, error) {
	if err := tenantOf(ctx, tenant); err != nil {
		return nil, err
	}
	reason = strings.TrimSpace(reason)
	gate, err := s.tickets.ChangeGateFor(ctx, agentID)
	if err != nil {
		return nil, err
	}
	t := gate.Ticket
	if err := s.verifyNoChange(ctx, tenant, agentID, gate, reason); err != nil {
		s.record(ctx, audit.ChangeOperationFailed, s.attrs(tenant, agentID, t.ID, "finish_no_change", "err", err.Error())...)
		return nil, err
	}
	done, err := s.tickets.Done(ctx, agentID, &ticket.DoneParams{Summary: reason})
	if err != nil {
		return nil, err
	}
	s.record(ctx, audit.TicketClosedNoChange, s.attrs(tenant, agentID, t.ID, "finish_no_change", "reason", reason)...)
	return done, nil
}

func (s *Service) verifyNoChange(ctx context.Context, tenant, agentID uuid.UUID, gate *ticket.ChangeGate, reason string) error {
	t := gate.Ticket
	if reason == "" {
		return ErrNoChangeReason
	}
	if !gate.Promoted {
		return ErrNoChangeUnpromoted
	}
	if gate.Review.PRURL != nil {
		return fmt.Errorf("%w: %s", ErrHasChange, *gate.Review.PRURL)
	}
	rec, err := s.store.Current(ctx, gate.Attempt.TicketID, gate.Attempt.Number)
	if err == nil {
		return fmt.Errorf("%w: %s", ErrHasChange, s.hosts.ChangeURL(rec.Change))
	}
	if !errors.Is(err, ErrNoChange) {
		return err
	}

	graph, err := s.tickets.Blockers(ctx, t.ID)
	if err != nil {
		return err
	}
	for i := range graph.Nodes {
		if n := &graph.Nodes[i]; n.ID == t.ID && len(n.TransitiveBlockers) > 0 {
			open := make([]string, len(n.TransitiveBlockers))
			for j, id := range slices.Sorted(slices.Values(n.TransitiveBlockers)) {
				open[j] = fmt.Sprintf("#%d", id)
			}
			return fmt.Errorf("%w: %s", ErrPartsOpen, strings.Join(open, ", "))
		}
	}

	// A ticket with no paths is never placed, so it gets no branch and no
	// base pin: there is nothing a commit could be on. The recorded-PR and
	// blocker checks above are all that can be verified.
	if t.BaseCommit == nil && len(t.Paths) == 0 {
		return nil
	}
	if t.BaseCommit == nil {
		return fmt.Errorf("%w: the ticket has no pinned base commit", ErrNoChangeUnverified)
	}
	tgt, err := s.targetFor(ctx, t)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrNoChangeUnverified, err)
	}
	branch := ticket.BranchName(t.ID, gate.Attempt.Number)
	ahead, err := tgt.changes.CommitsAhead(ctx, tenant, tgt.repo, *t.BaseCommit, branch)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrNoChangeUnverified, err)
	}
	if ahead > 0 {
		return fmt.Errorf("%w: %s has %d: land them through a change (change_open)", ErrBranchHasCommits, branch, ahead)
	}
	return nil
}
