package mergeguard

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/lowaa/slop-factory/slop-bot/internal/ident"
	"github.com/lowaa/slop-factory/slop-bot/internal/tickets"
)

// Tickets is what the guard reads of the ticketing system.
type Tickets interface {
	Get(ctx context.Context, id ident.TicketID) (*tickets.Ticket, error)
}

// Grants is where the user's go-ahead to merge a ticket is recorded
// (state.db's merge_grants).
type Grants interface {
	Granted(ctx context.Context, ticket ident.TicketID) (bool, error)
}

// Options configures a Guard.
type Options struct {
	Classifier *Classifier
	Tickets    Tickets
	Grants     Grants
	Logger     *slog.Logger
}

// Guard decides, in the daemon, whether an agent's tool call may run.
type Guard struct {
	opts *Options
}

// New returns a Guard.
func New(opts *Options) *Guard { return &Guard{opts: opts} }

// Verdict is the guard's answer for one tool call.
type Verdict struct {
	Allow bool
	// Finding is the merge the call was found to be; nil for any other
	// call.
	Finding *Finding
	// Reason is what a refused agent is told.
	Reason string
}

// Check is the guard's answer for call, made by p. Anything but an agent's
// merge is allowed; an agent's merge is allowed only on a ticket with
// AutoMerge, or one the user has authorised. When the ticket or the grant
// can't be read the merge is refused: the guard fails safe.
func (g *Guard) Check(ctx context.Context, p ident.Principal, call *Call) *Verdict {
	if p.IsUser() {
		// No token: a person's own session, with no ticket to hold it to.
		return &Verdict{Allow: true}
	}
	f := g.opts.Classifier.Classify(ctx, call)
	if f == nil {
		return &Verdict{Allow: true}
	}
	deny := func(why string) *Verdict {
		g.opts.Logger.Info("mergeguard: refused a merge", "agent", p.Agent, "ticket", p.Ticket, "kind", f.Kind, "detail", f.Detail, "why", why)
		return &Verdict{Finding: f, Reason: refusal(p, f, why)}
	}
	t, err := g.opts.Tickets.Get(ctx, p.Ticket)
	if err != nil {
		return deny(fmt.Sprintf("slop-bot couldn't read ticket %s (%v)", p.Ticket, err))
	}
	if t.Status.Terminal() {
		return deny(fmt.Sprintf("ticket %s is %s", t.ID, t.Status))
	}
	if t.AutoMerge {
		return &Verdict{Allow: true, Finding: f}
	}
	granted, err := g.opts.Grants.Granted(ctx, t.ID)
	if err != nil {
		return deny(fmt.Sprintf("slop-bot couldn't read whether the user authorised it (%v)", err))
	}
	if granted {
		return &Verdict{Allow: true, Finding: f}
	}
	return deny(fmt.Sprintf("ticket %s has no AutoMerge, and the user hasn't authorised a merge", t.ID))
}

// refusal is what the agent reads in place of the command's output: why,
// and what to do instead.
func refusal(p ident.Principal, f *Finding, why string) string {
	return fmt.Sprintf("slop-bot's merge guard refused this: it looks like a merge (%s), and %s. "+
		"Run `slop-bot ready-to-merge [--pr <url>]` and end your turn; the user authorises the merge with "+
		"`slop-bot tell %s --merge`. If this isn't a merge, rewrite the command so the guard can see that "+
		"(for example, name the branch you push to).", f.Detail, why, p.Agent)
}

// Unreachable is the hook's own answer when the daemon can't be asked:
// refuse the merge, allow everything else.
func Unreachable(ctx context.Context, c *Classifier, call *Call, err error) *Verdict {
	f := c.Classify(ctx, call)
	if f == nil {
		return &Verdict{Allow: true}
	}
	return &Verdict{Finding: f, Reason: fmt.Sprintf("slop-bot's merge guard refused this: it looks like a merge (%s), "+
		"and slop-bot's daemon couldn't be asked whether it's authorised (%v), so merges are refused until it can be.", f.Detail, err)}
}
