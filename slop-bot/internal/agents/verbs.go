package agents

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/lowaa/slop-factory/slop-bot/internal/harness"
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
	m.declare(ctx, r, declAsk, "")
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
	m.declare(ctx, r, declDiscuss, "")
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
	m.declare(ctx, r, declMerge, "")
	m.tellUser(ctx, r.id, fmt.Sprintf("ticket %s is ready to merge%s: `slop-bot tell %s merge` when it should", r.ticket, prSuffix(pr), r.id))
	return nil
}

func prSuffix(pr string) string {
	if pr == "" {
		return ""
	}
	return " (" + pr + ")"
}

// ErrCantFinish is `done` refused: the ticket can't reach done from where it
// is, so the agent hears why at once rather than being ended with the
// ticket left behind.
var ErrCantFinish = errors.New("agents: the ticket can't be marked done")

// Done is `slop-bot done`: the agent judges the ticket's goal met. It
// finishes when the current turn ends. It is refused now for a ticket that
// couldn't get there: one in discussion (the user resolves that first), or
// one whose work hasn't started and is blocked.
func (m *Manager) Done(ctx context.Context, p ident.Principal, summary string) error {
	r, err := m.own(p)
	if err != nil {
		return err
	}
	t, err := m.opts.Tickets.Get(ctx, r.ticket)
	if err != nil {
		return err
	}
	if err := canFinish(t); err != nil {
		return err
	}
	m.declare(ctx, r, declDone, summary)
	return nil
}

func canFinish(t *tickets.Ticket) error {
	switch t.Status {
	case tickets.StatusInProgress:
		return nil
	case tickets.StatusPreparing, tickets.StatusOpen:
		if len(t.BlockedBy) > 0 {
			return fmt.Errorf("%w: %s is blocked by %s, and its work can't start until they are done", ErrCantFinish, t.ID, refs(t.BlockedBy))
		}
		return nil
	case tickets.StatusDiscussion:
		return fmt.Errorf("%w: %s is in discussion, which only the user resolves", ErrCantFinish, t.ID)
	}
	return fmt.Errorf("%w: %s is %s", ErrCantFinish, t.ID, t.Status)
}

// SetNote is `slop-bot status set`, the agent's progress note.
func (m *Manager) SetNote(ctx context.Context, p ident.Principal, note string) error {
	r, err := m.own(p)
	if err != nil {
		return err
	}
	return m.opts.State.Agents.SetProgressNote(ctx, r.id, note)
}

// finish ends an agent whose ticket is done: the ticket is walked on to
// done, the summary goes on it, the conversation stops, and the worktrees
// are cleaned up, except any holding work no remote has. When the ticket
// can't be moved to done, nothing ends and the error says why. Stopping and
// cleaning up run on their own goroutine, so a caller on the scheduler or
// the runner's own loop is never held up.
func (m *Manager) finish(ctx context.Context, r *runner, summary string) error {
	by := ident.AgentPrincipal(r.id, r.ticket)
	r.setFinishing(true)
	if err := m.advanceTo(ctx, by, r.ticket, tickets.StatusDone); err != nil {
		r.setFinishing(false)
		return err
	}
	if summary != "" {
		if _, err := m.opts.Tickets.Comment(ctx, by, r.ticket, "**Done.** "+summary); err != nil {
			m.opts.Logger.Warn("agents: posting the summary", "agent", r.id, "err", err)
		}
	}
	m.end(ctx, r, false, "the ticket is done")
	return nil
}

// end stops a running agent for good and cleans up after it, once: force
// cuts its turn off, for a ticket that is no longer wanted.
func (m *Manager) end(ctx context.Context, r *runner, force bool, reason string) {
	r.mu.Lock()
	if r.ending {
		r.mu.Unlock()
		return
	}
	r.ending, r.stopping = true, true
	r.mu.Unlock()
	m.revokeMerge(ctx, r.ticket)
	m.ends.Go(func() {
		ctx := context.WithoutCancel(ctx)
		r.stopConv(ctx, force)
		<-r.exited
		m.cleanup(ctx, r.id, r.ticket)
		r.setState(ctx, state.StateDone, reason)
		m.event(ctx, EventFinished, r.id, r.ticket, nil)
		m.forget(r.id)
	})
}

// cleanup removes an agent's recorded worktrees.
func (m *Manager) cleanup(ctx context.Context, id ident.AgentID, ticket ident.TicketID) {
	a, err := m.opts.State.Agents.Get(ctx, id)
	if err != nil {
		return
	}
	m.forgetWorkspace(a)
	if len(a.Worktrees) == 0 {
		return
	}
	m.reportCleanup(ctx, id, ticket, m.opts.Worktrees.Cleanup(ctx, ticket, m.worktreesOf(a)))
}

// workspaceForgetter is a harness that keeps something of its own per
// workspace outside it (agy's sidecar folder, Codex's trust entries), to
// remove once the agent is done with it.
type workspaceForgetter interface {
	ForgetWorkspace(ws *harness.Workspace) error
}

// forgetWorkspace has a's harness remove what it kept for a's workspace.
// Its folders are slop-bot's own (worktrees, or the agent's directory), so
// nothing the user made is touched.
func (m *Manager) forgetWorkspace(a *state.Agent) {
	h, ok := m.opts.Harnesses.Harness(a.Harness)
	f, forgets := h.(workspaceForgetter)
	if !ok || !forgets {
		return
	}
	ws := &harness.Workspace{Agent: a.ID, Ticket: a.Ticket, Dir: m.opts.Paths.AgentDir(string(a.ID))}
	for _, w := range a.Worktrees {
		if w.Primary {
			ws.Dir = w.Path
		} else {
			ws.ExtraDirs = append(ws.ExtraDirs, w.Path)
		}
	}
	if err := f.ForgetWorkspace(ws); err != nil {
		m.opts.Logger.Warn("agents: removing what the harness kept for a workspace", "agent", a.ID, "err", err)
	}
}

// reportCleanup forgets the worktrees a cleanup removed, and tells the user
// about the rest: kept for the work they hold, or failed. A removed
// worktree can still come with an error, for branches it couldn't delete.
func (m *Manager) reportCleanup(ctx context.Context, id ident.AgentID, ticket ident.TicketID, results []*worktree.CleanupResult) {
	for _, res := range results {
		if res.Removed && id != "" {
			_ = m.opts.State.Agents.RemoveWorktree(ctx, id, res.Worktree.Repo)
		}
		switch {
		case res.Err != nil:
			m.tellUser(ctx, id, fmt.Sprintf("couldn't fully clean up %s: %v", res.Worktree.Path, res.Err))
		case !res.Removed:
			var why []string
			for _, reason := range res.Reasons {
				why = append(why, fmt.Sprintf("%s %s", reason.Kind, reason.Detail))
			}
			m.tellUser(ctx, id, fmt.Sprintf("kept %s (ticket %s), which holds work: %s. Once you've looked, remove it by hand: "+
				"git -C %s worktree remove --force %s", res.Worktree.Path, ticket, strings.Join(why, "; "), res.Worktree.RepoPath, res.Worktree.Path))
		}
	}
}

// forward is the order advanceTo walks a ticket in.
var forward = []tickets.Status{tickets.StatusPreparing, tickets.StatusOpen, tickets.StatusInProgress, tickets.StatusDone}

// advanceTo walks a ticket forward to target (preparing, open, in_progress,
// done), as the ticket's agent, and never back: from in_progress to done is
// one step, not a detour through open, and a ticket off that path (in
// discussion, say) isn't moved at all. It reports why the ticket didn't get
// there; one already at or past target is no error.
func (m *Manager) advanceTo(ctx context.Context, by ident.Principal, id ident.TicketID, target tickets.Status) error {
	to := slices.Index(forward, target)
	// Each step is conditional on the status this walk read, so it can't
	// undo a move made since (the agent parking the ticket in discussion,
	// say). A step refused that way reads again: the move may have been
	// this same walk made by another caller, which it carries on from.
	// Every step moves forward, so the attempts are bounded.
	for range 2 * len(forward) {
		t, err := m.opts.Tickets.Get(ctx, id)
		if err != nil {
			return err
		}
		from := slices.Index(forward, t.Status)
		switch {
		case t.Status == target || (from >= 0 && from > to):
			return nil
		case from < 0:
			return fmt.Errorf("%w: %s is %s", ErrCantFinish, id, t.Status)
		}
		st, seen := forward[from+1], t.Status
		_, err = m.opts.Tickets.Update(ctx, by, id, &tickets.TicketChange{Status: &st, IfStatus: &seen})
		if err != nil && !errors.Is(err, tickets.ErrStatusChanged) {
			return fmt.Errorf("moving ticket %s to %s: %w", id, st, err)
		}
	}
	return fmt.Errorf("agents: ticket %s keeps moving; it didn't reach %s", id, target)
}
