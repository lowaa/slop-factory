package tickets

import (
	"errors"
	"fmt"
	"strings"

	"github.com/lowaa/slop-factory/slop-bot/internal/ident"
)

// The rules every implementation enforces. They are pure checks on the
// state an implementation has loaded, so it calls them inside the
// transaction that writes the change: the state they judge can't move
// before the write commits.
//
// They tell the user from agents to guard against a confused agent, not a
// malicious one (see ident.Principal).

var (
	// ErrNotFound is also what a malformed ticket id gets: no ticket has it.
	ErrNotFound = errors.New("tickets: not found")
	// ErrInvalid is a request that can never succeed as made: a blank
	// title, an unknown status, a blank comment.
	ErrInvalid          = errors.New("tickets: invalid request")
	ErrInvalidPrincipal = errors.New("tickets: invalid principal")
	// ErrNotPermitted is the principal not being allowed to make the change.
	// Each refusal below wraps it.
	ErrNotPermitted = errors.New("tickets: not permitted")
	// ErrInvalidTransition is a status move the transition table lacks.
	ErrInvalidTransition = errors.New("tickets: invalid status transition")
	// ErrCycle is a blocker that would make a ticket wait on itself, which
	// would leave it unschedulable forever with nothing to say so.
	ErrCycle = errors.New("tickets: blocker would create a cycle")
	// ErrBlocked refuses starting the work on a ticket with open blockers.
	ErrBlocked = errors.New("tickets: ticket has open blockers")

	ErrInvalidCursor  = errors.New("tickets: invalid cursor")
	ErrAlreadyStarted = errors.New("tickets: already started")
	ErrStopped        = errors.New("tickets: stopped")
)

var (
	// ErrAutoMergeUserOnly: otherwise a misbehaving agent could grant itself
	// merge rights.
	ErrAutoMergeUserOnly = fmt.Errorf("%w: only the user may set AutoMerge", ErrNotPermitted)
	// ErrUrgentNotAllowed: agents_may_set_urgent is off.
	ErrUrgentNotAllowed = fmt.Errorf("%w: agents may not change Urgent (agents_may_set_urgent is off)", ErrNotPermitted)
	// ErrUrgentReasonRequired: an agent marking a ticket urgent must say
	// why, so the user can judge it.
	ErrUrgentReasonRequired = fmt.Errorf("%w: an agent marking a ticket urgent must give a reason", ErrInvalid)
	// ErrUserOnly covers what only a person decides: assignment, and
	// reopening a finished ticket.
	ErrUserOnly = fmt.Errorf("%w: only the user may do this", ErrNotPermitted)
	// ErrNotEditable refuses an agent editing a ticket that is neither its
	// own nor one it filed that hasn't started.
	ErrNotEditable = fmt.Errorf("%w: an agent may edit only its own ticket, "+
		"or one it filed that is still preparing or open. To ask for a change, comment on it", ErrNotPermitted)
	// ErrAutoMergeScopeLocked refuses an agent rewriting a ticket it filed
	// that has AutoMerge.
	ErrAutoMergeScopeLocked = fmt.Errorf("%w: an agent may not rewrite a ticket it filed that has AutoMerge. "+
		"To change it, comment on it", ErrNotPermitted)
	// ErrStatusNotPermitted refuses an agent moving the status of a ticket
	// other than its own.
	ErrStatusNotPermitted = fmt.Errorf("%w: an agent moves only its own ticket's status, "+
		"and may only cancel a ticket it filed", ErrNotPermitted)
	// ErrBlockersNotEditable is ErrNotEditable for blockers. Gating another
	// agent's work, or a person's, is a person's call.
	ErrBlockersNotEditable = fmt.Errorf("%w: an agent may edit the blockers only of its own ticket, "+
		"or of one it filed that is still preparing or open. To ask for this one, comment on it", ErrNotPermitted)
	// ErrBlockerNotRemovable refuses an agent removing an edge it did not
	// add. A person's edge is often a deliberate hold on work.
	ErrBlockerNotRemovable = fmt.Errorf("%w: an agent may remove only a blocker it added itself. "+
		"To ask for this one to be lifted, comment on the ticket", ErrNotPermitted)
)

// CheckPrincipal refuses a malformed principal.
func CheckPrincipal(by ident.Principal) error {
	if !by.Valid() {
		return fmt.Errorf("%w: %+v", ErrInvalidPrincipal, by)
	}
	return nil
}

// CheckNew judges a ticket to be filed by `by`. The implementation still
// sets an agent's ticket's AutoMerge from its parent (InheritedAutoMerge).
func CheckNew(by ident.Principal, cfg *Config, nt *NewTicket) error {
	if err := CheckPrincipal(by); err != nil {
		return err
	}
	if strings.TrimSpace(nt.Title) == "" {
		return fmt.Errorf("%w: a ticket needs a title", ErrInvalid)
	}
	switch nt.Status {
	case "", StatusPreparing:
	case StatusOpen:
		if !by.IsUser() {
			return fmt.Errorf("%w: an agent's ticket starts preparing", ErrNotPermitted)
		}
	default:
		return fmt.Errorf("%w: a ticket starts preparing or open, not %q", ErrInvalid, nt.Status)
	}
	if nt.AutoMerge && !by.IsUser() {
		return ErrAutoMergeUserOnly
	}
	if err := checkUrgent(by, cfg, &UrgentChange{Urgent: nt.Urgent, Reason: nt.UrgentReason}, nt.Urgent); err != nil {
		return err
	}
	return checkRepos(nt.Repos)
}

// InitialStatus is the status a NewTicket that passed CheckNew starts in.
func InitialStatus(nt *NewTicket) Status {
	if nt.Status == "" {
		return StatusPreparing
	}
	return nt.Status
}

// InheritedAutoMerge is the AutoMerge a ticket filed by `by` gets: the
// user's choice, or the creating ticket's for an agent.
func InheritedAutoMerge(by ident.Principal, nt *NewTicket, parent *Ticket) bool {
	if by.IsUser() {
		return nt.AutoMerge
	}
	return parent.AutoMerge
}

// CheckChange judges change to cur by `by`.
func CheckChange(by ident.Principal, cfg *Config, cur *Ticket, change *TicketChange) error {
	if err := CheckPrincipal(by); err != nil {
		return err
	}
	// Before editability, so an agent reopening its finished ticket hears
	// that reopening is the user's, not that the ticket isn't its to edit.
	if change.Status != nil {
		if err := checkStatusAuthority(by, cur, *change.Status); err != nil {
			return err
		}
	}
	if !by.IsUser() && !AgentMayEdit(by, cur) {
		return fmt.Errorf("%w (%s is %s)", ErrNotEditable, cur.ID, cur.Status)
	}
	// AutoMerge is granted against what a ticket says. A filer rewriting its
	// filing afterwards would steer what merges without a person.
	if !by.IsUser() && by.Ticket != cur.ID && cur.AutoMerge && changesScope(change) {
		return fmt.Errorf("%w (%s)", ErrAutoMergeScopeLocked, cur.ID)
	}
	if change.Title != nil && strings.TrimSpace(*change.Title) == "" {
		return fmt.Errorf("%w: a ticket needs a title", ErrInvalid)
	}
	if change.AutoMerge != nil && !by.IsUser() {
		return ErrAutoMergeUserOnly
	}
	if change.Urgent != nil {
		if err := checkUrgent(by, cfg, change.Urgent, true); err != nil {
			return err
		}
	}
	if change.Repos != nil {
		if err := checkRepos(*change.Repos); err != nil {
			return err
		}
	}
	if change.Status != nil {
		return checkTransition(cur, *change.Status)
	}
	return nil
}

// checkStatusAuthority is who may move a ticket's status. The user may make
// any move. An agent moves its own ticket's, and may only cancel one it
// filed: moving a filing on to open or in_progress itself would skip the
// refinement another agent is meant to give it.
func changesScope(c *TicketChange) bool {
	return c.Title != nil || c.Body != nil || c.Repos != nil || c.Agent != nil
}

func checkStatusAuthority(by ident.Principal, cur *Ticket, to Status) error {
	if by.IsUser() || to == cur.Status {
		return nil
	}
	if cur.Status.Terminal() {
		return fmt.Errorf("%w: reopening %s", ErrUserOnly, cur.ID)
	}
	if by.Ticket != cur.ID && to != StatusCancelled {
		return fmt.Errorf("%w (%s to %s)", ErrStatusNotPermitted, cur.ID, to)
	}
	return nil
}

func checkTransition(cur *Ticket, to Status) error {
	if !to.Valid() {
		return fmt.Errorf("%w: unknown status %q", ErrInvalid, to)
	}
	if !CanTransition(cur.Status, to) {
		return fmt.Errorf("%w: %s to %s", ErrInvalidTransition, cur.Status, to)
	}
	// Into in_progress from any status: there is no telling here whether the
	// work started before, so a ticket that waits on something waits, even
	// one blocked mid-work and coming back from a discussion.
	if to == StatusInProgress && cur.Status != StatusInProgress && len(cur.BlockedBy) > 0 {
		return fmt.Errorf("%w: %s waits on %d open blocker(s)", ErrBlocked, cur.ID, len(cur.BlockedBy))
	}
	return nil
}

// checkUrgent: the user may always change Urgent. An agent may change it
// only when the config allows, and setting it needs a reason. touched is
// whether the request changes the flag at all.
func checkUrgent(by ident.Principal, cfg *Config, u *UrgentChange, touched bool) error {
	if !u.Urgent && strings.TrimSpace(u.Reason) != "" {
		return fmt.Errorf("%w: an urgent reason without Urgent", ErrInvalid)
	}
	if !touched || by.IsUser() {
		return nil
	}
	if !cfg.AgentsMaySetUrgent {
		return ErrUrgentNotAllowed
	}
	if u.Urgent && strings.TrimSpace(u.Reason) == "" {
		return ErrUrgentReasonRequired
	}
	return nil
}

func checkRepos(repos []string) error {
	for _, r := range repos {
		if strings.TrimSpace(r) == "" {
			return fmt.Errorf("%w: a blank repo", ErrInvalid)
		}
	}
	return nil
}

// CheckAssign: assignment is the user's. With autonomy on, the bot assigns
// an agent's new ticket through NewTicket.Assignee, not here.
func CheckAssign(by ident.Principal) error {
	if err := CheckPrincipal(by); err != nil {
		return err
	}
	if !by.IsUser() {
		return fmt.Errorf("%w: assigning a ticket", ErrUserOnly)
	}
	return nil
}

// CheckComment refuses a blank comment.
func CheckComment(by ident.Principal, body string) error {
	if err := CheckPrincipal(by); err != nil {
		return err
	}
	if strings.TrimSpace(body) == "" {
		return fmt.Errorf("%w: a blank comment", ErrInvalid)
	}
	return nil
}

// AgentMayEdit is which tickets an agent may change, blockers included: its
// own while it is live, or one it filed that nobody has started (the
// follow-ups it splits off, which it must be able to chain and correct).
// Anything else is another agent's work or a person's queue, and changing
// those silently changes what the fleet is handed.
func AgentMayEdit(by ident.Principal, t *Ticket) bool {
	if by.Ticket == t.ID {
		return !t.Status.Terminal()
	}
	filed := t.CreatedBy.Kind == ident.Agent && t.CreatedBy.Agent == by.Agent
	return filed && (t.Status == StatusPreparing || t.Status == StatusOpen)
}

// CheckBlockerEdit judges `by` changing t's blockers.
func CheckBlockerEdit(by ident.Principal, t *Ticket) error {
	if err := CheckPrincipal(by); err != nil {
		return err
	}
	if !by.IsUser() && !AgentMayEdit(by, t) {
		return fmt.Errorf("%w (%s is %s)", ErrBlockersNotEditable, t.ID, t.Status)
	}
	return nil
}

// CheckBlockerRemoval judges `by` removing an edge addedBy added: the user
// may remove any, an agent only its own. Call it only for an edge that
// exists: removing none is a no-op, not a refusal.
func CheckBlockerRemoval(by ident.Principal, addedBy ident.Principal) error {
	if by.IsUser() {
		return nil
	}
	if addedBy.Kind != ident.Agent || addedBy.Agent != by.Agent {
		return fmt.Errorf("%w (added by %s)", ErrBlockerNotRemovable, addedBy)
	}
	return nil
}
