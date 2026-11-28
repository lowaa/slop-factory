package agents

import (
	"context"
	"errors"
	"fmt"

	"github.com/lowaa/slop-factory/slop-bot/internal/ident"
	"github.com/lowaa/slop-factory/slop-bot/internal/state"
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

// CleanWorktrees cleans up ticket's recorded worktrees on the user's say-so,
// as Cleanup does at the ticket's end, or with force, ForceCleanup: whatever
// they hold. It holds the ticket's start place, so no agent starts into a
// worktree being removed, and refuses while the ticket's agent is live. It
// runs under that start's context, not the caller's: a CLI that hangs up
// mustn't leave a worktree removed but still recorded.
func (m *Manager) CleanWorktrees(_ context.Context, ticket ident.TicketID, force bool) ([]*worktree.CleanupResult, error) {
	ctx, done, err := m.claimStart(ticket, state.PoolNormal)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrWorktreesInUse, err)
	}
	defer done()
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
	if len(held) == 0 {
		return nil, fmt.Errorf("%w for ticket %s", ErrNoWorktrees, ticket)
	}
	wts := make([]*worktree.Worktree, 0, len(held))
	for _, h := range held {
		wts = append(wts, h.wt)
	}
	var results []*worktree.CleanupResult
	if force {
		for _, wt := range wts {
			err := m.opts.Worktrees.ForceCleanup(ctx, ticket, wt)
			results = append(results, &worktree.CleanupResult{Worktree: wt, Removed: err == nil, Err: err})
		}
	} else {
		results = m.opts.Worktrees.Cleanup(ctx, ticket, wts)
	}
	for i, res := range results {
		if !res.Removed {
			continue
		}
		for _, id := range held[i].holders {
			if err := m.opts.State.Agents.RemoveWorktree(ctx, id, res.Worktree.Repo); err != nil {
				m.opts.Logger.Warn("agents: forgetting a cleaned worktree", "agent", id, "repo", res.Worktree.Repo, "err", err)
			}
		}
	}
	return results, nil
}

// heldWorktree is one worktree path and every agent recording it.
type heldWorktree struct {
	// agent is the newest of them.
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
// `ticket set` can fix: the earlier agent that kept it recorded it.
func brokenHint(ticket ident.TicketID, err error) string {
	if !errors.Is(err, worktree.ErrBroken) {
		return ""
	}
	return fmt.Sprintf(" (once you've looked, `slop-bot worktrees clean %s --force` discards it)", ticket)
}
