package worktree

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/lowaa/slop-factory/slop-bot/internal/ident"
)

// ReasonKind is why Cleanup kept a worktree.
type ReasonKind string

const (
	// Uncommitted is a change to a tracked file, staged or not.
	Uncommitted ReasonKind = "uncommitted"
	// Untracked is a file git doesn't track and doesn't ignore. Ignored
	// files (build output, caches) are not kept for.
	Untracked ReasonKind = "untracked"
	// Unpushed is commits on no remote and not merged into the default
	// branch.
	Unpushed ReasonKind = "unpushed"
)

// Reason is one thing that would be lost if the worktree were removed.
type Reason struct {
	Kind ReasonKind
	// Detail says which branch or how many files, for the user.
	Detail string
}

// CleanupResult is what Cleanup did with one worktree, for the caller to tell
// the user.
type CleanupResult struct {
	Worktree *Worktree
	// Removed is the worktree gone. Its branches may not be, if Err says
	// deleting them failed.
	Removed bool
	// Reasons it was kept; empty when Removed, or when Err stopped it.
	Reasons []Reason
	// Err is a check or removal step that failed. Nothing is removed on a
	// check that couldn't be made.
	Err error
}

// Cleanup removes ticket's worktrees and their local slop-bot/t<ticket>/*
// branches at the end of the ticket, keeping any that would lose work:
//   - a worktree with uncommitted changes or untracked files;
//   - a branch, or a detached HEAD, with commits that are on no remote
//     (any remote-tracking ref) and not merged into the default branch.
//
// A branch that is pushed but not merged is removed locally: the remote has
// its commits, so nothing is lost, and the PR lives on there. Remote-tracking
// refs are trusted as they stand, without a fetch; a stale one can only make a
// branch look pushed that was, at that point, really on the remote.
//
// A kept worktree keeps all its ticket's branches in that repo too. Removal
// goes through `git worktree remove` without --force, so git refuses as well
// if the worktree changed since it was checked.
func (m *Manager) Cleanup(ctx context.Context, ticket ident.TicketID, worktrees []*Worktree) []*CleanupResult {
	out := make([]*CleanupResult, 0, len(worktrees))
	for _, wt := range worktrees {
		res := &CleanupResult{Worktree: wt}
		res.Removed, res.Reasons, res.Err = m.cleanupOne(ctx, ticket, wt)
		switch {
		case res.Err != nil:
			m.logger.Warn("worktree cleanup failed", "ticket", ticket, "path", wt.Path, "removed", res.Removed, "err", res.Err)
		case !res.Removed:
			m.logger.Info("worktree kept: it has work", "ticket", ticket, "path", wt.Path, "reasons", res.Reasons)
		default:
			m.logger.Info("worktree removed", "ticket", ticket, "path", wt.Path)
		}
		out = append(out, res)
	}
	return out
}

func (m *Manager) cleanupOne(ctx context.Context, ticket ident.TicketID, wt *Worktree) (bool, []Reason, error) {
	st, err := m.inspect(ctx, ticket, wt)
	if err != nil {
		return false, nil, err
	}
	var reasons []Reason
	if st.reg != nil && !st.gone {
		if reasons, err = m.dirty(ctx, st.path); err != nil {
			return false, nil, err
		}
	}
	unpushed, err := m.unpushed(ctx, wt.RepoPath, st)
	if err != nil {
		return false, nil, err
	}
	reasons = append(reasons, unpushed...)
	if len(reasons) > 0 {
		return false, reasons, nil
	}
	if !st.gone {
		if _, err := m.git.Run(ctx, wt.RepoPath, "worktree", "remove", st.path); err != nil {
			return false, nil, fmt.Errorf("worktree: removing %s: %w", st.path, err)
		}
	}
	return true, nil, m.deleteBranches(ctx, wt.RepoPath, st.branches)
}

// ForceCleanup removes ticket's worktree and its branches whatever they
// hold, for `slop-bot worktrees clean <t> --force` once the user has looked.
// It still refuses a path that isn't this repo's worktree.
func (m *Manager) ForceCleanup(ctx context.Context, ticket ident.TicketID, wt *Worktree) error {
	st, err := m.inspect(ctx, ticket, wt)
	if err != nil {
		return err
	}
	if !st.gone {
		// Twice, so a locked worktree goes too: the user asked for it gone.
		if _, err := m.git.Run(ctx, wt.RepoPath, "worktree", "remove", "--force", "--force", st.path); err != nil {
			return fmt.Errorf("worktree: removing %s: %w", st.path, err)
		}
	}
	if err := m.deleteBranches(ctx, wt.RepoPath, st.branches); err != nil {
		return err
	}
	m.logger.Info("worktree force-removed", "ticket", ticket, "path", st.path, "branches", st.branches)
	return nil
}

// state is what inspect found of one worktree.
type state struct {
	path string
	reg  *registration
	// gone is a worktree whose directory is no longer there; its stale
	// registration has been pruned.
	gone bool
	// branches are the ticket's branches in the repo, as full refs.
	branches []string
}

// inspect checks wt is ours, prunes it if its directory is gone, and finds
// the ticket's branches. It errors on a path that exists but isn't this
// repo's worktree, which is never touched.
func (m *Manager) inspect(ctx context.Context, ticket ident.TicketID, wt *Worktree) (*state, error) {
	if err := m.owns(ticket, wt); err != nil {
		return nil, err
	}
	st := &state{path: m.path(ticket, wt.Repo)}
	reg, err := m.registration(ctx, wt.RepoPath, st.path)
	if err != nil {
		return nil, err
	}
	st.reg = reg
	_, statErr := os.Lstat(st.path)
	switch {
	case errors.Is(statErr, fs.ErrNotExist):
		st.gone = true
		if reg != nil {
			if err := m.prune(ctx, wt.RepoPath, st.path); err != nil {
				return nil, err
			}
		}
	case statErr != nil:
		return nil, statErr
	case reg == nil:
		return nil, fmt.Errorf("worktree: %w: %s isn't a worktree of %s", ErrCollision, st.path, wt.RepoPath)
	}
	out, err := m.git.Run(ctx, wt.RepoPath, "for-each-ref", "--format=%(refname)", ticketBranchPrefix(ticket))
	if err != nil {
		return nil, err
	}
	st.branches = strings.Fields(out)
	return st, nil
}

func (m *Manager) dirty(ctx context.Context, path string) ([]Reason, error) {
	out, err := m.git.Run(ctx, path, "status", "--porcelain=v1", "--untracked-files=all")
	if err != nil {
		return nil, fmt.Errorf("worktree: status of %s: %w", path, err)
	}
	var changed, untracked int
	for _, line := range strings.Split(out, "\n") {
		switch {
		case line == "":
		case strings.HasPrefix(line, "??"):
			untracked++
		default:
			changed++
		}
	}
	var reasons []Reason
	if changed > 0 {
		reasons = append(reasons, Reason{Kind: Uncommitted, Detail: plural(changed, "file") + " with uncommitted changes"})
	}
	if untracked > 0 {
		reasons = append(reasons, Reason{Kind: Untracked, Detail: plural(untracked, "untracked file")})
	}
	return reasons, nil
}

// unpushed finds commits removing st would lose: on the ticket's branches,
// and on a detached HEAD, which nothing else holds once the worktree goes.
func (m *Manager) unpushed(ctx context.Context, repoPath string, st *state) ([]Reason, error) {
	defaults, err := m.defaultRefs(ctx, repoPath)
	if err != nil {
		return nil, err
	}
	tips := map[string]string{}
	for _, b := range st.branches {
		tips[b] = "branch " + strings.TrimPrefix(b, "refs/heads/")
	}
	if st.reg != nil && st.reg.detached && st.reg.head != "" {
		tips[st.reg.head] = "detached HEAD " + st.reg.head
	}
	var reasons []Reason
	for _, tip := range sortedKeys(tips) {
		args := append([]string{"rev-list", "--count", tip, "--not", "--remotes"}, defaults...)
		out, err := m.git.Run(ctx, repoPath, args...)
		if err != nil {
			return nil, err
		}
		n, err := strconv.Atoi(strings.TrimSpace(out))
		if err != nil {
			return nil, fmt.Errorf("worktree: rev-list count %q: %w", out, err)
		}
		if n > 0 {
			reasons = append(reasons, Reason{
				Kind:   Unpushed,
				Detail: fmt.Sprintf("%s has %s on no remote and not merged into %s", tips[tip], plural(n, "commit"), defaults[0]),
			})
		}
	}
	return reasons, nil
}

func (m *Manager) deleteBranches(ctx context.Context, repoPath string, refs []string) error {
	if len(refs) == 0 {
		return nil
	}
	names := make([]string, len(refs))
	for i, r := range refs {
		names[i] = strings.TrimPrefix(r, "refs/heads/")
	}
	// -D, not -d: -d measures against the upstream or HEAD, not "pushed or
	// merged", and the caller has already decided these are safe to go.
	if _, err := m.git.Run(ctx, repoPath, append([]string{"branch", "-D", "--"}, names...)...); err != nil {
		return fmt.Errorf("worktree: deleting branches: %w", err)
	}
	return nil
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return strconv.Itoa(n) + " " + noun + "s"
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
