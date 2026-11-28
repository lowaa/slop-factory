package worktree

import (
	"context"
	"errors"
	"fmt"
	"os"
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
	// Locked is a worktree someone locked (`git worktree lock`), often
	// because it lives on storage that isn't always mounted.
	Locked ReasonKind = "locked"
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
//     (any remote-tracking ref) and not merged into the default branch;
//   - a locked worktree.
//
// A branch that is pushed but not merged is removed locally: the remote has
// its commits, so nothing is lost, and the PR lives on there. Remote-tracking
// refs are trusted as they stand, without a fetch; a stale one can only make a
// branch look pushed that was, at that point, really on the remote.
//
// A kept worktree keeps its registration and all its ticket's branches in that
// repo. A worktree whose directory was deleted has its detached HEAD, if any,
// saved to a ticket branch first, so the check above covers it.
//
// Between the checks and the removal, git refuses to remove a worktree that
// became dirty, and each branch is deleted only if it still points where it
// did when checked, so work that lands in between isn't lost either.
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
	reasons, err := m.atRisk(ctx, wt.RepoPath, st)
	if err != nil || len(reasons) > 0 {
		return false, reasons, err
	}
	switch {
	case st.reg != nil && st.present:
		if _, err := m.git.Run(ctx, wt.RepoPath, "worktree", "remove", st.path); err != nil {
			return false, nil, fmt.Errorf("worktree: removing %s: %w", st.path, err)
		}
	case st.reg != nil:
		if err := m.unregister(ctx, wt.RepoPath, st.path); err != nil {
			return false, nil, err
		}
	}
	return true, nil, m.deleteBranches(ctx, wt.RepoPath, st.branches)
}

// atRisk is everything removing st would lose.
func (m *Manager) atRisk(ctx context.Context, repoPath string, st *state) ([]Reason, error) {
	var reasons []Reason
	if st.reg != nil && st.reg.locked {
		reasons = append(reasons, Reason{Kind: Locked, Detail: "locked: " + orNone(st.reg.lockReason)})
	}
	if st.reg != nil && st.present {
		dirty, err := m.dirty(ctx, st.path)
		if err != nil {
			return nil, err
		}
		reasons = append(reasons, dirty...)
	}
	unpushed, err := m.unpushed(ctx, repoPath, st)
	if err != nil {
		return nil, err
	}
	return append(reasons, unpushed...), nil
}

// ForceCleanup removes ticket's worktree and its branches whatever they
// hold, for `slop-bot worktrees clean <t> --force` once the user has looked.
// That includes a locked one, one whose creation was interrupted, and one
// whose .git link is gone. It still refuses a path that isn't this repo's
// worktree.
func (m *Manager) ForceCleanup(ctx context.Context, ticket ident.TicketID, wt *Worktree) error {
	st, err := m.inspect(ctx, ticket, wt)
	if err != nil {
		return err
	}
	switch {
	case st.reg != nil && st.present:
		if err := m.forceRemove(ctx, wt.RepoPath, st.path); err != nil {
			return err
		}
	case st.reg != nil:
		if err := m.unregister(ctx, wt.RepoPath, st.path); err != nil {
			return err
		}
	}
	if err := m.deleteBranches(ctx, wt.RepoPath, st.branches); err != nil {
		return err
	}
	m.logger.Info("worktree force-removed", "ticket", ticket, "path", st.path, "branches", len(st.branches))
	return nil
}

func (m *Manager) forceRemove(ctx context.Context, repoPath, path string) error {
	err := m.checkWorktree(ctx, path)
	switch {
	case err == nil:
		// Twice, so a locked worktree goes too: the user asked for it gone.
		if _, err := m.git.Run(ctx, repoPath, "worktree", "remove", "--force", "--force", path); err != nil {
			return fmt.Errorf("worktree: removing %s: %w", path, err)
		}
		return nil
	case errors.Is(err, ErrBroken):
		// git won't remove a worktree it can't validate. The path is ours
		// (owns), registered to this repo (inspect), and a real directory
		// (checkWorktree got as far as asking git), so delete it ourselves.
		if err := os.RemoveAll(path); err != nil {
			return fmt.Errorf("worktree: removing %s: %w", path, err)
		}
		return m.unregister(ctx, repoPath, path)
	default:
		return fmt.Errorf("worktree: %w", err)
	}
}

// branchRef is a branch and the commit it pointed at when inspected.
type branchRef struct {
	ref string
	oid string
}

// state is what inspect found of one worktree.
type state struct {
	path    string
	reg     *registration
	present bool
	// branches are the ticket's branches in the repo.
	branches []branchRef
}

// inspect checks wt is ours and finds its registration and the ticket's
// branches. It errors on a path that exists but isn't this repo's worktree,
// which is never touched. A deleted worktree's detached HEAD is saved to a
// ticket branch, since its registration is all that holds those commits.
func (m *Manager) inspect(ctx context.Context, ticket ident.TicketID, wt *Worktree) (*state, error) {
	if err := m.owns(ctx, ticket, wt); err != nil {
		return nil, err
	}
	st := &state{path: m.path(ticket, wt.Repo)}
	var err error
	if st.reg, err = m.registration(ctx, wt.RepoPath, st.path); err != nil {
		return nil, err
	}
	if st.present, err = exists(st.path); err != nil {
		return nil, err
	}
	if st.reg == nil && st.present {
		return nil, fmt.Errorf("worktree: %w: %s isn't a worktree of %s", ErrCollision, st.path, wt.RepoPath)
	}
	if st.reg != nil && !st.present && st.reg.detached {
		if err := m.rescue(ctx, wt.RepoPath, ticket, st.reg.head); err != nil {
			return nil, err
		}
	}
	out, err := m.git.Run(ctx, wt.RepoPath, "for-each-ref", "--format=%(objectname) %(refname)", ticketBranchPrefix(ticket))
	if err != nil {
		return nil, err
	}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if oid, ref, ok := strings.Cut(line, " "); ok {
			st.branches = append(st.branches, branchRef{ref: ref, oid: oid})
		}
	}
	return st, nil
}

func (m *Manager) dirty(ctx context.Context, path string) ([]Reason, error) {
	out, err := m.inWorktree(ctx, path, "status", "--porcelain=v1", "--untracked-files=all")
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
// Tips are the commits inspect saw, so the check and the deletion agree.
func (m *Manager) unpushed(ctx context.Context, repoPath string, st *state) ([]Reason, error) {
	defaults, err := m.defaultRefs(ctx, repoPath)
	if err != nil {
		return nil, err
	}
	into := "no default branch known (origin/HEAD is unset)"
	if len(defaults) > 0 {
		into = "not merged into " + defaults[0]
	}
	type tip struct{ oid, what string }
	tips := make([]tip, 0, len(st.branches)+1)
	for _, b := range st.branches {
		tips = append(tips, tip{b.oid, "branch " + strings.TrimPrefix(b.ref, "refs/heads/")})
	}
	if st.reg != nil && st.present && st.reg.detached && st.reg.head != "" {
		tips = append(tips, tip{st.reg.head, "detached HEAD " + st.reg.head})
	}
	var reasons []Reason
	for _, t := range tips {
		n, err := m.unsaved(ctx, repoPath, t.oid, defaults...)
		if err != nil {
			return nil, err
		}
		if n > 0 {
			reasons = append(reasons, Reason{
				Kind:   Unpushed,
				Detail: fmt.Sprintf("%s has %s on no remote, %s", t.what, plural(n, "commit"), into),
			})
		}
	}
	return reasons, nil
}

// deleteBranches deletes each branch only if it still points where inspect
// saw it, and never one checked out in some worktree, which would leave that
// worktree on a branch that doesn't exist.
func (m *Manager) deleteBranches(ctx context.Context, repoPath string, branches []branchRef) error {
	if len(branches) == 0 {
		return nil
	}
	regs, err := m.registrations(ctx, repoPath)
	if err != nil {
		return err
	}
	checkedOut := map[string]string{}
	for _, r := range regs {
		if r.branch != "" {
			checkedOut["refs/heads/"+r.branch] = r.path
		}
	}
	var errs []error
	for _, b := range branches {
		if at, ok := checkedOut[b.ref]; ok {
			errs = append(errs, fmt.Errorf("worktree: not deleting %s: it is checked out at %s", b.ref, at))
			continue
		}
		if _, err := m.git.Run(ctx, repoPath, "update-ref", "-d", b.ref, b.oid); err != nil {
			errs = append(errs, fmt.Errorf("worktree: deleting %s: %w", b.ref, err))
		}
	}
	return errors.Join(errs...)
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return strconv.Itoa(n) + " " + noun + "s"
}

func orNone(s string) string {
	if s == "" {
		return "no reason given"
	}
	return s
}
