package landing

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// ErrRedRequeue is a ticket_finish refused because the head already went red in
// the landing queue. The queue retries red checks once itself, so each red
// landing is two CI runs with nothing else landing; finishing the same head
// again only repeats them.
var ErrRedRequeue = errors.New("landing: this head already went red")

// flakeTitlePrefix is the title prefix of a CI flake ticket.
const flakeTitlePrefix = "CI flake:"

// RedHistory counts a ticket's landings of one head by outcome.
type RedHistory interface {
	CountForHead(ctx context.Context, ticketID int64, head string, o Outcome) (int, error)
}

// FlakeTickets reads the ticket an agent cites as the cause of a red.
type FlakeTickets interface {
	Get(ctx context.Context, id int64) (*ticket.Ticket, error)
}

// EnqueueGuard refuses to queue a head whose landing already ended in red
// checks, unless the agent cites a flake ticket, which allows exactly one more
// attempt of that head. A new head (a commit, or a merge of the base) has no
// history and passes. Only checks_red counts: a conflict, a moved base or a
// code host error says nothing about the head's checks.
//
// The extra attempt is not stored: it is the second checks_red outcome for the
// head, so the count itself shows it was used.
type EnqueueGuard struct {
	reds    RedHistory
	tickets FlakeTickets
	events  *audit.Recorder
}

// NewEnqueueGuard builds a guard; events may be nil.
func NewEnqueueGuard(reds RedHistory, tickets FlakeTickets, events *audit.Recorder) *EnqueueGuard {
	return &EnqueueGuard{reds: reds, tickets: tickets, events: events}
}

// Check returns nil when the head may be queued, otherwise an error wrapping
// ErrRedRequeue that says what to do. flake is the cited ticket id, 0 for none.
func (g *EnqueueGuard) Check(ctx context.Context, tenant, agentID uuid.UUID, ticketID int64, head string, flake int64) error {
	reds, err := g.reds.CountForHead(ctx, ticketID, head, ChecksRed)
	if err != nil {
		return err
	}
	if reds == 0 {
		return nil
	}
	reason, err := g.refusal(ctx, reds, flake)
	if err != nil || reason == "" {
		return err
	}
	if g.events != nil {
		g.events.Record(ctx, audit.LandingRequeueRefused, "tenant_id", tenant.String(), "agent_id", agentID.String(),
			"ticket", ticketID, "head", head, "red_landings", reds, "flake_ticket", flake, "reason", reason)
	}
	return fmt.Errorf("%w: the landing queue already ran the checks on %s %d time(s) and they failed (each landing retries them once). %s "+
		"Nothing was queued. Fix the failure and push a new commit, or, if the failure is a flake unrelated to your change, "+
		"find or file a 'CI flake:' ticket and call ticket_finish again with red_was_flake set to its id. "+
		"Do not call ticket_finish again unchanged.", ErrRedRequeue, head, reds, reason)
}

// refusal is why the head may not be queued, empty when it may.
func (g *EnqueueGuard) refusal(ctx context.Context, reds int, flake int64) (string, error) {
	if flake == 0 {
		return "No flake ticket was cited.", nil
	}
	t, err := g.tickets.Get(ctx, flake)
	if errors.Is(err, ticket.ErrNotFound) {
		return fmt.Sprintf("Ticket #%d, cited as the flake, does not exist.", flake), nil
	}
	if err != nil {
		return "", err
	}
	switch {
	case !strings.HasPrefix(t.Title, flakeTitlePrefix):
		return fmt.Sprintf("Ticket #%d, cited as the flake, is not a '%s' ticket.", flake, flakeTitlePrefix), nil
	case t.Status != ticket.StatusOpen && t.Status != ticket.StatusDone:
		return fmt.Sprintf("Flake ticket #%d is %s; it must be open or done.", flake, t.Status), nil
	case reds > 1:
		return fmt.Sprintf("A flake ticket already bought this head one more attempt and it went red again (#%d does not allow another).", flake), nil
	}
	return "", nil
}
