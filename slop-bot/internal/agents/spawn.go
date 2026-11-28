package agents

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/lowaa/slop-factory/slop-bot/internal/agentspec"
	"github.com/lowaa/slop-factory/slop-bot/internal/briefing"
	"github.com/lowaa/slop-factory/slop-bot/internal/control"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness"
	"github.com/lowaa/slop-factory/slop-bot/internal/ident"
	"github.com/lowaa/slop-factory/slop-bot/internal/state"
	"github.com/lowaa/slop-factory/slop-bot/internal/tickets"
	"github.com/lowaa/slop-factory/slop-bot/internal/worktree"
)

// errMisconfigured is a ticket whose agent spec can't run here.
var errMisconfigured = errors.New("agents: misconfigured")

// spawn starts an agent for t, which the scheduler found eligible.
func (m *Manager) spawn(ctx context.Context, t *tickets.Ticket, pool state.Pool) error {
	spec := agentspec.Resolve(t.Agent, m.opts.Config)
	h, ok := m.opts.Harnesses.Harness(spec.Harness)
	if err := agentspec.Validate(spec, m.opts.Config); err != nil || !ok {
		if err == nil {
			err = fmt.Errorf("%w: harness %q has no implementation in this build", agentspec.ErrInvalid, spec.Harness)
		}
		return m.misconfigure(ctx, t, err)
	}
	id, err := newAgentID(t.ID)
	if err != nil {
		return err
	}
	token, err := control.NewToken()
	if err != nil {
		return err
	}
	var wts []*worktree.Worktree
	if len(t.Repos) > 0 {
		if wts, err = m.opts.Worktrees.Create(ctx, t.ID, t.Repos, t.Repos[0]); err != nil {
			return m.misconfigure(ctx, t, fmt.Errorf("creating worktrees: %w", err))
		}
	}
	if _, err := m.opts.State.Agents.Create(ctx, &state.NewAgent{
		// The configured name, not the implementation's: resume looks the
		// harness up by it.
		ID: id, Ticket: t.ID, Harness: spec.Harness, Driver: h.Driver(), Model: spec.Model, Effort: spec.Effort,
		Pool: pool, State: state.StateStarting, Reason: "spawned", TokenHash: control.HashToken(token),
		Worktrees: stateWorktrees(wts),
	}); err != nil {
		return err
	}
	ws, err := m.workspace(id, t, spec, wts, token)
	if err != nil {
		m.abandon(ctx, id, err)
		return err
	}
	conv, err := h.Start(ctx, ws, nil)
	if err != nil {
		m.abandon(ctx, id, err)
		return fmt.Errorf("agents: starting %s for ticket %s: %w", h.Name(), t.ID, err)
	}
	r := m.adopt(ctx, &adoption{id: id, ticket: t.ID, pool: pool, conv: conv, caps: h.Capabilities(), st: state.StateStarting})
	m.event(ctx, EventSpawned, id, t.ID, map[string]string{"harness": spec.Harness, "driver": h.Driver()})
	return r.conv.Send(ctx, &harness.Message{From: ident.UserPrincipal(), Text: kickoff(t)})
}

// misconfigure parks a ticket whose spec can't run, and tells the user how
// to fix it: never a crashed agent.
func (m *Manager) misconfigure(ctx context.Context, t *tickets.Ticket, cause error) error {
	st := tickets.StatusMisconfigured
	if _, err := m.opts.Tickets.Update(ctx, ident.UserPrincipal(), t.ID, &tickets.TicketChange{Status: &st}); err != nil {
		m.opts.Logger.Warn("agents: marking a ticket misconfigured", "ticket", t.ID, "err", err)
	}
	m.event(ctx, EventMisconfigure, "", t.ID, map[string]string{"error": cause.Error()})
	m.tellUser(ctx, "", fmt.Sprintf("ticket %s won't be scheduled: %v. Fix it with `slop-bot ticket set %s ...`, "+
		"then `slop-bot ticket set %s --status preparing`.", t.ID, cause, t.ID, t.ID))
	return fmt.Errorf("%w: ticket %s: %w", errMisconfigured, t.ID, cause)
}

// abandon ends an agent that never got going, so its slot frees.
func (m *Manager) abandon(ctx context.Context, id ident.AgentID, cause error) {
	if _, err := m.opts.State.Agents.SetState(ctx, &state.Transition{Agent: id, To: state.StateCrashed, Reason: cause.Error()}); err != nil {
		m.opts.Logger.Warn("agents: abandoning", "agent", id, "err", err)
	}
	m.event(ctx, EventSpawnFailed, id, "", map[string]string{"error": cause.Error()})
	m.tellUser(ctx, id, fmt.Sprintf("agent %s failed to start: %v", id, cause))
}

// adoption is a conversation the Manager is taking on, new or resumed.
type adoption struct {
	id     ident.AgentID
	ticket ident.TicketID
	pool   state.Pool
	conv   harness.Conversation
	caps   harness.Capabilities
	st     state.AgentState
}

func (m *Manager) adopt(ctx context.Context, a *adoption) *runner {
	id := a.id
	conv := a.conv
	r := &runner{m: m, id: id, ticket: a.ticket, pool: a.pool, conv: conv, caps: a.caps, st: a.st, exited: make(chan struct{})}
	m.mu.Lock()
	m.runners[id] = r
	m.mu.Unlock()
	if err := m.opts.State.Agents.SetConversationID(ctx, id, conv.Ref().ID); err != nil {
		m.opts.Logger.Warn("agents: recording the conversation id", "agent", id, "err", err)
	}
	go r.run(context.WithoutCancel(ctx))
	return r
}

// workspace is everything the harness needs: the primary worktree as cwd,
// the briefing rendered outside the worktree, and the agent's token.
func (m *Manager) workspace(id ident.AgentID, t *tickets.Ticket, spec tickets.AgentSpec, wts []*worktree.Worktree, token string) (*harness.Workspace, error) {
	dir := m.opts.Paths.AgentDir(string(id))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	data := &briefing.Data{
		Ticket:             briefing.Ticket{ID: string(t.ID), Title: t.Title, AutoMerge: t.AutoMerge},
		CLI:                m.opts.CLI,
		Nudges:             m.opts.Config.Agents.Nudges,
		Autonomy:           m.autonomy(context.Background()),
		AgentsMaySetUrgent: *m.opts.Config.AgentsMaySetUrgent,
		CLIReference:       cliReference,
	}
	for _, r := range m.opts.Config.Repos {
		data.Repos = append(data.Repos, briefing.Repo{Name: r.Name, Path: r.Path})
	}
	ws := &harness.Workspace{
		Agent: id, Ticket: t.ID, Dir: dir, Model: spec.Model, Effort: spec.Effort,
		Env: map[string]string{control.TokenEnv: token},
	}
	if hc := m.opts.Config.Harnesses[spec.Harness]; hc != nil {
		ws.Permissive = *hc.Permissive
		ws.APIKeyOptIn = hc.APIKeyOptIn
	}
	for _, w := range wts {
		data.Worktrees = append(data.Worktrees, briefing.Worktree{Repo: w.Repo, Path: w.Path, Branch: w.Branch, Primary: w.Primary})
		if w.Primary {
			ws.Dir = w.Path
		} else {
			ws.ExtraDirs = append(ws.ExtraDirs, w.Path)
		}
	}
	path, text, err := briefing.Write(dir, data, m.opts.Config.BriefingPath)
	if err != nil {
		return nil, err
	}
	ws.Briefing, ws.BriefingFile = text, path
	return ws, nil
}

// cliReference is set by the daemon from the CLI's command tree, so the
// briefing's command list can't drift from the CLI.
var cliReference string

// SetCLIReference installs the generated CLI reference for briefings.
func SetCLIReference(ref string) { cliReference = ref }

func kickoff(t *tickets.Ticket) string {
	var b strings.Builder
	fmt.Fprintf(&b, "You've been assigned ticket %s: %q.\n\n", t.ID, t.Title)
	if t.Body != "" {
		fmt.Fprintf(&b, "%s\n\n", strings.TrimSpace(t.Body))
	}
	switch {
	case t.Status == tickets.StatusPreparing && len(t.BlockedBy) > 0:
		fmt.Fprintf(&b, "It is being prepared, and is blocked by %s. Refine it now; don't start the work "+
			"until slop-bot tells you its blockers are done.", refs(t.BlockedBy))
	case t.Status == tickets.StatusPreparing:
		b.WriteString("It is being prepared: refine it first (see your briefing), then do the work.")
	default:
		b.WriteString("It is open: do the work (see your briefing).")
	}
	return b.String()
}

func refs(rs []tickets.TicketRef) string {
	ids := make([]string, len(rs))
	for i, r := range rs {
		ids[i] = string(r.ID)
	}
	return strings.Join(ids, ", ")
}

func stateWorktrees(wts []*worktree.Worktree) []state.Worktree {
	out := make([]state.Worktree, 0, len(wts))
	for _, w := range wts {
		out = append(out, state.Worktree{Repo: w.Repo, Path: w.Path, Branch: w.Branch, Primary: w.Primary})
	}
	return out
}

// resumeAll restarts, by conversation id, every agent that was running when
// the daemon last stopped.
func (m *Manager) resumeAll(ctx context.Context) error {
	live, err := m.opts.State.Agents.ListLive(ctx)
	if err != nil {
		return err
	}
	for _, a := range live {
		if a.Mode == state.ModeAttached || a.ConversationID == "" ||
			a.State == state.StateCrashed || a.State == state.StateExited {
			continue
		}
		if err := m.resume(ctx, a); err != nil {
			m.opts.Logger.Warn("agents: resuming", "agent", a.ID, "err", err)
			m.tellUser(ctx, a.ID, fmt.Sprintf("agent %s (ticket %s) couldn't be resumed after a restart: %v", a.ID, a.Ticket, err))
		}
	}
	return nil
}

func (m *Manager) resume(ctx context.Context, a *state.Agent) error {
	h, ok := m.opts.Harnesses.Harness(a.Harness)
	if !ok {
		return fmt.Errorf("harness %q isn't available", a.Harness)
	}
	t, err := m.opts.Tickets.Get(ctx, a.Ticket)
	if err != nil {
		return err
	}
	// state.db holds only token hashes, so the resumed agent gets a new one.
	token, err := control.NewToken()
	if err != nil {
		return err
	}
	if err := m.opts.State.Agents.RotateTokenHash(ctx, a.ID, control.HashToken(token)); err != nil {
		return err
	}
	var wts []*worktree.Worktree
	for _, w := range a.Worktrees {
		wts = append(wts, &worktree.Worktree{Ticket: a.Ticket, Repo: w.Repo, Path: w.Path, Branch: w.Branch, Primary: w.Primary})
	}
	ws, err := m.workspace(a.ID, t, tickets.AgentSpec{Harness: a.Harness, Model: a.Model, Effort: a.Effort}, wts, token)
	if err != nil {
		return err
	}
	conv, err := h.Start(ctx, ws, &harness.ConversationRef{Harness: a.Harness, Driver: a.Driver, ID: a.ConversationID})
	if err != nil {
		return err
	}
	m.adopt(ctx, &adoption{id: a.ID, ticket: a.Ticket, pool: a.Pool, conv: conv, caps: h.Capabilities(), st: a.State})
	m.event(ctx, EventResumed, a.ID, a.Ticket, nil)
	// A waiting agent stays waiting; one that was mid-work is told why its
	// last turn may have been cut short.
	switch a.State {
	case state.StateWaitingUser, state.StateDiscussion, state.StateWaitingMerge, state.StateIdle:
		return nil
	}
	_, err = m.opts.State.Messages.Enqueue(ctx, &state.NewMessage{
		From: state.UserParty, To: state.AgentParty(a.ID), Kind: state.KindSystem,
		Body: "slop-bot restarted, so your last turn may have been cut short. Check where you got to, then carry on.",
	})
	if err != nil {
		return err
	}
	if r, ok := m.runner(a.ID); ok {
		m.deliverPending(ctx, r)
	}
	return nil
}
