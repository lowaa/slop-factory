package agents

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/lowaa/slop-factory/slop-bot/internal/ident"
	"github.com/lowaa/slop-factory/slop-bot/internal/state"
	"github.com/lowaa/slop-factory/slop-bot/internal/tickets"
	"github.com/lowaa/slop-factory/slop-bot/internal/worktree"
)

// WorktreeStatus is where a recorded worktree stands, for `slop-bot
// worktrees`. The values are the API's.
type WorktreeStatus string

const (
	WorktreeInUse     WorktreeStatus = "in_use"
	WorktreeKept      WorktreeStatus = "kept"
	WorktreeReleased  WorktreeStatus = "released"
	WorktreeRemovable WorktreeStatus = "removable"
	WorktreeUnchecked WorktreeStatus = "unchecked"
)

var (
	// ErrWorktreesInUse is a clean refused because the ticket's agent is
	// live, or being started: its worktrees are its to work in.
	ErrWorktreesInUse = errors.New("agents: the ticket's worktrees are in use")
	// ErrNoWorktrees is a clean of a ticket with none recorded.
	ErrNoWorktrees = errors.New("agents: no worktrees recorded")
)

// Released is why a plain clean keeps a worktree released to a person
// (`attach --no-resume`): it is theirs, and their TUI may be working in it.
const Released worktree.ReasonKind = "released"

// WorktreeEntry is one recorded worktree and where it stands.
type WorktreeEntry struct {
	// Agent is the newest agent recording it.
	Agent    *state.Agent
	Worktree state.Worktree
	Status   WorktreeStatus
	// Reasons it would be kept; for an ended agent's only.
	Reasons []worktree.Reason
	// Err is a check that couldn't be made (WorktreeUnchecked).
	Err error
}

// ListWorktrees is every worktree state.db records, oldest agent first. A
// live agent's isn't checked: it is in use, and cleaned up when the ticket
// ends. An ended agent's is checked as Cleanup would check it.
func (m *Manager) ListWorktrees(ctx context.Context) ([]*WorktreeEntry, error) {
	held, err := m.recordedWorktrees(ctx, "")
	if err != nil {
		return nil, err
	}
	out := make([]*WorktreeEntry, 0, len(held))
	for _, h := range held {
		e := &WorktreeEntry{Agent: h.agent, Worktree: h.recorded, Status: WorktreeInUse}
		if h.agent.EndedAt != nil {
			m.check(ctx, e, h.wt)
		}
		out = append(out, e)
	}
	return out, nil
}

func (m *Manager) check(ctx context.Context, e *WorktreeEntry, wt *worktree.Worktree) {
	e.Reasons, e.Err = m.opts.Worktrees.Check(ctx, e.Agent.Ticket, wt)
	released := false
	if e.Agent.State == state.StateReleased {
		var err error
		if released, err = m.opts.State.Agents.ReleasedForTicket(ctx, e.Agent.Ticket); err != nil {
			e.Err = errors.Join(e.Err, err)
		}
	}
	switch {
	case e.Err != nil:
		e.Status = WorktreeUnchecked
	case released:
		e.Status = WorktreeReleased
	case len(e.Reasons) > 0:
		e.Status = WorktreeKept
	default:
		e.Status = WorktreeRemovable
	}
}

// CleanWorktrees cleans up ticket's worktrees on the user's say-so, as
// Cleanup does at the ticket's end, or with force, ForceCleanup: whatever
// they hold. A plain clean leaves a worktree released to a person alone.
// Besides what state.db records, it takes any worktree of the ticket's repos
// on disk: a first start that failed leaves one nothing records.
//
// It holds the ticket's start place, so no agent starts into a worktree
// being removed, and refuses while the ticket's agent is live. That counts
// as a start against the pools for the few seconds git takes; a clean is
// rare enough not to need a place of its own. The removal and forgetting
// run detached from every context (gitx bounds each command): cut short,
// they would leave a worktree removed but still recorded.
func (m *Manager) CleanWorktrees(_ context.Context, ticket ident.TicketID, force bool) ([]*worktree.CleanupResult, error) {
	claimed, done, err := m.claimStart(ticket, state.PoolNormal)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrWorktreesInUse, err)
	}
	defer done()
	ctx := context.WithoutCancel(claimed)
	switch a, err := m.opts.State.Agents.LiveForTicket(ctx, ticket); {
	case err == nil:
		return nil, fmt.Errorf("%w: ticket %s's agent %s is live; its worktrees are cleaned up when the ticket ends",
			ErrWorktreesInUse, ticket, a.ID)
	case !errors.Is(err, state.ErrNotFound):
		return nil, err
	}
	held, err := m.recordedWorktrees(ctx, ticket)
	if err != nil {
		return nil, err
	}
	if held, err = m.withUnrecorded(ctx, ticket, held); err != nil {
		return nil, err
	}
	if len(held) == 0 {
		return nil, fmt.Errorf("%w for ticket %s", ErrNoWorktrees, ticket)
	}
	results := make([]*worktree.CleanupResult, 0, len(held))
	for _, h := range held {
		res := m.cleanOne(ctx, ticket, h, force)
		if res.Removed {
			m.forgetWorktree(ctx, h, res.Worktree.Repo)
		}
		results = append(results, res)
	}
	return results, nil
}

func (m *Manager) cleanOne(ctx context.Context, ticket ident.TicketID, h *heldWorktree, force bool) *worktree.CleanupResult {
	switch released, err := m.releasedTo(ctx, h); {
	case force:
		err := m.opts.Worktrees.ForceCleanup(ctx, ticket, h.wt)
		return &worktree.CleanupResult{Worktree: h.wt, Removed: err == nil, Err: err}
	case err != nil:
		return &worktree.CleanupResult{Worktree: h.wt, Err: err}
	case released:
		return &worktree.CleanupResult{Worktree: h.wt, Reasons: []worktree.Reason{{Kind: Released,
			Detail: "attach --no-resume released it to you, so a plain clean leaves it alone"}}}
	}
	return m.opts.Worktrees.Cleanup(ctx, ticket, []*worktree.Worktree{h.wt})[0]
}

// releasedTo reports whether h was released to a person.
func (m *Manager) releasedTo(ctx context.Context, h *heldWorktree) (bool, error) {
	if h.agent == nil || h.agent.State != state.StateReleased {
		return false, nil
	}
	return m.opts.State.Agents.ReleasedForTicket(ctx, h.agent.Ticket)
}

// forgetWorktree drops a removed worktree from every agent recording it.
func (m *Manager) forgetWorktree(ctx context.Context, h *heldWorktree, repo string) {
	for _, id := range h.holders {
		if err := m.opts.State.Agents.RemoveWorktree(ctx, id, repo); err != nil {
			m.opts.Logger.Warn("agents: forgetting a cleaned worktree", "agent", id, "repo", repo, "err", err)
		}
	}
}

// withUnrecorded adds to held the worktrees of ticket's repos that are on
// disk but not recorded: a start that failed before its agent was recorded
// tells the user to clean what it couldn't remove.
func (m *Manager) withUnrecorded(ctx context.Context, ticket ident.TicketID, held []*heldWorktree) ([]*heldWorktree, error) {
	t, err := m.opts.Tickets.Get(ctx, ticket)
	if err != nil {
		if errors.Is(err, tickets.ErrNotFound) {
			return held, nil
		}
		return nil, err
	}
	have := map[string]bool{}
	for _, h := range held {
		have[h.wt.Repo] = true
	}
	for _, repo := range t.Repos {
		r, ok := m.opts.Config.Repo(repo)
		if !ok || have[repo] {
			continue
		}
		path := filepath.Join(m.opts.Config.WorkspaceDir, worktree.DirName(ticket, repo))
		if _, err := os.Lstat(path); errors.Is(err, fs.ErrNotExist) {
			continue
		} else if err != nil {
			return nil, err
		}
		held = append(held, &heldWorktree{wt: &worktree.Worktree{Ticket: ticket, Repo: repo, Path: path, RepoPath: r.Path}})
	}
	return held, nil
}

// heldWorktree is one worktree path and every agent recording it.
type heldWorktree struct {
	// agent is the newest of them; nil for a worktree nothing records.
	agent    *state.Agent
	recorded state.Worktree
	wt       *worktree.Worktree
	holders  []ident.AgentID
}

// recordedWorktrees are the worktrees state.db records, for ticket or, when
// it's "", every ticket. A worktree an ended agent kept, then the ticket's
// next agent reused, is recorded by both: it is one worktree, under the
// newer agent.
func (m *Manager) recordedWorktrees(ctx context.Context, ticket ident.TicketID) ([]*heldWorktree, error) {
	all, err := m.opts.State.Agents.WithWorktrees(ctx)
	if err != nil {
		return nil, err
	}
	var out []*heldWorktree
	byPath := map[string]*heldWorktree{}
	for _, a := range all {
		if ticket != "" && a.Ticket != ticket {
			continue
		}
		for i, wt := range m.worktreesOf(a) {
			h, seen := byPath[wt.Path]
			if !seen {
				h = &heldWorktree{}
				byPath[wt.Path] = h
				out = append(out, h)
			}
			// Oldest first, so the last seen is the newest.
			h.agent, h.recorded, h.wt = a, a.Worktrees[i], wt
			h.holders = append(h.holders, a.ID)
		}
	}
	return out, nil
}

// brokenHint is the way out of a broken worktree Create refused, which no
// `ticket set` can fix. A clean finds it from the ticket's repos whether or
// not an agent recorded it.
func brokenHint(ticket ident.TicketID, err error) string {
	if !errors.Is(err, worktree.ErrBroken) {
		return ""
	}
	// ForceCleanup won't delete a checkout whose .git file points elsewhere,
	// unseen: it may not be slop-bot's.
	return fmt.Sprintf(" (once you've looked, `slop-bot worktrees clean %s --force` discards it, "+
		"unless its .git file points nowhere: then remove it by hand)", ticket)
}
