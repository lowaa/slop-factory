package providers

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-review/internal/claudecli"
	"github.com/lowaa/slop-factory/slop-review/internal/codexcli"
	"github.com/lowaa/slop-factory/slop-review/internal/config"
	"github.com/lowaa/slop-factory/slop-review/internal/llm"
	"github.com/lowaa/slop-factory/slop-review/internal/session"
)

// fakeCodex writes a codex stand-in whose `login status` exits loginExit.
func fakeCodex(t *testing.T, loginExit int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "codex")
	script := "#!/bin/sh\necho status\nexit " + strconv.Itoa(loginExit) + "\n"
	require.NoError(t, os.WriteFile(path, []byte(script), 0o755))
	return path
}

// fakeClaude writes a claude stand-in whose `auth status` prints out.
// noSandboxTools stands in for bubblewrap and socat, which CI's image
// doesn't have: a fake claude runs no sandbox.
var noSandboxTools = []string{}

func fakeClaude(t *testing.T, out string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "claude")
	// Anything but `auth status` fails, so a changed login check is caught.
	script := "#!/bin/sh\n[ \"$*\" = \"auth status\" ] || exit 9\nprintf '%s' '" + out + "'\n"
	require.NoError(t, os.WriteFile(path, []byte(script), 0o755))
	return path
}

func routeOf(t *testing.T, args ...string) *config.Route {
	t.Helper()
	cfg, err := config.Load(args)
	require.NoError(t, err)
	return &cfg.Route
}

func runOpts(repo string, runChecks bool) *RunOptions {
	return &RunOptions{RepoRoot: repo, RunChecks: runChecks}
}

func TestSessions_APIRoutesHaveNoSessionFactoryOfTheirOwn(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "test-key")
	sessions, err := (&Factory{}).Sessions(context.Background(), routeOf(t, "--provider=openai"), runOpts(".", false), slog.DiscardHandler)
	require.NoError(t, err)
	assert.Nil(t, sessions, "the orchestrator's built-in loop runs over New's provider")
}

func TestSessions_CodexBuildsTheCodexFactoryWhenLoggedIn(t *testing.T) {
	t.Setenv("CODEX_HOME", t.TempDir())
	route := routeOf(t, "--provider=codex", "--reasoning-effort=high")

	f := &Factory{CodexCommand: fakeCodex(t, 0), CodexHome: filepath.Join(t.TempDir(), "home")}
	sessions, err := f.Sessions(context.Background(), route, runOpts(".", false), slog.DiscardHandler)
	require.NoError(t, err)
	factory, ok := sessions.(*session.CodexCLIFactory)
	require.True(t, ok, "got %T", sessions)
	assert.Equal(t, "gpt-5.6-luna", factory.Model)
	assert.Equal(t, "high", factory.ReasoningEffort)
}

// With no CodexHome given, each run gets a home of its own that Close
// removes, so nothing Codex writes there outlives the run.
func TestSessions_CodexHomeIsFreshPerRunAndRemovedOnClose(t *testing.T) {
	route := routeOf(t, "--provider=codex")
	t.Setenv("CODEX_HOME", t.TempDir())
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)

	f := &Factory{CodexCommand: fakeCodex(t, 0)}
	_, err := f.Sessions(context.Background(), route, runOpts(".", false), slog.DiscardHandler)
	require.NoError(t, err)
	homes, err := filepath.Glob(filepath.Join(tmp, "slop-review-codex-home-*"))
	require.NoError(t, err)
	require.Len(t, homes, 1)

	require.NoError(t, f.Close())
	assert.NoDirExists(t, homes[0], "the home must not outlive the run")
}

// A missing login is an auth failure, so a configured fallback on auth can
// take it, and the message says how to fix it.
func TestSessions_CodexRefusesToStartWithoutALogin(t *testing.T) {
	route := routeOf(t, "--provider=codex")
	t.Setenv("CODEX_HOME", t.TempDir())

	f := &Factory{CodexCommand: fakeCodex(t, 1), CodexHome: filepath.Join(t.TempDir(), "home")}
	_, err := f.Sessions(context.Background(), route, runOpts(".", false), slog.DiscardHandler)
	require.Error(t, err)
	assert.True(t, errors.Is(err, codexcli.ErrNotLoggedIn))
	assert.True(t, errors.Is(err, llm.ErrAuth))
	assert.Contains(t, err.Error(), "codex login")
}

func TestSessions_ClaudeBuildsTheClaudeFactoryWhenLoggedIn(t *testing.T) {
	route := routeOf(t, "--provider=claude", "--reasoning-effort=max")

	f := &Factory{ClaudeCommand: fakeClaude(t, `{"loggedIn":true}`), ClaudeSandboxTools: noSandboxTools}
	sessions, err := f.Sessions(context.Background(), route, runOpts(".", false), slog.DiscardHandler)
	require.NoError(t, err)
	factory, ok := sessions.(*session.ClaudeCLIFactory)
	require.True(t, ok, "got %T", sessions)
	assert.Equal(t, "claude-sonnet-5-5", factory.Model)
	assert.Equal(t, "max", factory.ReasoningEffort)
}

func TestSessions_ClaudeRefusesToStartWithoutALogin(t *testing.T) {
	route := routeOf(t, "--provider=claude")
	for name, out := range map[string]string{
		"logged out": `{"loggedIn":false}`,
		"not json":   `Not logged in`,
	} {
		t.Run(name, func(t *testing.T) {
			f := &Factory{ClaudeCommand: fakeClaude(t, out), ClaudeSandboxTools: noSandboxTools}
			_, err := f.Sessions(context.Background(), route, runOpts(".", false), slog.DiscardHandler)
			require.Error(t, err)
			assert.True(t, errors.Is(err, claudecli.ErrNotLoggedIn))
			assert.True(t, errors.Is(err, llm.ErrAuth))
			assert.Contains(t, err.Error(), "claude auth login")
		})
	}
}

// A machine that can't run claude -- not installed, or without what its
// sandbox needs -- can't use the provider: an auth failure, so a centrally
// chosen fallback takes it, with the reason kept in the message.
func TestSessions_AClaudeThisMachineCantRunIsAnAuthFailure(t *testing.T) {
	route := routeOf(t, "--provider=claude")
	for name, tc := range map[string]struct {
		f    *Factory
		want error
	}{
		"not installed": {&Factory{ClaudeCommand: filepath.Join(t.TempDir(), "no-such-claude")}, claudecli.ErrUnavailable},
		"no sandbox":    {&Factory{ClaudeCommand: fakeClaude(t, `{"loggedIn":true}`), ClaudeSandboxTools: []string{"no-such-bwrap"}}, claudecli.ErrNoSandbox},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := tc.f.Sessions(context.Background(), route, runOpts(".", false), slog.DiscardHandler)
			require.Error(t, err)
			assert.ErrorIs(t, err, tc.want)
			assert.ErrorIs(t, err, llm.ErrAuth)
		})
	}
}

// repoWithGoCache returns a git repo and a GOCACHE outside it, with the
// environment pointed at them.
func repoWithGoCache(t *testing.T) (run *RunOptions, cache string) {
	t.Helper()
	repo, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, exec.Command("git", "-C", repo, "init", "-q").Run())
	cache, err = filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	t.Setenv("GOCACHE", cache)
	return runOpts(repo, true), cache
}

func TestWritableRoots(t *testing.T) {
	f := &Factory{}
	run, cache := repoWithGoCache(t)

	off := *run
	off.RunChecks = false
	assert.Empty(t, f.writableRoots(context.Background(), &off, slog.DiscardHandler), "no hole is punched without run-checks")

	assert.Equal(t, []string{cache}, f.writableRoots(context.Background(), run, slog.DiscardHandler))

	t.Setenv("GOCACHE", "off")
	assert.Empty(t, f.writableRoots(context.Background(), run, slog.DiscardHandler), "GOCACHE=off is not a directory")

	t.Setenv("GOCACHE", cache)
	notARepo := *run
	notARepo.RepoRoot = t.TempDir()
	assert.Empty(t, f.writableRoots(context.Background(), &notARepo, slog.DiscardHandler), "no toplevel fails closed")

	t.Setenv("GOCACHE", run.RepoRoot)
	assert.Empty(t, f.writableRoots(context.Background(), run, slog.DiscardHandler), "GOCACHE inside the repo is refused")

	t.Setenv("GOCACHE", cache)
	t.Setenv("PATH", t.TempDir())
	assert.Empty(t, f.writableRoots(context.Background(), run, slog.DiscardHandler), "a missing go must not fail the run")
}

func TestSafeWritableRoots(t *testing.T) {
	parent, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	repo := filepath.Join(parent, "repo")
	require.NoError(t, os.MkdirAll(filepath.Join(repo, "sub"), 0o755))
	unrelated, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	link := filepath.Join(unrelated, "link")
	require.NoError(t, os.Symlink(filepath.Join(repo, "sub"), link))
	linkToRepo := filepath.Join(unrelated, "linkrepo")
	require.NoError(t, os.Symlink(repo, linkToRepo))

	tests := []struct {
		name      string
		candidate string
		kept      bool
	}{
		{"filesystem root", "/", false},
		{"the repo", repo, false},
		{"parent of the repo", parent, false},
		{"inside the repo", filepath.Join(repo, "sub"), false},
		{"nonexistent inside the repo", filepath.Join(repo, "new", "cache"), false},
		{"symlink into the repo", link, false},
		{"symlink to the repo", linkToRepo, false},
		{"unrelated dir", unrelated, true},
		{"nonexistent dir under an unrelated parent", filepath.Join(unrelated, "missing", "cache"), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := safeWritableRoots([]string{tt.candidate}, repo, slog.New(slog.DiscardHandler))
			if tt.kept {
				assert.Len(t, got, 1)
			} else {
				assert.Empty(t, got)
			}
		})
	}

	assert.Empty(t, safeWritableRoots([]string{unrelated}, "", slog.New(slog.DiscardHandler)), "an unresolvable repo drops everything")
}

func TestSessions_CLIProvidersGetModelEffortAndSandboxFromRunOptions(t *testing.T) {
	t.Setenv("CODEX_HOME", t.TempDir())
	run, cache := repoWithGoCache(t)

	for _, provider := range []string{"codex", "claude"} {
		t.Run(provider, func(t *testing.T) {
			route := routeOf(t, "--provider="+provider, "--reasoning-effort=high")
			f := &Factory{
				CodexCommand: fakeCodex(t, 0), CodexHome: filepath.Join(t.TempDir(), "home"),
				ClaudeCommand:      fakeClaude(t, `{"loggedIn":true}`),
				ClaudeSandboxTools: noSandboxTools,
			}
			sessions, err := f.Sessions(context.Background(), route, run, slog.DiscardHandler)
			require.NoError(t, err)

			var model, effort string
			var runChecks bool
			var sandbox interface {
				Sandbox() (bool, []string)
				TempDir() string
			}
			switch s := sessions.(type) {
			case *session.CodexCLIFactory:
				model, effort, runChecks, sandbox = s.Model, s.ReasoningEffort, s.RunChecks, s.Exec.(*codexcli.Runner)
			case *session.ClaudeCLIFactory:
				model, effort, runChecks, sandbox = s.Model, s.ReasoningEffort, s.RunChecks, s.Exec.(*claudecli.Runner)
			default:
				t.Fatalf("unexpected factory %T", sessions)
			}
			assert.Equal(t, route.Model, model)
			assert.NotEmpty(t, model)
			assert.Equal(t, "high", effort)
			assert.True(t, runChecks)
			runnerChecks, roots := sandbox.Sandbox()
			assert.True(t, runnerChecks)
			assert.Equal(t, []string{cache}, roots)
			scratch := sandbox.TempDir()
			assert.DirExists(t, scratch)
			assert.Contains(t, scratch, "slop-review-checks-")
			assert.NotContains(t, roots, scratch)

			require.NoError(t, f.Close())
			assert.NoDirExists(t, scratch, "the scratch dir must not outlive the run")
		})
	}
}

func TestSessions_ClaudeRunChecksFalseGetsNoWritableRoots(t *testing.T) {
	run, _ := repoWithGoCache(t)
	run.RunChecks = false

	f := &Factory{ClaudeCommand: fakeClaude(t, `{"loggedIn":true}`), ClaudeSandboxTools: noSandboxTools}
	sessions, err := f.Sessions(context.Background(), routeOf(t, "--provider=claude"), run, slog.DiscardHandler)
	require.NoError(t, err)

	assert.False(t, sessions.(*session.ClaudeCLIFactory).RunChecks)
	runner := sessions.(*session.ClaudeCLIFactory).Exec.(*claudecli.Runner)
	runChecks, roots := runner.Sandbox()
	assert.False(t, runChecks)
	assert.Empty(t, roots)
	assert.Empty(t, runner.TempDir())
}

func TestCheckTempDir(t *testing.T) {
	run, _ := repoWithGoCache(t)
	f := &Factory{}
	t.Cleanup(func() { _ = f.Close() })

	dir := f.checkTempDir(context.Background(), run, slog.DiscardHandler)
	require.NotEmpty(t, dir)
	assert.DirExists(t, dir)

	off := *run
	off.RunChecks = false
	assert.Empty(t, f.checkTempDir(context.Background(), &off, slog.DiscardHandler))

	// A repository that contains the scratch dir is refused, not granted.
	tmp, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, exec.Command("git", "-C", tmp, "init", "-q").Run())
	t.Setenv("TMPDIR", tmp)
	inside := *run
	inside.RepoRoot = tmp
	assert.Empty(t, f.checkTempDir(context.Background(), &inside, slog.DiscardHandler))
	leftovers, err := filepath.Glob(filepath.Join(tmp, "slop-review-checks-*"))
	require.NoError(t, err)
	assert.Empty(t, leftovers, "nothing may be created in the repository before it is refused")
	require.NoError(t, f.Close())
	assert.NoDirExists(t, dir)
}
