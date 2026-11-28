package worktree

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-bot/internal/gitx"
	"github.com/lowaa/slop-factory/slop-bot/internal/ident"
)

func ticketID(s string) ident.TicketID { return ident.TicketID(s) }

func TestCreateFlatLayoutAcrossRepos(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "slop-factory", "go-db")
	wts := f.create("42", []string{"slop-factory", "go-db"}, "go-db")
	require.Len(t, wts, 2)

	for i, repo := range []string{"slop-factory", "go-db"} {
		wt := wts[i]
		assert.Equal(t, filepath.Join(f.ws, "t42-"+repo), wt.Path)
		assert.Equal(t, repo, wt.Repo)
		assert.Equal(t, f.repos[repo], wt.RepoPath)
		assert.Equal(t, ticketID("42"), wt.Ticket)
		assert.Equal(t, "slop-bot/t42/work", wt.Branch)
		assert.Equal(t, repo == "go-db", wt.Primary)
		assert.Equal(t, "slop-bot/t42/work", f.run(wt.Path, "branch", "--show-current"))
		assert.Equal(t, f.run(f.repos[repo], "rev-parse", "origin/main"), f.run(wt.Path, "rev-parse", "HEAD"))
		// No upstream, so a bare `git push` can't land on main.
		_, err := f.git.Run(context.Background(), wt.Path, "rev-parse", "--abbrev-ref", "@{upstream}")
		assert.Error(t, err)
	}
	entries, err := os.ReadDir(f.ws)
	require.NoError(t, err)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	assert.ElementsMatch(t, []string{"t42-slop-factory", "t42-go-db"}, names)
}

// The base is origin's default branch, not whatever the user's own checkout
// happens to be on.
func TestCreateBasesOnOriginDefaultNotUserCheckout(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "a")
	repo := f.repos["a"]
	f.run(repo, "switch", "-q", "-c", "users-feature")
	f.commit(repo, "feature", "user's own work")

	wt := f.create("1", []string{"a"}, "a")[0]
	assert.Equal(t, f.run(repo, "rev-parse", "origin/main"), f.run(wt.Path, "rev-parse", "HEAD"))
}

func TestCreateWithoutOriginHEADBasesOnMainCheckoutHEAD(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	repo := filepath.Join(f.root, "repos", "local")
	f.run(f.root, "init", "-q", repo)
	f.commit(repo, "README", "init")
	f.run(repo, "switch", "-q", "-c", "dev")
	head := f.commit(repo, "dev", "dev work")
	f.repos["local"] = repo

	wt := f.create("1", []string{"local"}, "local")[0]
	assert.Equal(t, head, f.run(wt.Path, "rev-parse", "HEAD"))
}

func TestCreateIsIdempotentAndReusesAcrossPRs(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "a", "b")
	first := f.create("7", []string{"a", "b"}, "a")
	f.commit(first[0].Path, "pr1", "first PR")
	f.write(first[0].Path, "scratch", "in progress")

	again := f.create("7", []string{"a", "b"}, "a")
	assert.Equal(t, first, again)
	assert.FileExists(t, filepath.Join(first[0].Path, "pr1"))
	assert.FileExists(t, filepath.Join(first[0].Path, "scratch"), "reuse must not reset the worktree")

	// The next PR: a new branch in the same worktree. Create afterwards
	// reports the worktree as it stands, on that branch.
	next, err := f.m.NewBranch(context.Background(), first[0], "second")
	require.NoError(t, err)
	assert.Equal(t, "slop-bot/t7/second", next.Branch)
	assert.Equal(t, first[0].Path, next.Path)
	again = f.create("7", []string{"a", "b"}, "a")
	assert.Equal(t, "slop-bot/t7/second", again[0].Branch)
	assert.True(t, again[0].Primary)
}

func TestNewBranch(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "a")
	ctx := context.Background()
	wt := f.create("3", []string{"a"}, "a")[0]
	pr1 := f.commit(wt.Path, "pr1", "first PR")

	next, err := f.m.NewBranch(ctx, wt, "fix-tests")
	require.NoError(t, err)
	assert.Equal(t, "slop-bot/t3/fix-tests", f.run(wt.Path, "branch", "--show-current"))
	assert.Equal(t, f.run(wt.Path, "rev-parse", "origin/main"), f.run(wt.Path, "rev-parse", "HEAD"),
		"the next PR starts from the default branch")
	assert.Equal(t, pr1, f.run(wt.Path, "rev-parse", "slop-bot/t3/work"), "the previous PR's branch is untouched")
	assert.Equal(t, "work", wt.Branch[len("slop-bot/t3/"):], "the caller's Worktree isn't mutated")

	again, err := f.m.NewBranch(ctx, next, "fix-tests")
	require.ErrorIs(t, err, gitx.ErrBranchExists)
	assert.Nil(t, again, "git never switched, so no new Worktree")
	again, err = f.m.NewBranch(ctx, next, "work")
	require.ErrorIs(t, err, gitx.ErrBranchExists)
	assert.Nil(t, again)

	for _, bad := range []string{"", "-x", "a..b", "a/", "a.lock", "a b", "a~1", "x//y", strings.Repeat("x", 101)} {
		_, err = f.m.NewBranch(ctx, next, bad)
		require.ErrorIs(t, err, ErrInvalidName, "%q", bad)
	}

	other := *next
	other.Ticket = "4"
	_, err = f.m.NewBranch(ctx, &other, "x")
	require.ErrorIs(t, err, ErrNotOurs)
}

func TestCreateRejectsUnsafeNames(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "a")
	ctx := context.Background()
	for _, ticket := range []string{"", "1-2", "../1", "a/b", "1.2", " 1", "_1", strings.Repeat("1", 65)} {
		_, err := f.m.Create(ctx, ticketID(ticket), []string{"a"}, "a")
		require.ErrorIs(t, err, ErrInvalidName, "ticket %q", ticket)
	}
	for _, repo := range []string{"", "..", "a/b", "../a", "-a", ".a", "a..b", "a b"} {
		f.repos[repo] = f.repos["a"]
		_, err := f.m.Create(ctx, "1", []string{repo}, repo)
		require.ErrorIs(t, err, ErrInvalidName, "repo %q", repo)
	}
	assert.False(t, onDisk(t, filepath.Join(f.ws, "t1-a")))
}

func TestCreateRejectsBadRepoLists(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "a", "b")
	ctx := context.Background()

	_, err := f.m.Create(ctx, "1", nil, "a")
	require.ErrorContains(t, err, "no repos")
	_, err = f.m.Create(ctx, "1", []string{"a", "a"}, "a")
	require.ErrorContains(t, err, "listed twice")
	_, err = f.m.Create(ctx, "1", []string{"a"}, "b")
	require.ErrorContains(t, err, "primary")

	// Every repo is checked before any worktree is made.
	_, err = f.m.Create(ctx, "1", []string{"a", "nope"}, "a")
	require.ErrorIs(t, err, ErrUnknownRepo)
	assert.False(t, onDisk(t, filepath.Join(f.ws, "t1-a")))
}

func TestCreateMissingOrNonGitRepoPath(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	f.repos["gone"] = filepath.Join(f.root, "not-there")
	_, err := f.m.Create(ctx, "1", []string{"gone"}, "gone")
	require.ErrorIs(t, err, ErrRepoMissing)

	plain := filepath.Join(f.root, "plain")
	require.NoError(t, os.Mkdir(plain, 0o750))
	f.repos["plain"] = plain
	_, err = f.m.Create(ctx, "1", []string{"plain"}, "plain")
	require.ErrorIs(t, err, gitx.ErrNotARepo)

	// git finds the enclosing repo from a subdirectory, so only the
	// top-level check refuses it (doctor shares it, through CheckRepo).
	sub := filepath.Join(f.root, "mono")
	f.run(f.root, "init", "-q", sub)
	require.NoError(t, os.Mkdir(filepath.Join(sub, "app"), 0o750))
	f.repos["sub"] = filepath.Join(sub, "app")
	_, err = f.m.Create(ctx, "1", []string{"sub"}, "sub")
	require.ErrorIs(t, err, ErrNotTopLevel)
	assert.Contains(t, err.Error(), "is inside the repo at "+sub)
}

func TestCreateRefusesAPathThatIsNotOurs(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "a", "b")
	ctx := context.Background()

	// A directory someone else put there.
	require.NoError(t, os.Mkdir(filepath.Join(f.ws, "t1-a"), 0o750))
	_, err := f.m.Create(ctx, "1", []string{"a"}, "a")
	require.ErrorIs(t, err, ErrCollision)

	// Another repo's worktree at this repo's path, as after the lookup
	// changed what "b" points to.
	f.create("2", []string{"b"}, "b")
	f.repos["b"] = f.newRepo("b2")
	_, err = f.m.Create(ctx, "2", []string{"b"}, "b")
	require.ErrorIs(t, err, ErrCollision)

	// A symlink at the path to a real worktree of the repo must not pass
	// for it.
	elsewhere := filepath.Join(f.root, "elsewhere")
	f.run(f.repos["a"], "worktree", "add", "-q", elsewhere)
	require.NoError(t, os.Symlink(elsewhere, filepath.Join(f.ws, "t3-a")))
	_, err = f.m.Create(ctx, "3", []string{"a"}, "a")
	require.ErrorIs(t, err, ErrCollision)
}

// A worktree deleted by hand leaves a stale registration; Create drops it
// and re-adds the worktree on the branch that's still there, work and all.
func TestCreateRecoversFromAStaleRegistration(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "a")
	wt := f.create("5", []string{"a"}, "a")[0]
	head := f.commit(wt.Path, "work", "unpushed work")
	require.NoError(t, os.RemoveAll(wt.Path))

	again := f.create("5", []string{"a"}, "a")[0]
	assert.Equal(t, wt.Path, again.Path)
	assert.Equal(t, head, f.run(again.Path, "rev-parse", "HEAD"))
	assert.Equal(t, "slop-bot/t5/work", f.run(again.Path, "branch", "--show-current"))
}

func TestNewManagerValidates(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	_, err := NewManager(&Options{WorkspaceDir: "rel", Repos: f.repos, Git: f.git, Logger: f.m.logger})
	require.ErrorContains(t, err, "absolute")
	_, err = NewManager(&Options{WorkspaceDir: f.ws, Repos: f.repos, Logger: f.m.logger})
	require.ErrorContains(t, err, "required")
}
