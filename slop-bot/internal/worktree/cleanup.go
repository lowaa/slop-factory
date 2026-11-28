package worktree

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/lowaa/slop-factory/slop-bot/internal/gitx"
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
	// Nested is another worktree inside this one, typically under an
	// ignored path (an agent's own worktrees): git would delete it, files
	// and all, with this one, and `git status` doesn't show it.
	Nested ReasonKind = "nested"
	// InProgress is a rebase, merge, cherry-pick or revert stopped part
	// way; an autostash of uncommitted edits may be held with it.
	InProgress ReasonKind = "in_progress"
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
// saved to a ticket branch first, so the check above covers it; changes it
// had staged but not committed went with the directory and aren't recovered.
//
// Between the checks and the removal, git refuses to remove a worktree that
// became dirty, and each branch is deleted only if it still points where it
// did when checked. A commit made on a detached HEAD in that window isn't
// covered; nothing should be working in a finished ticket's worktree.
//
// A worktree with initialised submodules can't be removed by git, so Cleanup
// reports git's error for it every time; ForceCleanup removes it. Edits to
// files marked skip-worktree or assume-unchanged, and a separate repo under
// an ignored path, are invisible to git's status and are not kept for.
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
	if err := m.owns(ctx, ticket, wt); err != nil {
		return false, nil, err
	}
	unlock, err := m.lockRepo(ctx, wt.RepoPath)
	if err != nil {
		return false, nil, err
	}
	defer unlock()
	st, err := m.inspect(ctx, ticket, wt, true)
	if err != nil {
		return false, nil, err
	}
	reasons, err := m.atRisk(ctx, wt.RepoPath, st)
	if err != nil || len(reasons) > 0 {
		return false, reasons, err
	}
	switch {
	case st.reg != nil && st.present:
		if err := m.removeWorktree(ctx, wt.RepoPath, st.path, false); err != nil {
			return false, nil, m.afterFailedRemove(ctx, wt.RepoPath, st, err)
		}
	case st.reg != nil:
		if err := m.unregister(ctx, wt.RepoPath, st.path); err != nil {
			return false, nil, err
		}
	}
	m.dropResume(ticket, wt.Repo)
	return true, nil, m.deleteBranches(ctx, wt.RepoPath, st.branches)
}

// nested are the registered worktrees of repoPath inside path.
func (m *Manager) nested(ctx context.Context, repoPath, path string) ([]string, error) {
	regs, err := m.registrations(ctx, repoPath)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, r := range regs {
		if strings.HasPrefix(r.path, path+string(filepath.Separator)) {
			out = append(out, r.path)
		}
	}
	return out, nil
}

// atRisk is everything removing st would lose.
func (m *Manager) atRisk(ctx context.Context, repoPath string, st *state) ([]Reason, error) {
	var reasons []Reason
	if st.reg != nil && st.reg.locked {
		reasons = append(reasons, Reason{Kind: Locked, Detail: "locked: " + orNone(st.reg.lockReason)})
	}
	if st.reg != nil {
		dir, err := m.adminDir(ctx, repoPath, st.path)
		if err != nil {
			return nil, err
		}
		if dir != "" {
			ops, err := inProgress(dir)
			if err != nil {
				return nil, err
			}
			if len(ops) > 0 {
				reasons = append(reasons, Reason{Kind: InProgress,
					Detail: "an operation is in progress (" + strings.Join(ops, ", ") + "); finish or abort it"})
			}
		}
	}
	nested, err := m.nested(ctx, repoPath, st.path)
	if err != nil {
		return nil, err
	}
	for _, p := range nested {
		reasons = append(reasons, Reason{Kind: Nested, Detail: "holds the worktree at " + p})
	}
	if st.reg != nil && st.present {
		dirty, err := m.dirty(ctx, repoPath, st.path)
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
// worktree, and one holding another worktree, which may not be the ticket's
// to discard.
func (m *Manager) ForceCleanup(ctx context.Context, ticket ident.TicketID, wt *Worktree) error {
	if err := m.owns(ctx, ticket, wt); err != nil {
		return err
	}
	unlock, err := m.lockRepo(ctx, wt.RepoPath)
	if err != nil {
		return err
	}
	defer unlock()
	// No rescue: the ticket's branches are about to go whatever they hold,
	// and a rescue branch someone moved would otherwise block this for good.
	st, err := m.inspect(ctx, ticket, wt, false)
	if err != nil {
		return err
	}
	if nested, err := m.nested(ctx, wt.RepoPath, st.path); err != nil {
		return err
	} else if len(nested) > 0 {
		return fmt.Errorf("worktree: %w: %s holds the worktrees at %s; remove them first",
			ErrCollision, st.path, strings.Join(nested, ", "))
	}
	switch {
	case st.reg != nil && st.present:
		if err := m.forceRemove(ctx, wt.RepoPath, st.path); err != nil {
			return m.afterFailedRemove(ctx, wt.RepoPath, st, err)
		}
	case st.reg != nil:
		if err := m.unregister(ctx, wt.RepoPath, st.path); err != nil {
			return err
		}
	}
	if err := m.deleteBranches(ctx, wt.RepoPath, st.branches); err != nil {
		return err
	}
	m.dropResume(ticket, wt.Repo)
	m.logger.Info("worktree force-removed", "ticket", ticket, "path", st.path, "branches", len(st.branches))
	return nil
}

func (m *Manager) forceRemove(ctx context.Context, repoPath, path string) error {
	err := m.checkWorktree(ctx, repoPath, path)
	switch {
	case err == nil:
		return m.removeWorktree(ctx, repoPath, path, true)
	case errors.Is(err, ErrBroken):
		// git won't remove a worktree it can't validate. Delete it ourselves,
		// but only what is plainly the remains of ours: the path is ours
		// (owns), registered to this repo (inspect), a real directory
		// (checkWorktree), and has no .git at all, so it isn't some other
		// checkout put there since.
		if _, statErr := os.Lstat(filepath.Join(path, ".git")); !errors.Is(statErr, fs.ErrNotExist) {
			return fmt.Errorf("worktree: %w", err)
		}
		if err := os.RemoveAll(path); err != nil {
			return fmt.Errorf("worktree: removing %s: %w", path, err)
		}
		return m.unregister(ctx, repoPath, path)
	default:
		return fmt.Errorf("worktree: %w", err)
	}
}

// removeWorktree is `git worktree remove`; force twice also takes a locked
// one, for ForceCleanup.
func (m *Manager) removeWorktree(ctx context.Context, repoPath, path string, force bool) error {
	// git's own pre-removal check honours status.showUntrackedFiles; with it
	// set to "no" it would remove a worktree holding untracked files.
	args := []string{"-c", "status.showUntrackedFiles=normal", "worktree", "remove"}
	if force {
		args = append(args, "--force", "--force")
	}
	if _, err := m.git.Run(ctx, repoPath, append(args, path)...); err != nil {
		return fmt.Errorf("worktree: removing %s: %w", path, err)
	}
	return nil
}

// afterFailedRemove explains a failed removal. git drops the registration
// even when it couldn't delete every file, so a file it can't delete
// (read-only, or owned by root from a container build) leaves a directory
// that is no longer a worktree, and that every later call would take for
// someone else's. Say
// so now, and finish with the branches, which were checked safe to delete.
func (m *Manager) afterFailedRemove(ctx context.Context, repoPath string, st *state, removeErr error) error {
	reg, err := m.registration(ctx, repoPath, st.path)
	if err != nil || reg != nil {
		return errors.Join(removeErr, err)
	}
	if present, err := exists(st.path); err != nil || !present {
		return errors.Join(removeErr, err, m.deleteBranches(ctx, repoPath, st.branches))
	}
	return errors.Join(
		fmt.Errorf("%w: git unregistered %s but couldn't delete all of it; delete what's left by hand: %w",
			ErrLeftover, st.path, removeErr),
		m.deleteBranches(ctx, repoPath, st.branches))
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

// inspect finds wt's registration and the ticket's
// branches. It errors on a path that exists but isn't this repo's worktree,
// which is never touched. With rescue, a deleted worktree's detached HEAD is
// saved to a ticket branch, since its registration is all that holds those
// commits.
// The caller has checked wt with owns and holds the repo's lock.
func (m *Manager) inspect(ctx context.Context, ticket ident.TicketID, wt *Worktree, rescue bool) (*state, error) {
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
	if rescue && st.reg != nil && !st.present && st.reg.detached {
		if err := m.rescue(ctx, wt.RepoPath, ticket, st.reg.head); err != nil {
			return nil, err
		}
	}
	out, err := m.git.Run(ctx, wt.RepoPath, "for-each-ref",
		"--format=%(objectname) %(refname) %(symref)", ticketBranchPrefix(ticket))
	if err != nil {
		return nil, err
	}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		fields := strings.Fields(line)
		switch {
		case len(fields) == 3:
			// A symref under the ticket's prefix isn't a branch slop-bot cut,
			// and deleting through it would delete its target.
			m.logger.Warn("ignoring a symbolic ref among a ticket's branches", "ref", fields[1], "target", fields[2])
		case len(fields) == 2:
			st.branches = append(st.branches, branchRef{ref: fields[1], oid: fields[0]})
		}
	}
	return st, nil
}

func (m *Manager) dirty(ctx context.Context, repoPath, path string) ([]Reason, error) {
	out, err := m.inWorktree(ctx, repoPath, path, "status", "--porcelain=v1", "--untracked-files=all")
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
// saw it, and never one in use by some worktree, which would leave that
// worktree on a branch that doesn't exist.
func (m *Manager) deleteBranches(ctx context.Context, repoPath string, branches []branchRef) error {
	if len(branches) == 0 {
		return nil
	}
	checkedOut, err := m.branchesInUse(ctx, repoPath)
	if err != nil {
		return err
	}
	var errs []error
	for _, b := range branches {
		if at, ok := checkedOut[b.ref]; ok {
			errs = append(errs, fmt.Errorf("worktree: not deleting %s: it is checked out at %s", b.ref, at))
			continue
		}
		if _, err := m.git.Run(ctx, repoPath, "update-ref", "--no-deref", "-d", b.ref, b.oid); err != nil {
			errs = append(errs, fmt.Errorf("worktree: deleting %s: %w", b.ref, err))
			continue
		}
		// update-ref, unlike `branch -D`, leaves branch.<name>.* (the
		// upstream `push -u` set) behind, and a later branch of the same name
		// would inherit it.
		section := "branch." + strings.TrimPrefix(b.ref, "refs/heads/")
		if _, err := m.git.Run(ctx, repoPath, "config", "--local", "--remove-section", section); err != nil &&
			!errors.Is(err, gitx.ErrNoSuchSection) {
			errs = append(errs, fmt.Errorf("worktree: removing %s config: %w", section, err))
		}
	}
	return errors.Join(errs...)
}

// branchesInUse maps each branch a worktree of repoPath is on, or is in the
// middle of rebasing or bisecting from, to that worktree. Mid-rebase and
// mid-bisect, git lists the worktree as detached, but `rebase --continue` or
// `bisect reset` still needs the branch. The rebase and bisect state is read
// from each worktree's admin dir in the common git dir, which is there even
// when the checkout isn't (unmounted storage); anything unreadable is an
// error, not "not in use".
func (m *Manager) branchesInUse(ctx context.Context, repoPath string) (map[string]string, error) {
	regs, err := m.registrations(ctx, repoPath)
	if err != nil {
		return nil, err
	}
	inUse := map[string]string{}
	for _, r := range regs {
		if r.branch != "" {
			inUse["refs/heads/"+r.branch] = r.path
		}
	}
	common, err := m.commonDir(ctx, repoPath)
	if err != nil {
		return nil, err
	}
	gitDirs := []string{common}
	entries, err := os.ReadDir(filepath.Join(common, "worktrees"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	for _, e := range entries {
		if e.IsDir() {
			gitDirs = append(gitDirs, filepath.Join(common, "worktrees", e.Name()))
		}
	}
	for _, d := range gitDirs {
		refs, err := pendingBranches(d)
		if err != nil {
			return nil, err
		}
		for _, ref := range refs {
			inUse[ref] = "the rebase or bisect in " + d
		}
	}
	return inUse, nil
}

// pendingBranches are the branches a rebase or bisect in gitDir will return
// to.
func pendingBranches(gitDir string) ([]string, error) {
	var refs []string
	for _, f := range []string{"rebase-merge/head-name", "rebase-apply/head-name", "BISECT_START"} {
		raw, err := os.ReadFile(filepath.Join(gitDir, f)) // #nosec G304 -- git's own state files in a worktree's git dir
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		ref := strings.TrimSpace(string(raw))
		if !strings.HasPrefix(ref, "refs/") {
			// BISECT_START holds a short branch name, or a commit id when
			// the bisect started detached.
			ref = "refs/heads/" + ref
		}
		refs = append(refs, ref)
	}
	return refs, nil
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
