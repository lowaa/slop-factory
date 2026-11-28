package agents

import (
	"context"
	"fmt"
	"strings"

	"github.com/lowaa/slop-factory/slop-bot/internal/ident"
	"github.com/lowaa/slop-factory/slop-bot/internal/state"
	"github.com/lowaa/slop-factory/slop-bot/internal/tickets"
	"github.com/lowaa/slop-factory/slop-bot/internal/worktree"
)

// own returns the runner for the calling agent: verbs act on the caller's
// own ticket only.
func (m *Manager) own(p ident.Principal) (*runner, error) {
	if p.IsUser() || !p.Valid() {
		return nil, fmt.Errorf("%w: run it as the agent, from its session", ErrNotYourTicket)
	}
	r, ok := m.runner(p.Agent)
	if !ok {
		return nil, fmt.Errorf("%w: agent %s isn't running", ErrNoLiveAgent, p.Agent)
	}
	return r, nil
}

// Ask is `slop-bot ask`. It returns at once: the agent carries on with what
// doesn't depend on the answer, and is waiting_user once its turn ends.
func (m *Manager) Ask(ctx context.Context, p ident.Principal, question string, options []string, recommend string) (*state.Ask, error) {
	r, err := m.own(p)
	if err != nil {
		return nil, err
	}
	ask, err := m.opts.State.Messages.Ask(ctx, &state.NewAsk{From: r.id, Question: question, Options: options, Recommendation: recommend})
	if err != nil {
		return nil, err
	}
	r.declare(declAsk, "")
	m.event(ctx, "agent.asked", r.id, r.ticket, map[string]any{"question": question})
	return ask, nil
}

// Discuss is `slop-bot discuss`: a risky decision for human review. The
// ticket goes into discussion, the decision goes on it, and the agent waits.
func (m *Manager) Discuss(ctx context.Context, p ident.Principal, decision string, options []string, recommend string) error {
	r, err := m.own(p)
	if err != nil {
		return err
	}
	st := tickets.StatusDiscussion
	if _, err := m.opts.Tickets.Update(ctx, p, r.ticket, &tickets.TicketChange{Status: &st}); err != nil {
		return err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "**Discussion: a risky decision needs review.**\n\n%s\n", decision)
	for i, o := range options {
		fmt.Fprintf(&b, "\n%d. %s", i+1, o)
	}
	if recommend != "" {
		fmt.Fprintf(&b, "\n\nRecommendation: %s", recommend)
	}
	if _, err := m.opts.Tickets.Comment(ctx, p, r.ticket, b.String()); err != nil {
		return err
	}
	r.declare(declDiscuss, "")
	m.tellUser(ctx, r.id, fmt.Sprintf("ticket %s needs a decision: `slop-bot discussions`", r.ticket))
	return nil
}

// ResolveDiscussion is `slop-bot discussions resolve`, the user's answer.
// The ticket goes back to being refined or worked, and the agent is told.
func (m *Manager) ResolveDiscussion(ctx context.Context, p ident.Principal, ticket ident.TicketID, resolution string) error {
	if !p.IsUser() {
		return ErrUserOnly
	}
	t, err := m.opts.Tickets.Get(ctx, ticket)
	if err != nil {
		return err
	}
	if t.Status != tickets.StatusDiscussion {
		return fmt.Errorf("%w: ticket %s is %s", ErrNotInDiscusson, ticket, t.Status)
	}
	if _, err := m.opts.Tickets.Comment(ctx, p, ticket, "**Discussion resolved:** "+resolution); err != nil {
		return err
	}
	back := tickets.StatusOpen
	if _, err := m.opts.Tickets.Update(ctx, p, ticket, &tickets.TicketChange{Status: &back}); err != nil {
		return err
	}
	a, err := m.opts.State.Agents.LiveForTicket(ctx, ticket)
	if err != nil {
		return nil // no agent: the ticket is open, and the scheduler will start one
	}
	_, err = m.Tell(ctx, p, string(a.ID), "Your discussion is resolved: "+resolution, false)
	return err
}

// ReadyToMerge is `slop-bot ready-to-merge`, for a ticket without AutoMerge.
func (m *Manager) ReadyToMerge(ctx context.Context, p ident.Principal, pr string) error {
	r, err := m.own(p)
	if err != nil {
		return err
	}
	t, err := m.opts.Tickets.Get(ctx, r.ticket)
	if err != nil {
		return err
	}
	if t.AutoMerge {
		return ErrHasAutoMerge
	}
	note := "Ready to merge."
	if pr != "" {
		note += " " + pr
	}
	if _, err := m.opts.Tickets.Comment(ctx, p, r.ticket, note); err != nil {
		return err
	}
	r.declare(declMerge, "")
	m.tellUser(ctx, r.id, fmt.Sprintf("ticket %s is ready to merge%s: `slop-bot tell %s merge` when it should", r.ticket, prSuffix(pr), r.id))
	return nil
}

func prSuffix(pr string) string {
	if pr == "" {
		return ""
	}
	return " (" + pr + ")"
}

// Done is `slop-bot done`: the agent judges the ticket's goal met. It
// finishes when the current turn ends.
func (m *Manager) Done(ctx context.Context, p ident.Principal, summary string) error {
	r, err := m.own(p)
	if err != nil {
		return err
	}
	r.declare(declDone, summary)
	return nil
}

// SetNote is `slop-bot status set`, the agent's progress note.
func (m *Manager) SetNote(ctx context.Context, p ident.Principal, note string) error {
	r, err := m.own(p)
	if err != nil {
		return err
	}
	return m.opts.State.Agents.SetProgressNote(ctx, r.id, note)
}

// finish ends an agent whose ticket is done: the summary goes on the
// ticket, the conversation stops, and the worktrees are cleaned up, except
// any holding work no remote has. It returns at once: stopping and cleaning
// up run on their own goroutine, so a caller on the scheduler or the
// runner's own loop is never held up.
func (m *Manager) finish(ctx context.Context, r *runner, summary string) {
	m.end(ctx, r, summary, false)
}

// cancel ends an agent whose ticket was cancelled: its turn is cut off.
func (m *Manager) cancel(ctx context.Context, r *runner) {
	m.end(ctx, r, "", true)
}

func (m *Manager) end(ctx context.Context, r *runner, summary string, force bool) {
	r.mu.Lock()
	r.stopping = true
	r.mu.Unlock()
	by := ident.AgentPrincipal(r.id, r.ticket)
	if summary != "" {
		if _, err := m.opts.Tickets.Comment(ctx, by, r.ticket, "**Done.** "+summary); err != nil {
			m.opts.Logger.Warn("agents: posting the summary", "agent", r.id, "err", err)
		}
	}
	if !force {
		m.advanceTo(ctx, by, r.ticket, tickets.StatusDone)
	}
	go func() {
		ctx := context.WithoutCancel(ctx)
		_ = r.conv.Stop(ctx, force)
		<-r.exited
		m.cleanup(ctx, r)
		reason := "the ticket is done"
		if force {
			reason = "the ticket was cancelled"
		}
		r.setState(ctx, state.StateDone, reason)
		m.event(ctx, EventFinished, r.id, r.ticket, nil)
		m.forget(r.id)
	}()
}

func (m *Manager) cleanup(ctx context.Context, r *runner) {
	a, err := m.opts.State.Agents.Get(ctx, r.id)
	if err != nil || len(a.Worktrees) == 0 {
		return
	}
	wts := make([]*worktree.Worktree, 0, len(a.Worktrees))
	for _, w := range a.Worktrees {
		wts = append(wts, &worktree.Worktree{Ticket: r.ticket, Repo: w.Repo, Path: w.Path, Branch: w.Branch, Primary: w.Primary})
	}
	for _, res := range m.opts.Worktrees.Cleanup(ctx, r.ticket, wts) {
		switch {
		case res.Removed:
			_ = m.opts.State.Agents.RemoveWorktree(ctx, r.id, res.Worktree.Repo)
		case res.Err != nil:
			m.tellUser(ctx, r.id, fmt.Sprintf("couldn't clean up %s: %v", res.Worktree.Path, res.Err))
		default:
			var why []string
			for _, reason := range res.Reasons {
				why = append(why, fmt.Sprintf("%s %s", reason.Kind, reason.Detail))
			}
			m.tellUser(ctx, r.id, fmt.Sprintf("kept %s, which holds work: %s. `slop-bot worktrees clean %s --force` once you've looked.",
				res.Worktree.Path, strings.Join(why, "; "), r.ticket))
		}
	}
}

// advanceTo walks a ticket forward to target through the statuses the
// rules allow (preparing → open → in_progress → done), as the ticket's
// agent: an agent can't reopen a ticket, so a move made on a stale read
// can't undo a ticket that has meanwhile finished.
func (m *Manager) advanceTo(ctx context.Context, by ident.Principal, id ident.TicketID, target tickets.Status) {
	path := []tickets.Status{tickets.StatusOpen, tickets.StatusInProgress, tickets.StatusDone}
	t, err := m.opts.Tickets.Get(ctx, id)
	if err != nil {
		return
	}
	for _, next := range path {
		if t.Status == target || t.Status.Terminal() {
			return
		}
		if next == t.Status || !tickets.CanTransition(t.Status, next) {
			continue
		}
		st := next
		if t, err = m.opts.Tickets.Update(ctx, by, id, &tickets.TicketChange{Status: &st}); err != nil {
			m.opts.Logger.Warn("agents: moving a ticket", "ticket", id, "to", next, "err", err)
			return
		}
		if next == target {
			return
		}
	}
}
