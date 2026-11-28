// Package worktree manages the git worktrees an agent works in: one per repo
// a ticket touches, flat under the workspace dir as t<ticket>-<repo>, kept for
// the ticket's whole life and removed at its end without throwing work away.
//
// Callers serialise calls for one ticket; calls for different tickets may run
// at once, since they touch different paths and refs.
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
)

// remote is the remote whose HEAD names a repo's default branch.
const remote = "origin"

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
// names it, or from the main checkout's HEAD when there's no origin/HEAD.
// Nothing is fetched: the agent brings its branch up to date itself.
//
// It is idempotent: a worktree already there for this ticket and repo is
// reused as it stands, whatever branch it is on, since one ticket makes
// several PRs from the same worktree. On error, the worktrees made so far are
// returned with it, so the caller can record them.
func (m *Manager) Create(ctx context.Context, ticket ident.TicketID, repos []string, primary string) ([]*Worktree, error) {
	refs, err := m.resolve(ticket, repos, primary)
	if err != nil {
		return nil, err
	}
	out := make([]*Worktree, 0, len(refs))
	for _, r := range refs {
		wt, err := m.ensure(ctx, ticket, r)
		if err != nil {
			return out, fmt.Errorf("worktree: ticket %s, repo %s: %w", ticket, r.name, err)
		}
		wt.Primary = r.name == primary
		out = append(out, wt)
	}
	return out, nil
}

// resolve checks every name and repo before anything is made, so a config
// mistake doesn't leave half a ticket's worktrees behind.
func (m *Manager) resolve(ticket ident.TicketID, repos []string, primary string) ([]repoRef, error) {
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
		p, err := m.repoPath(name)
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

func (m *Manager) repoPath(name string) (string, error) {
	if err := validRepo(name); err != nil {
		return "", fmt.Errorf("worktree: %w", err)
	}
	r, ok := m.repos.Repo(name)
	if !ok {
		return "", fmt.Errorf("worktree: %w: %q", ErrUnknownRepo, name)
	}
	if err := checkRepoPath(r.Path); err != nil {
		return "", fmt.Errorf("worktree: repo %q: %w", name, err)
	}
	return r.Path, nil
}

func checkRepoPath(p string) error {
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
	return nil
}

func (m *Manager) path(ticket ident.TicketID, repo string) string {
	return filepath.Join(m.workspace, dirName(ticket, repo))
}

func (m *Manager) ensure(ctx context.Context, ticket ident.TicketID, r repoRef) (*Worktree, error) {
	wt := &Worktree{Ticket: ticket, Repo: r.name, RepoPath: r.path, Path: m.path(ticket, r.name)}
	reg, err := m.registration(ctx, r.path, wt.Path)
	if err != nil {
		return nil, err
	}
	if reg != nil && !reg.prunable {
		wt.Branch = reg.branch
		m.logger.Info("reusing worktree", "ticket", ticket, "repo", r.name, "path", wt.Path, "branch", wt.Branch)
		return wt, nil
	}
	if reg != nil {
		if err := m.prune(ctx, r.path, wt.Path); err != nil {
			return nil, err
		}
	}
	if _, err := os.Lstat(wt.Path); err == nil {
		return nil, fmt.Errorf("%w: %s exists but isn't a worktree of %s", ErrCollision, wt.Path, r.path)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}

	wt.Branch = BranchName(ticket, FirstBranch)
	exists, err := m.branchExists(ctx, r.path, wt.Branch)
	if err != nil {
		return nil, err
	}
	if exists {
		// Left by a worktree that was pruned: pick its work back up.
		_, err = m.git.Run(ctx, r.path, "worktree", "add", "--quiet", wt.Path, wt.Branch)
	} else {
		var base string
		if base, err = m.base(ctx, r.path); err != nil {
			return nil, err
		}
		// --no-track: an upstream of origin/main would make the agent's
		// plain `git push` aim at main.
		_, err = m.git.Run(ctx, r.path, "worktree", "add", "--quiet", "--no-track", "-b", wt.Branch, wt.Path, base)
	}
	if err != nil {
		return nil, err
	}
	m.logger.Info("created worktree", "ticket", ticket, "repo", r.name, "path", wt.Path, "branch", wt.Branch)
	return wt, nil
}

// NewBranch cuts a new branch, slop-bot/t<ticket>/<name>, in an existing
// worktree for the ticket's next PR. Like Create, it starts from the repo's
// default branch, not from the previous PR's branch, which stays as it is.
// Local changes carry over unless they conflict, in which case git refuses.
func (m *Manager) NewBranch(ctx context.Context, wt *Worktree, name string) (*Worktree, error) {
	if err := m.owns(wt.Ticket, wt); err != nil {
		return nil, err
	}
	if err := validLeaf(name); err != nil {
		return nil, fmt.Errorf("worktree: %w", err)
	}
	branch := BranchName(wt.Ticket, name)
	if _, err := m.git.Run(ctx, wt.Path, "check-ref-format", "--branch", branch); err != nil {
		return nil, fmt.Errorf("worktree: %w: branch name %q: %w", ErrInvalidName, branch, err)
	}
	base, err := m.base(ctx, wt.RepoPath)
	if err != nil {
		return nil, fmt.Errorf("worktree: %w", err)
	}
	if _, err := m.git.Run(ctx, wt.Path, "switch", "--quiet", "--no-track", "-c", branch, base); err != nil {
		return nil, fmt.Errorf("worktree: new branch in %s: %w", wt.Path, err)
	}
	m.logger.Info("new branch", "ticket", wt.Ticket, "repo", wt.Repo, "branch", branch)
	next := *wt
	next.Branch = branch
	return &next, nil
}

// owns checks wt is ticket's and sits exactly where Manager would have put it.
func (m *Manager) owns(ticket ident.TicketID, wt *Worktree) error {
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
	if err := checkRepoPath(wt.RepoPath); err != nil {
		return fmt.Errorf("worktree: repo %q: %w", wt.Repo, err)
	}
	return nil
}

// base is the revision a new branch starts from: origin's default branch, or
// the main checkout's HEAD when origin/HEAD isn't set.
func (m *Manager) base(ctx context.Context, repoPath string) (string, error) {
	out, err := m.git.Run(ctx, repoPath, "symbolic-ref", "-q", "refs/remotes/"+remote+"/HEAD")
	switch {
	case err == nil:
		return strings.TrimSpace(out), nil
	case gitx.IsExit(err, 1):
		return "HEAD", nil
	default:
		return "", err
	}
}

// defaultRefs are what "merged into the default branch" is measured against:
// origin's default branch and the local branch of the same name, or the main
// checkout's HEAD when origin/HEAD isn't set.
func (m *Manager) defaultRefs(ctx context.Context, repoPath string) ([]string, error) {
	base, err := m.base(ctx, repoPath)
	if err != nil || base == "HEAD" {
		return []string{base}, err
	}
	refs := []string{base}
	local := "refs/heads/" + strings.TrimPrefix(base, "refs/remotes/"+remote+"/")
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

// registration is how repo's worktree list has path, or nil if it isn't
// there.
type registration struct {
	head     string
	branch   string
	detached bool
	// prunable is git's word for a registration whose directory is gone.
	prunable bool
}

func (m *Manager) registration(ctx context.Context, repoPath, path string) (*registration, error) {
	out, err := m.git.Run(ctx, repoPath, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return nil, err
	}
	for _, rec := range strings.Split(out, "\x00\x00") {
		var (
			reg  registration
			here bool
		)
		for _, line := range strings.Split(rec, "\x00") {
			key, val, _ := strings.Cut(line, " ")
			switch key {
			case "worktree":
				here = canon(val) == path
			case "HEAD":
				reg.head = val
			case "branch":
				reg.branch = strings.TrimPrefix(val, "refs/heads/")
			case "detached":
				reg.detached = true
			case "prunable":
				reg.prunable = true
			}
		}
		if here {
			return &reg, nil
		}
	}
	return nil, nil
}

// prune drops stale registrations, which would otherwise block re-adding a
// worktree at the same path.
func (m *Manager) prune(ctx context.Context, repoPath, path string) error {
	m.logger.Info("pruning stale worktree registration", "repo", repoPath, "path", path)
	_, err := m.git.Run(ctx, repoPath, "worktree", "prune")
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
