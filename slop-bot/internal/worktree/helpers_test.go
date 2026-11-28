package worktree

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-bot/internal/config"
	"github.com/lowaa/slop-factory/slop-bot/internal/gitx"
	"github.com/lowaa/slop-factory/slop-bot/internal/gitx/gitxtest"
)

// lookup is a repo lookup from a map.
type lookup map[string]string

func (l lookup) Repo(name string) (*config.Repo, bool) {
	p, ok := l[name]
	if !ok {
		return nil, false
	}
	return &config.Repo{Name: name, Path: p}, true
}

// fixture is a workspace, a Manager, and real repos, each cloned from a bare
// "remote" so pushed and unpushed mean what they do in production.
type fixture struct {
	t     *testing.T
	git   *gitx.Git
	root  string
	ws    string
	repos lookup
	m     *Manager
}

func newFixture(t *testing.T, names ...string) *fixture {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	f := &fixture{t: t, git: gitxtest.New(t), root: root, ws: filepath.Join(root, "workspace"), repos: lookup{}}
	for _, n := range names {
		f.repos[n] = f.newRepo(n)
	}
	f.m, err = NewManager(&Options{WorkspaceDir: f.ws, Repos: f.repos, Git: f.git, Logger: slog.New(slog.DiscardHandler)})
	require.NoError(t, err)
	return f
}

// newRepo makes remotes/<name>.git and its clone repos/<name>, with one
// commit on main pushed and origin/HEAD set, as after a real clone.
func (f *fixture) newRepo(name string) string {
	bare := filepath.Join(f.root, "remotes", name+".git")
	repo := filepath.Join(f.root, "repos", name)
	f.run(f.root, "init", "-q", "--bare", bare)
	f.run(f.root, "init", "-q", repo)
	f.run(repo, "remote", "add", "origin", bare)
	f.commit(repo, "README", "init")
	f.run(repo, "push", "-q", "-u", "origin", "main")
	f.run(repo, "remote", "set-head", "origin", "--auto")
	return repo
}

func (f *fixture) run(dir string, args ...string) string {
	f.t.Helper()
	out, err := f.git.Run(context.Background(), dir, args...)
	require.NoError(f.t, err)
	return strings.TrimSpace(out)
}

func (f *fixture) write(dir, file, content string) {
	f.t.Helper()
	require.NoError(f.t, os.WriteFile(filepath.Join(dir, file), []byte(content), 0o600))
}

func (f *fixture) commit(dir, file, msg string) string {
	f.t.Helper()
	f.write(dir, file, msg+"\n")
	f.run(dir, "add", file)
	f.run(dir, "commit", "-q", "-m", msg)
	return f.run(dir, "rev-parse", "HEAD")
}

func (f *fixture) create(ticket string, repos []string, primary string) []*Worktree {
	f.t.Helper()
	wts, err := f.m.Create(context.Background(), ticketID(ticket), repos, primary)
	require.NoError(f.t, err)
	return wts
}

func (f *fixture) branchExists(repo, branch string) bool {
	f.t.Helper()
	ok, err := f.m.branchExists(context.Background(), f.repos[repo], branch)
	require.NoError(f.t, err)
	return ok
}

func onDisk(t *testing.T, p string) bool {
	t.Helper()
	_, err := os.Lstat(p)
	if os.IsNotExist(err) {
		return false
	}
	require.NoError(t, err)
	return true
}

func kinds(rs []Reason) []ReasonKind {
	out := make([]ReasonKind, len(rs))
	for i, r := range rs {
		out[i] = r.Kind
	}
	return out
}
