package agents

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/lowaa/slop-factory/slop-bot/internal/ident"
	"github.com/lowaa/slop-factory/slop-bot/internal/state"
)

// EventMergeAuthorised is recorded when the user authorises a merge.
const EventMergeAuthorised = "merge.authorised"

// MergeGoAhead is the message an authorisation sends when the user gives
// no text of their own.
const MergeGoAhead = "You're authorised to merge this ticket's work now: merge it as your briefing says."

// SaysMerge reports whether a user's tell is the bare go-ahead the design
// names, `slop-bot tell <agent> "merge"`. Only the whole message counts, so
// "don't merge yet" never authorises anything.
func SaysMerge(text string) bool {
	return strings.EqualFold(strings.TrimRight(strings.TrimSpace(text), ".!"), "merge")
}

// IsMergeGoAhead reports whether a user's tell of text to `to` is the bare
// go-ahead: just "merge", to an agent waiting on one (waiting_merge). To an
// agent with a question open, "merge" is its answer (say, to "rebase or
// merge main in?"), not an authorisation.
func (m *Manager) IsMergeGoAhead(ctx context.Context, to, text string) bool {
	if !SaysMerge(text) {
		return false
	}
	a, err := m.resolveAgent(ctx, to)
	return err == nil && a.State == state.StateWaitingMerge
}

// AuthoriseMerge records the user's go-ahead to merge the work of to (an
// agent, or a ticket with a live agent), which the merge guard then lets
// through. It is the ticket's until the ticket ends. Only the user may give
// it.
func (m *Manager) AuthoriseMerge(ctx context.Context, p ident.Principal, to string) error {
	if !p.IsUser() {
		return ErrUserOnly
	}
	a, err := m.resolveAgent(ctx, to)
	if err != nil {
		return err
	}
	t, err := m.opts.Tickets.Get(ctx, a.Ticket)
	if err != nil {
		return err
	}
	if t.Status.Terminal() {
		// Nothing would clear it before a reopen; the guard ignores it anyway.
		return fmt.Errorf("%w: ticket %s is %s", ErrTicketEnded, t.ID, t.Status)
	}
	if err := m.opts.State.MergeGrants.Grant(ctx, a.Ticket, state.UserParty); err != nil {
		return err
	}
	m.event(ctx, EventMergeAuthorised, a.ID, a.Ticket, nil)
	return nil
}

// revokeMerge clears a ticket's go-ahead when it ends, so a reopened ticket
// needs a new one.
func (m *Manager) revokeMerge(ctx context.Context, ticket ident.TicketID) {
	if err := m.opts.State.MergeGrants.Revoke(ctx, ticket); err != nil {
		m.opts.Logger.Warn("agents: clearing a merge authorisation", "ticket", ticket, "err", err)
	}
}

// revokeMergeIfEnded clears the go-ahead of a ticket that has ended, by
// whoever and whether or not an agent is running for it: a ticket marked
// done by hand, or cancelled while its agent is dead, ends no runner.
func (m *Manager) revokeMergeIfEnded(ctx context.Context, ticket ident.TicketID) {
	t, err := m.opts.Tickets.Get(ctx, ticket)
	if err == nil && t.Status.Terminal() {
		m.revokeMerge(ctx, ticket)
	}
}

// ErrTicketEnded refuses a merge go-ahead for a ticket that is done or
// cancelled.
var ErrTicketEnded = errors.New("agents: the ticket has ended")
