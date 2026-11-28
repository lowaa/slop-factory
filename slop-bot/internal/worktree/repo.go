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

// remote is the remote whose HEAD names a repo's default branch.
const remote = "origin"

// lockInitializing is the lock reason git leaves on a worktree whose
// `worktree add` never finished.
const lockInitializing = "initializing"

// checkRepo checks p is a git repo's own top level (or, bare, its git dir).
// A plain directory inside some other repo would otherwise have git discover
// and act on that enclosing repo.
func (m *Manager) checkRepo(ctx context.Context, p string) error {
	info, err := os.Stat(p)
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%w: %s", ErrRepoMissing, p)
	}
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%w: %s isn't a directory", ErrRepoMissing, p)
	}
	bare, err := m.git.Run(ctx, p, "rev-parse", "--is-bare-repository")
	if err != nil {
		return err
	}
	query := "--show-toplevel"
	if strings.TrimSpace(bare) == "true" {
		query = "--absolute-git-dir"
	}
	top, err := m.git.Run(ctx, p, "rev-parse", query)
	if err != nil {
		return err
	}
	want, err := filepath.EvalSymlinks(p)
	if err != nil {
		return err
	}
	if got := filepath.Clean(strings.TrimSpace(top)); got != want {
		return fmt.Errorf("%w: %s is inside the repo at %s, not a repo of its own", gitx.ErrNotARepo, p, got)
	}
	return nil
}

// checkWorktree checks path is a real directory (not a symlink) that git sees
// as a worktree's top level, so a command run there can't act on the repo
// enclosing the workspace, or on wherever a symlink points.
func (m *Manager) checkWorktree(ctx context.Context, path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%w: %s isn't a directory", ErrCollision, path)
	}
	top, err := m.git.Run(ctx, path, "rev-parse", "--show-toplevel")
	if err != nil {
		return fmt.Errorf("%w: %s: %w", ErrBroken, path, err)
	}
	if got := canon(strings.TrimSpace(top)); got != canon(path) {
		return fmt.Errorf("%w: %s (git found the repo at %s)", ErrBroken, path, got)
	}
	return nil
}

// inWorktree runs git in the worktree at path, once checkWorktree passes.
func (m *Manager) inWorktree(ctx context.Context, path string, args ...string) (string, error) {
	if err := m.checkWorktree(ctx, path); err != nil {
		return "", err
	}
	return m.git.Run(ctx, path, args...)
}

// base is the commit-ish a new branch starts from: origin's default branch,
// or, when origin/HEAD isn't set, the commit the main checkout is on. The
// latter is resolved to a commit id here, because "HEAD" means something
// else once it's used inside a worktree.
func (m *Manager) base(ctx context.Context, repoPath string) (string, error) {
	ref, ok, err := m.originHEAD(ctx, repoPath)
	if err != nil || ok {
		return ref, err
	}
	out, err := m.git.Run(ctx, repoPath, "rev-parse", "--verify", "--quiet", "HEAD^{commit}")
	if err != nil {
		return "", fmt.Errorf("worktree: %s has no commit to start from: %w", repoPath, err)
	}
	return strings.TrimSpace(out), nil
}

// originHEAD is the ref origin/HEAD points to, if it's set.
func (m *Manager) originHEAD(ctx context.Context, repoPath string) (string, bool, error) {
	out, err := m.git.Run(ctx, repoPath, "symbolic-ref", "-q", "refs/remotes/"+remote+"/HEAD")
	if gitx.IsExit(err, 1) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	ref := strings.TrimSpace(out)
	// A default-branch rename and a `fetch --prune` leave origin/HEAD
	// pointing at nothing.
	ok, err := m.refExists(ctx, repoPath, ref)
	if err != nil {
		return "", false, err
	}
	if !ok {
		return "", false, fmt.Errorf("worktree: %w: %s/HEAD in %s points at %s, which doesn't exist; "+
			"run `git remote set-head %s --auto` there", ErrNoDefaultBranch, remote, repoPath, ref, remote)
	}
	return ref, true, nil
}

// defaultRefs are what "merged into the default branch" is measured against:
// origin's default branch and the local branch of that name. Without
// origin/HEAD, it's the main checkout's branch, but only a named branch that
// isn't slop-bot's: being reachable from a detached HEAD, or from a ticket
// branch the user has checked out, isn't being merged. It may be empty, and
// then only a remote counts.
func (m *Manager) defaultRefs(ctx context.Context, repoPath string) ([]string, error) {
	ref, ok, err := m.originHEAD(ctx, repoPath)
	if err != nil {
		return nil, err
	}
	if !ok {
		out, err := m.git.Run(ctx, repoPath, "symbolic-ref", "-q", "HEAD")
		if gitx.IsExit(err, 1) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		head := strings.TrimSpace(out)
		if strings.HasPrefix(head, "refs/heads/"+BranchPrefix) {
			return nil, nil
		}
		return []string{head}, nil
	}
	refs := []string{ref}
	local := "refs/heads/" + strings.TrimPrefix(ref, "refs/remotes/"+remote+"/")
	if ok, err := m.refExists(ctx, repoPath, local); err != nil {
		return nil, err
	} else if ok {
		refs = append(refs, local)
	}
	return refs, nil
}

func (m *Manager) branchExists(ctx context.Context, repoPath, branch string) (bool, error) {
	return m.refExists(ctx, repoPath, "refs/heads/"+branch)
}

func (m *Manager) refExists(ctx context.Context, repoPath, ref string) (bool, error) {
	_, err := m.git.Run(ctx, repoPath, "show-ref", "--verify", "--quiet", ref)
	switch {
	case err == nil:
		return true, nil
	case gitx.IsExit(err, 1):
		return false, nil
	default:
		return false, err
	}
}

// unsaved counts the commits reachable from tip that are on no remote and
// not reachable from any of keep.
func (m *Manager) unsaved(ctx context.Context, dir, tip string, keep ...string) (int, error) {
	args := append([]string{"rev-list", "--count", tip, "--not", "--remotes"}, keep...)
	out, err := m.git.Run(ctx, dir, args...)
	if err != nil {
		return 0, err
	}
	n, err := strconv.Atoi(strings.TrimSpace(out))
	if err != nil {
		return 0, fmt.Errorf("worktree: rev-list count %q: %w", out, err)
	}
	return n, nil
}

// rescue puts a detached HEAD's commits on a ticket branch, so they survive
// the worktree registration that alone holds them. Cleanup then treats them
// like any other ticket branch.
func (m *Manager) rescue(ctx context.Context, repoPath string, ticket ident.TicketID, head string) error {
	n, err := m.unsaved(ctx, repoPath, head, "--branches")
	if err != nil || n == 0 {
		return err
	}
	branch := BranchName(ticket, "detached-"+head[:min(12, len(head))])
	if ok, err := m.branchExists(ctx, repoPath, branch); err != nil || ok {
		return err
	}
	m.logger.Warn("saving a detached HEAD's commits to a branch", "repo", repoPath, "branch", branch, "head", head)
	_, err = m.git.Run(ctx, repoPath, "branch", "--", branch, head)
	return err
}

// registration is one entry of a repo's worktree list.
type registration struct {
	path       string
	head       string
	branch     string
	detached   bool
	locked     bool
	lockReason string
}

func (m *Manager) registrations(ctx context.Context, repoPath string) ([]*registration, error) {
	out, err := m.git.Run(ctx, repoPath, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return nil, err
	}
	var regs []*registration
	for _, rec := range strings.Split(out, "\x00\x00") {
		reg := &registration{}
		for _, line := range strings.Split(rec, "\x00") {
			key, val, _ := strings.Cut(line, " ")
			switch key {
			case "worktree":
				reg.path = canon(val)
			case "HEAD":
				reg.head = val
			case "branch":
				reg.branch = strings.TrimPrefix(val, "refs/heads/")
			case "detached":
				reg.detached = true
			case "locked":
				reg.locked, reg.lockReason = true, val
			}
		}
		if reg.path != "" {
			regs = append(regs, reg)
		}
	}
	return regs, nil
}

// registration is how repo's worktree list has path, or nil if it isn't
// there.
func (m *Manager) registration(ctx context.Context, repoPath, path string) (*registration, error) {
	regs, err := m.registrations(ctx, repoPath)
	if err != nil {
		return nil, err
	}
	for _, r := range regs {
		if r.path == path {
			return r, nil
		}
	}
	return nil, nil
}

// unregister drops the registration of a worktree whose directory is gone.
// It touches only that one: `git worktree prune` would also drop every other
// stale registration in the repo, the user's own included, and with them the
// HEAD and index that are all that's left of a worktree on unmounted storage.
// Forced, because git won't otherwise drop a locked one.
func (m *Manager) unregister(ctx context.Context, repoPath, path string) error {
	m.logger.Info("dropping stale worktree registration", "repo", repoPath, "path", path)
	_, err := m.git.Run(ctx, repoPath, "worktree", "remove", "--force", "--force", path)
	return err
}

// canon resolves symlinks in p's parent, as git does when it records a
// worktree, but not in p itself: a symlink at t<ticket>-<repo> must not pass
// for whatever it points at.
func canon(p string) string {
	p = filepath.Clean(p)
	if dir, err := filepath.EvalSymlinks(filepath.Dir(p)); err == nil {
		return filepath.Join(dir, filepath.Base(p))
	}
	return p
}
