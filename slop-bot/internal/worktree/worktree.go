// Package worktree manages the git worktrees an agent works in: one per repo
// a ticket touches, flat under the workspace dir as t<ticket>-<repo>, kept for
// the ticket's whole life and removed at its end without throwing work away.
//
// Callers serialise calls for one ticket. Calls for different tickets may run
// at once: Manager serialises its own git worktree operations per repo.
package worktree

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/lowaa/slop-factory/slop-bot/internal/config"
	"github.com/lowaa/slop-factory/slop-bot/internal/gitx"
	"github.com/lowaa/slop-factory/slop-bot/internal/ident"
)

var (
	// ErrInvalidName is a ticket id, repo or branch name that isn't safe in a
	// path or a ref.
	ErrInvalidName = errors.New("invalid name")
	// ErrUnknownRepo is a repo name the lookup doesn't have.
	ErrUnknownRepo = errors.New("repo not in the repo lookup")
	// ErrRepoMissing is a looked-up repo whose path isn't there.
	ErrRepoMissing = errors.New("repo path missing")
	// ErrCollision is a worktree path already taken by something that isn't
	// this repo's worktree.
	ErrCollision = errors.New("worktree path is taken by something else")
	// ErrNotOurs is a Worktree that doesn't belong to the ticket, or whose
	// path isn't where Manager puts that ticket's worktree: a guard against
	// removing an arbitrary directory on a bad record.
	ErrNotOurs = errors.New("worktree does not belong to this ticket")
	// ErrBroken is a worktree git doesn't see as a working one: its .git
	// link was deleted, its creation was interrupted, or it is locked while
	// its directory is missing. ForceCleanup clears it.
	ErrBroken = errors.New("worktree isn't a working git worktree")
	// ErrNoDefaultBranch is an origin/HEAD that points at a ref that's gone.
	ErrNoDefaultBranch = errors.New("default branch not found")
	// ErrUnsavedWork is an operation that would leave commits on no branch.
	ErrUnsavedWork = errors.New("commits would be left on no branch")
	// ErrLeftover is a removal git half-finished: the worktree is
	// unregistered but files it couldn't delete remain.
	ErrLeftover = errors.New("worktree directory left behind")
)

// Repos is the repo lookup; *config.Config satisfies it.
type Repos interface {
	Repo(name string) (*config.Repo, bool)
}

// Worktree is one ticket's checkout of one repo. state.db records these.
type Worktree struct {
	Ticket ident.TicketID
	Repo   string
	// RepoPath is the repo's main checkout, the one the worktree hangs off.
	RepoPath string
	Path     string
	// Branch is the branch checked out, as a short name; empty when detached.
	Branch string
	// Primary marks the harness's cwd.
	Primary bool
}

// Options configures a Manager.
type Options struct {
	WorkspaceDir string
	Repos        Repos
	Git          *gitx.Git
	Logger       *slog.Logger
}

// Manager creates, branches and cleans up ticket worktrees.
type Manager struct {
	workspace string
	repos     Repos
	git       *gitx.Git
	logger    *slog.Logger
	// locks holds a *sync.Mutex per repo, keyed on its common git dir.
	locks sync.Map
}

// NewManager makes the workspace dir if need be.
func NewManager(opts *Options) (*Manager, error) {
	if !filepath.IsAbs(opts.WorkspaceDir) {
		return nil, fmt.Errorf("worktree: workspace dir %q isn't absolute", opts.WorkspaceDir)
	}
	if opts.Repos == nil || opts.Git == nil || opts.Logger == nil {
		return nil, errors.New("worktree: Repos, Git and Logger are required")
	}
	if err := os.MkdirAll(opts.WorkspaceDir, 0o750); err != nil {
		return nil, fmt.Errorf("worktree: workspace dir: %w", err)
	}
	// git records worktree paths with symlinks resolved; resolving ours once
	// lets the two be compared as strings.
	ws, err := filepath.EvalSymlinks(opts.WorkspaceDir)
	if err != nil {
		return nil, fmt.Errorf("worktree: workspace dir: %w", err)
	}
	return &Manager{workspace: ws, repos: opts.Repos, git: opts.Git, logger: opts.Logger}, nil
}

type repoRef struct {
	name string
	path string
}

// Create gives ticket a worktree in each of repos, primary among them, at
// <workspace>/t<ticket>-<repo>. A new worktree is on a new branch,
// slop-bot/t<ticket>/work, cut from the repo's default branch as origin/HEAD
// names it, or from the commit the main checkout is on when there's no
// origin/HEAD. Nothing is fetched: the agent brings its branch up to date
// itself.
//
// It is idempotent: a worktree already there for this ticket and repo is
// reused as it stands, whatever branch it is on, since one ticket makes
// several PRs from the same worktree. One whose directory was deleted is
// re-added on the branch it was on (a detached HEAD's commits are first
// saved to a ticket branch). On error, the worktrees made so far are returned
// with it, so the caller can record them, including the one that failed if
// git got as far as making it (a post-checkout hook can fail after that).
func (m *Manager) Create(ctx context.Context, ticket ident.TicketID, repos []string, primary string) ([]*Worktree, error) {
	refs, err := m.resolve(ctx, ticket, repos, primary)
	if err != nil {
		return nil, err
	}
	out := make([]*Worktree, 0, len(refs))
	for _, r := range refs {
		wt, err := m.ensureLocked(ctx, ticket, r)
		if err != nil {
			if made := m.madeAnyway(ctx, ticket, r); made != nil {
				made.Primary = r.name == primary
				out = append(out, made)
			}
			return out, fmt.Errorf("worktree: ticket %s, repo %s: %w", ticket, r.name, err)
		}
		wt.Primary = r.name == primary
		out = append(out, wt)
	}
	return out, nil
}

// resolve checks every name and repo before anything is made, so a config
// mistake doesn't leave half a ticket's worktrees behind.
func (m *Manager) resolve(ctx context.Context, ticket ident.TicketID, repos []string, primary string) ([]repoRef, error) {
	if err := validTicket(ticket); err != nil {
		return nil, fmt.Errorf("worktree: %w", err)
	}
	if len(repos) == 0 {
		return nil, errors.New("worktree: no repos given")
	}
	seen := map[string]bool{}
	refs := make([]repoRef, 0, len(repos))
	for _, name := range repos {
		if seen[name] {
			return nil, fmt.Errorf("worktree: repo %q listed twice", name)
		}
		seen[name] = true
		p, err := m.repoPath(ctx, name)
		if err != nil {
			return nil, err
		}
		refs = append(refs, repoRef{name: name, path: p})
	}
	if !seen[primary] {
		return nil, fmt.Errorf("worktree: primary repo %q isn't among the ticket's repos", primary)
	}
	return refs, nil
}

func (m *Manager) repoPath(ctx context.Context, name string) (string, error) {
	if err := validRepo(name); err != nil {
		return "", fmt.Errorf("worktree: %w", err)
	}
	r, ok := m.repos.Repo(name)
	if !ok {
		return "", fmt.Errorf("worktree: %w: %q", ErrUnknownRepo, name)
	}
	if err := m.checkRepo(ctx, r.Path); err != nil {
		return "", fmt.Errorf("worktree: repo %q: %w", name, err)
	}
	return r.Path, nil
}

func (m *Manager) path(ticket ident.TicketID, repo string) string {
	return filepath.Join(m.workspace, dirName(ticket, repo))
}

// madeAnyway is the worktree a failed ensure left registered, if any.
func (m *Manager) madeAnyway(ctx context.Context, ticket ident.TicketID, r repoRef) *Worktree {
	path := m.path(ticket, r.name)
	reg, err := m.registration(ctx, r.path, path)
	if err != nil || reg == nil {
		return nil
	}
	return &Worktree{Ticket: ticket, Repo: r.name, RepoPath: r.path, Path: path, Branch: reg.branch}
}

func (m *Manager) ensureLocked(ctx context.Context, ticket ident.TicketID, r repoRef) (*Worktree, error) {
	unlock, err := m.lockRepo(ctx, r.path)
	if err != nil {
		return nil, err
	}
	defer unlock()
	return m.ensure(ctx, ticket, r)
}

func (m *Manager) ensure(ctx context.Context, ticket ident.TicketID, r repoRef) (*Worktree, error) {
	wt := &Worktree{Ticket: ticket, Repo: r.name, RepoPath: r.path, Path: m.path(ticket, r.name)}
	reg, err := m.registration(ctx, r.path, wt.Path)
	if err != nil {
		return nil, err
	}
	present, err := exists(wt.Path)
	if err != nil {
		return nil, err
	}
	switch {
	case reg != nil && reg.locked && reg.lockReason == lockInitializing:
		return nil, fmt.Errorf("%w: %s: an earlier `git worktree add` was interrupted; "+
			"remove it by hand: `git -C %s worktree unlock %s && git -C %s worktree remove --force --force %s`",
			ErrBroken, wt.Path, r.path, wt.Path, r.path, wt.Path)
	case reg != nil && present:
		if err := m.checkWorktree(ctx, r.path, wt.Path); err != nil {
			return nil, err
		}
		wt.Branch = reg.branch
		m.logger.Info("reusing worktree", "ticket", ticket, "repo", r.name, "path", wt.Path, "branch", wt.Branch)
		return wt, nil
	case reg != nil && reg.locked:
		// Locked and missing is git's way of saying "on storage that isn't
		// mounted": it may come back, so it isn't stale.
		return nil, fmt.Errorf("%w: %s is locked (%s) but missing; remount it, or drop its registration by hand: "+
			"`git -C %s worktree unlock %s && git -C %s worktree prune`",
			ErrBroken, wt.Path, reg.lockReason, r.path, wt.Path, r.path)
	case reg != nil:
		return m.recover(ctx, wt, reg)
	case present:
		return nil, fmt.Errorf("%w: %s exists but isn't a worktree of %s", ErrCollision, wt.Path, r.path)
	}
	return m.add(ctx, wt, BranchName(ticket, FirstBranch))
}

// recover re-adds a worktree whose directory was deleted, on the branch it
// was on, so the agent carries on with the PR it was working on.
func (m *Manager) recover(ctx context.Context, wt *Worktree, reg *registration) (*Worktree, error) {
	if reg.detached {
		if err := m.rescue(ctx, wt.RepoPath, wt.Ticket, reg.head); err != nil {
			return nil, err
		}
	}
	if err := m.unregister(ctx, wt.RepoPath, wt.Path); err != nil {
		return nil, err
	}
	if reg.detached {
		if _, err := m.git.Run(ctx, wt.RepoPath, "worktree", "add", "--quiet", "--detach", wt.Path, reg.head); err != nil {
			return nil, err
		}
		m.logger.Info("re-added worktree", "ticket", wt.Ticket, "repo", wt.Repo, "path", wt.Path, "head", reg.head)
		return wt, nil
	}
	branch := BranchName(wt.Ticket, FirstBranch)
	if reg.branch != "" {
		ok, err := m.branchExists(ctx, wt.RepoPath, reg.branch)
		if err != nil {
			return nil, err
		}
		if ok {
			branch = reg.branch
		}
	}
	return m.add(ctx, wt, branch)
}

// add makes the worktree on branch, cutting the branch from the default
// branch if it doesn't exist yet.
func (m *Manager) add(ctx context.Context, wt *Worktree, branch string) (*Worktree, error) {
	ok, err := m.branchExists(ctx, wt.RepoPath, branch)
	if err != nil {
		return nil, err
	}
	if ok {
		_, err = m.git.Run(ctx, wt.RepoPath, "worktree", "add", "--quiet", wt.Path, branch)
	} else {
		var base string
		if base, err = m.base(ctx, wt.RepoPath); err != nil {
			return nil, err
		}
		// --no-track: an upstream of origin/main would make the agent's
		// plain `git push` aim at main.
		_, err = m.git.Run(ctx, wt.RepoPath, "worktree", "add", "--quiet", "--no-track", "-b", branch, wt.Path, base)
	}
	if err != nil {
		return nil, err
	}
	wt.Branch = branch
	m.logger.Info("added worktree", "ticket", wt.Ticket, "repo", wt.Repo, "path", wt.Path, "branch", branch)
	return wt, nil
}

// NewBranch cuts a new branch, slop-bot/t<ticket>/<name>, in an existing
// worktree for the ticket's next PR. Like Create, it starts from the repo's
// default branch, not from the previous PR's branch, which stays as it is.
// Local changes carry over unless they conflict, in which case git refuses.
// It refuses to leave a detached HEAD's commits on no branch. If git switched
// but then failed (a post-checkout hook), the new Worktree comes back with
// the error, since the branch now exists.
func (m *Manager) NewBranch(ctx context.Context, wt *Worktree, name string) (*Worktree, error) {
	if err := m.owns(ctx, wt.Ticket, wt); err != nil {
		return nil, err
	}
	unlock, err := m.lockRepo(ctx, wt.RepoPath)
	if err != nil {
		return nil, err
	}
	defer unlock()
	if err := validLeaf(name); err != nil {
		return nil, fmt.Errorf("worktree: %w", err)
	}
	branch := BranchName(wt.Ticket, name)
	if _, err := m.git.Run(ctx, wt.RepoPath, "check-ref-format", "--branch", branch); err != nil {
		return nil, fmt.Errorf("worktree: %w: branch name %q: %w", ErrInvalidName, branch, err)
	}
	path := m.path(wt.Ticket, wt.Repo)
	if err := m.checkWorktree(ctx, wt.RepoPath, path); err != nil {
		return nil, fmt.Errorf("worktree: %w", err)
	}
	if err := m.refuseDetachedWork(ctx, path); err != nil {
		return nil, err
	}
	base, err := m.base(ctx, wt.RepoPath)
	if err != nil {
		return nil, fmt.Errorf("worktree: %w", err)
	}
	next := *wt
	next.Branch = branch
	if _, err := m.git.Run(ctx, path, "switch", "--quiet", "--no-track", "-c", branch, base); err != nil {
		err = fmt.Errorf("worktree: new branch in %s: %w", path, err)
		if cur, curErr := m.git.Run(ctx, path, "branch", "--show-current"); curErr == nil && strings.TrimSpace(cur) == branch {
			return &next, err
		}
		return nil, err
	}
	m.logger.Info("new branch", "ticket", wt.Ticket, "repo", wt.Repo, "branch", branch)
	return &next, nil
}

// refuseDetachedWork stops a switch away from a detached HEAD whose commits
// are on no branch or remote: git would only warn, and they'd be left
// reachable from the worktree's reflog alone.
func (m *Manager) refuseDetachedWork(ctx context.Context, path string) error {
	_, err := m.git.Run(ctx, path, "symbolic-ref", "-q", "HEAD")
	if err == nil {
		return nil
	}
	if !gitx.IsExit(err, 1) {
		return err
	}
	n, err := m.unsaved(ctx, path, "HEAD", "--branches")
	if err != nil {
		return err
	}
	if n > 0 {
		return fmt.Errorf("worktree: %w: %s has a detached HEAD with %s on no branch; put them on a branch first",
			ErrUnsavedWork, path, plural(n, "commit"))
	}
	return nil
}

// owns checks wt is ticket's, sits exactly where Manager would have put it,
// and hangs off a real repo.
func (m *Manager) owns(ctx context.Context, ticket ident.TicketID, wt *Worktree) error {
	if err := validTicket(ticket); err != nil {
		return fmt.Errorf("worktree: %w", err)
	}
	if err := validRepo(wt.Repo); err != nil {
		return fmt.Errorf("worktree: %w", err)
	}
	want := m.path(ticket, wt.Repo)
	if wt.Ticket != ticket || canon(wt.Path) != want {
		return fmt.Errorf("worktree: %w: %s (ticket %s) isn't %s", ErrNotOurs, wt.Path, wt.Ticket, want)
	}
	if err := m.checkRepo(ctx, wt.RepoPath); err != nil {
		return fmt.Errorf("worktree: repo %q: %w", wt.Repo, err)
	}
	return nil
}

// exists reports whether anything, a symlink included, is at p.
func exists(p string) (bool, error) {
	_, err := os.Lstat(p)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}
