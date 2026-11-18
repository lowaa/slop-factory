package claudecli

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const fakeClaudeScript = `#!/bin/sh
if [ "$1" = "auth" ]; then
  printf '%s' "$FAKE_CLAUDE_AUTH_OUT"
  exit "${FAKE_CLAUDE_AUTH_EXIT:-0}"
fi
: > "$FAKE_CLAUDE_DIR/argv"
for a in "$@"; do
  printf '%s\n' "$a" >> "$FAKE_CLAUDE_DIR/argv"
done
pwd > "$FAKE_CLAUDE_DIR/pwd"
env > "$FAKE_CLAUDE_DIR/env"
cat > "$FAKE_CLAUDE_DIR/stdin"
case "$FAKE_CLAUDE_MODE" in
  fail) echo "boom: something broke" >&2; exit 3 ;;
  badjson) printf 'not json at all'; exit 0 ;;
  iserror) printf '{"is_error":true,"subtype":"error_max_turns","result":"ran out of turns","structured_output":{"ok":true}}'; exit 0 ;;
  nostructured) printf '{"is_error":false,"subtype":"success","result":"plain text"}'; exit 0 ;;
  nullstructured) printf '{"is_error":false,"subtype":"success","result":"x","structured_output":null}'; exit 0 ;;
  nosandbox) echo "Error: sandbox required but unavailable: bubblewrap not found" >&2; exit 1 ;;
  leaklogin) printf '{"is_error":false,"subtype":"success","result":"","structured_output":{"summary":"env says %s"}}' "$CLAUDE_CODE_OAUTH_TOKEN"; exit 0 ;;
  block) exec sleep 1000 ;;
  orphan) sleep 1000 & echo $! > "$FAKE_CLAUDE_DIR/child"; wait ;;
esac
printf '{"is_error":false,"subtype":"success","result":"","structured_output":{"ok":true},"modelUsage":{"claude-b":{},"claude-a":{}}}'
`

type fakeClaude struct {
	command string
	dir     string
	tmp     string
}

func newFakeClaude(t *testing.T, mode string) *fakeClaude {
	t.Helper()
	command := filepath.Join(t.TempDir(), "claude")
	require.NoError(t, os.WriteFile(command, []byte(fakeClaudeScript), 0o755))
	dir := t.TempDir()
	// Failed runs keep their scratch dir on purpose; keep it inside the test's own temp.
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	t.Setenv("FAKE_CLAUDE_DIR", dir)
	t.Setenv("FAKE_CLAUDE_MODE", mode)
	return &fakeClaude{command: command, dir: dir, tmp: tmp}
}

func (f *fakeClaude) read(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(f.dir, name))
	require.NoError(t, err)
	return string(b)
}

func (f *fakeClaude) argv(t *testing.T) []string {
	return strings.Split(strings.TrimSuffix(f.read(t, "argv"), "\n"), "\n")
}

func (f *fakeClaude) scratchDirs(t *testing.T) []string {
	t.Helper()
	dirs, err := filepath.Glob(filepath.Join(f.tmp, "slop-review-claude-*"))
	require.NoError(t, err)
	return dirs
}

func newRunner(f *fakeClaude) *Runner {
	return New(&Options{Command: f.command}, slog.DiscardHandler)
}

func containsPair(argv []string, flag, value string) bool {
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == flag && argv[i+1] == value {
			return true
		}
	}
	return false
}

func valueAfter(t *testing.T, argv []string, flag string) string {
	t.Helper()
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == flag {
			return argv[i+1]
		}
	}
	t.Fatalf("flag %s not in %v", flag, argv)
	return ""
}

func execRequest(repo string) *ExecRequest {
	return &ExecRequest{RepoRoot: repo, Prompt: "p", Schema: []byte(`{}`)}
}

func TestExec_HappyPath(t *testing.T) {
	f := newFakeClaude(t, "ok")
	repo := t.TempDir()
	schema := `{"type":"object"}`

	res, err := newRunner(f).Exec(context.Background(), &ExecRequest{
		RepoRoot: repo, Prompt: "review this", Schema: []byte(schema), Model: "claude-x", ReasoningEffort: "high",
	})
	require.NoError(t, err)

	assert.JSONEq(t, `{"ok":true}`, string(res.JSON))
	assert.Equal(t, []string{"claude-a", "claude-b"}, res.Models)

	argv := f.argv(t)
	for _, flag := range []string{"-p", "--dangerously-skip-permissions", "--strict-mcp-config", "--disable-slash-commands", "--no-session-persistence"} {
		assert.Contains(t, argv, flag)
	}
	assert.True(t, containsPair(argv, "--output-format", "json"))
	assert.True(t, containsPair(argv, "--json-schema", schema))
	assert.True(t, containsPair(argv, "--tools", "Read,Grep,Glob,Bash"))
	assert.True(t, containsPair(argv, "--add-dir", repo))
	assert.True(t, containsPair(argv, "--setting-sources", ""), "an empty --setting-sources keeps user and project settings out")
	assert.True(t, containsPair(argv, "--model", "claude-x"))
	assert.True(t, containsPair(argv, "--effort", "high"))

	// The sandbox only governs the shell, so the file tools that could
	// write must not be offered.
	toolList := valueAfter(t, argv, "--tools")
	for _, banned := range []string{"Edit", "Write", "NotebookEdit"} {
		assert.NotContains(t, strings.Split(toolList, ","), banned)
	}

	assert.Equal(t, "review this", f.read(t, "stdin"))
	assert.NotContains(t, argv, "review this", "the prompt can exceed the argument length limit, so it is not an argument")
}

func TestExec_SettingsHoldTheRunReadOnlyAndOffline(t *testing.T) {
	f := newFakeClaude(t, "ok")
	repo := t.TempDir()

	_, err := newRunner(f).Exec(context.Background(), execRequest(repo))
	require.NoError(t, err)

	var settings struct {
		AutoMemoryEnabled bool `json:"autoMemoryEnabled"`
		Sandbox           struct {
			Enabled                  bool `json:"enabled"`
			FailIfUnavailable        bool `json:"failIfUnavailable"`
			AllowUnsandboxedCommands bool `json:"allowUnsandboxedCommands"`
			Filesystem               struct {
				DenyWrite []string `json:"denyWrite"`
			} `json:"filesystem"`
			Network struct {
				AllowedDomains []string `json:"allowedDomains"`
				DeniedDomains  []string `json:"deniedDomains"`
			} `json:"network"`
		} `json:"sandbox"`
	}
	require.NoError(t, json.Unmarshal([]byte(valueAfter(t, f.argv(t), "--settings")), &settings))

	assert.False(t, settings.AutoMemoryEnabled)
	assert.True(t, settings.Sandbox.Enabled)
	assert.True(t, settings.Sandbox.FailIfUnavailable, "a missing sandbox must fail the run, not silently drop it")
	assert.False(t, settings.Sandbox.AllowUnsandboxedCommands)
	assert.Contains(t, settings.Sandbox.Filesystem.DenyWrite, repo)
	assert.Equal(t, []string{"*"}, settings.Sandbox.Network.DeniedDomains)
	assert.Empty(t, settings.Sandbox.Network.AllowedDomains)
}

func TestExec_RunsOutsideTheRepoAndRemovesItsScratchDirOnSuccess(t *testing.T) {
	f := newFakeClaude(t, "ok")
	repo := t.TempDir()

	_, err := newRunner(f).Exec(context.Background(), execRequest(repo))
	require.NoError(t, err)

	cwd := strings.TrimSpace(f.read(t, "pwd"))
	realRepo, err := filepath.EvalSymlinks(repo)
	require.NoError(t, err)
	realTmp, err := filepath.EvalSymlinks(f.tmp)
	require.NoError(t, err)
	assert.NotEqual(t, realRepo, cwd, "claude writes .claude/ into its cwd, which must not be the repo")
	assert.True(t, strings.HasPrefix(cwd, realTmp+string(filepath.Separator)), "cwd %q should be a scratch dir under %q", cwd, realTmp)
	assert.Equal(t, "work", filepath.Base(cwd))
	assert.Empty(t, f.scratchDirs(t))
}

func TestExec_KeepsItsScratchDirOnFailure(t *testing.T) {
	f := newFakeClaude(t, "fail")

	_, err := newRunner(f).Exec(context.Background(), execRequest(t.TempDir()))
	require.Error(t, err)

	dirs := f.scratchDirs(t)
	require.Len(t, dirs, 1)
	stderr, err := os.ReadFile(filepath.Join(dirs[0], "claude.stderr"))
	require.NoError(t, err)
	assert.Contains(t, string(stderr), "boom")
}

func TestExec_OmitsModelAndEffortWhenUnset(t *testing.T) {
	f := newFakeClaude(t, "ok")

	_, err := newRunner(f).Exec(context.Background(), execRequest(t.TempDir()))
	require.NoError(t, err)

	argv := f.argv(t)
	assert.NotContains(t, argv, "--model")
	assert.NotContains(t, argv, "--effort")
}

func TestExec_Failures(t *testing.T) {
	tests := []struct {
		mode    string
		wantErr string
	}{
		{"fail", "boom: something broke"},
		{"badjson", "printed no result"},
		{"iserror", "ran out of turns"},
		{"nostructured", "no structured output"},
		{"nullstructured", "no structured output"},
	}
	for _, tc := range tests {
		t.Run(tc.mode, func(t *testing.T) {
			f := newFakeClaude(t, tc.mode)

			res, err := newRunner(f).Exec(context.Background(), execRequest(t.TempDir()))
			require.Error(t, err)
			assert.Nil(t, res)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

func TestExec_EnvStripsTheParentSessionAndDisablesMemory(t *testing.T) {
	f := newFakeClaude(t, "ok")
	for _, name := range strippedEnv {
		t.Setenv(name, "leak")
	}
	t.Setenv("CLAUDE_CONFIG_DIR", "/my/claude/config")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "slot-login")

	_, err := newRunner(f).Exec(context.Background(), execRequest(t.TempDir()))
	require.NoError(t, err)

	env := strings.Split(f.read(t, "env"), "\n")
	assert.Contains(t, env, "CLAUDE_CONFIG_DIR=/my/claude/config", "the login lives under CLAUDE_CONFIG_DIR")
	assert.Contains(t, env, "CLAUDE_CODE_OAUTH_TOKEN=slot-login", "a slop-haus slot's login is this token")
	assert.Contains(t, env, "CLAUDE_CODE_DISABLE_AUTO_MEMORY=1")
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		assert.NotContains(t, strippedEnv, name, "%s must not reach the review session", name)
	}
	// Pinned as a literal list: building it from strippedEnv would let a
	// name drop out of both at once.
	for _, name := range []string{
		"CLAUDECODE", "CLAUDE_PID", "CLAUDE_EFFORT", "CLAUDE_CODE_ENTRYPOINT", "CLAUDE_CODE_EXECPATH",
		"CLAUDE_CODE_SESSION_ID", "CLAUDE_CODE_CHILD_SESSION", "CLAUDE_CODE_SESSION_ATTENDED",
		"CLAUDE_CODE_MESSAGING_SOCKET", "CLAUDE_CODE_MESSAGING_TOKEN",
		"ANTHROPIC_MODEL", "CLAUDE_CODE_EFFORT_LEVEL", "CLAUDE_CODE_SUBAGENT_MODEL", "CLAUDE_CODE_SUBAGENT_MODEL_FORCE",
	} {
		assert.Contains(t, strippedEnv, name)
	}
}

func TestExec_HonoursContextCancellation(t *testing.T) {
	f := newFakeClaude(t, "block")
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		_, err := newRunner(f).Exec(ctx, execRequest(t.TempDir()))
		done <- err
	}()

	select {
	case err := <-done:
		require.Error(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("Exec did not return after its context was cancelled")
	}
}

func TestExec_TimeoutBoundsTheRun(t *testing.T) {
	f := newFakeClaude(t, "block")
	r := New(&Options{Command: f.command, Timeout: 200 * time.Millisecond}, slog.DiscardHandler)

	done := make(chan error, 1)
	go func() {
		_, err := r.Exec(context.Background(), execRequest(t.TempDir()))
		done <- err
	}()

	select {
	case err := <-done:
		require.Error(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("Exec did not return after its timeout")
	}
}

func TestCheckLogin(t *testing.T) {
	tests := []struct {
		name    string
		out     string
		exit    string
		wantErr bool
	}{
		{"logged in", `{"loggedIn":true}`, "0", false},
		{"logged out", `{"loggedIn":false}`, "0", true},
		{"not json", "Not logged in", "0", true},
		{"non-zero exit", `{"loggedIn":true}`, "1", true},
		{"empty", "", "0", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeClaude(t, "ok")
			t.Setenv("FAKE_CLAUDE_AUTH_OUT", tc.out)
			t.Setenv("FAKE_CLAUDE_AUTH_EXIT", tc.exit)

			err := newRunner(f).CheckLogin(context.Background())
			if tc.wantErr {
				require.Error(t, err)
				assert.True(t, errors.Is(err, ErrNotLoggedIn))
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestExec_RefusesARelativeRepoRoot(t *testing.T) {
	f := newFakeClaude(t, "ok")

	_, err := newRunner(f).Exec(context.Background(), &ExecRequest{RepoRoot: ".", Prompt: "p", Schema: []byte(`{}`)})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must be absolute")
	assert.NoFileExists(t, filepath.Join(f.dir, "argv"), "claude ran anyway")
}

// A tool command claude started must die with it. In "orphan" mode the
// fake's own child holds stdout open, so killing only the fake would leave
// that child running and Exec waiting out WaitDelay; the deadline is set
// well under WaitDelay to tell the two apart.
func TestExec_CancellationKillsClaudesChildren(t *testing.T) {
	f := newFakeClaude(t, "orphan")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		_, err := newRunner(f).Exec(ctx, execRequest(t.TempDir()))
		done <- err
	}()
	var child int
	require.Eventually(t, func() bool {
		b, err := os.ReadFile(filepath.Join(f.dir, "child"))
		if err != nil {
			return false
		}
		child, err = strconv.Atoi(strings.TrimSpace(string(b)))
		return err == nil
	}, 5*time.Second, 10*time.Millisecond, "the fake never started its child")
	t.Cleanup(func() { _ = syscall.Kill(child, syscall.SIGKILL) })

	cancel()
	select {
	case err := <-done:
		require.Error(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("Exec waited on claude's child after cancellation")
	}
	assert.Eventually(t, func() bool {
		return errors.Is(syscall.Kill(child, 0), syscall.ESRCH)
	}, 2*time.Second, 10*time.Millisecond, "claude's child outlived the cancelled run")
}

func settingsOf(t *testing.T, r *Runner, repo string) (allowWrite, denyWrite []string) {
	t.Helper()
	a, err := r.args(execRequest(repo))
	require.NoError(t, err)
	var settings struct {
		Sandbox struct {
			Filesystem struct {
				AllowWrite []string `json:"allowWrite"`
				DenyWrite  []string `json:"denyWrite"`
			} `json:"filesystem"`
			Network struct {
				DeniedDomains []string `json:"deniedDomains"`
			} `json:"network"`
		} `json:"sandbox"`
	}
	require.NoError(t, json.Unmarshal([]byte(valueAfter(t, a, "--settings")), &settings))
	assert.Equal(t, []string{"*"}, settings.Sandbox.Network.DeniedDomains)
	return settings.Sandbox.Filesystem.AllowWrite, settings.Sandbox.Filesystem.DenyWrite
}

func TestArgs_RunChecksAllowsWritingBuildCachesButStillDeniesTheRepo(t *testing.T) {
	repo := t.TempDir()

	allow, deny := settingsOf(t, New(&Options{RunChecks: true, WritableRoots: []string{"/cache/go-build"}}, slog.DiscardHandler), repo)
	assert.Equal(t, []string{"/cache/go-build"}, allow)
	assert.Equal(t, []string{repo}, deny)

	allow, _ = settingsOf(t, New(&Options{RunChecks: true, WritableRoots: []string{"/cache/go-build"}, TempDir: "/scratch"}, slog.DiscardHandler), repo)
	assert.Equal(t, []string{"/cache/go-build", "/scratch"}, allow, "the scratch temp dir is writable, not the system one")

	allow, deny = settingsOf(t, New(&Options{WritableRoots: []string{"/cache/go-build"}}, slog.DiscardHandler), repo)
	assert.Empty(t, allow, "without RunChecks nothing extra is writable")
	assert.Equal(t, []string{repo}, deny)
}

func TestArgs_RunChecksKeepsTheSandboxSafetySettings(t *testing.T) {
	repo := t.TempDir()
	a, err := New(&Options{RunChecks: true, WritableRoots: []string{"/cache"}, TempDir: "/scratch"}, slog.DiscardHandler).args(execRequest(repo))
	require.NoError(t, err)
	var settings struct {
		Sandbox struct {
			Enabled                  bool `json:"enabled"`
			FailIfUnavailable        bool `json:"failIfUnavailable"`
			AllowUnsandboxedCommands bool `json:"allowUnsandboxedCommands"`
			Filesystem               struct {
				DenyWrite []string `json:"denyWrite"`
			} `json:"filesystem"`
			Network struct {
				AllowedDomains []string `json:"allowedDomains"`
				DeniedDomains  []string `json:"deniedDomains"`
			} `json:"network"`
		} `json:"sandbox"`
	}
	require.NoError(t, json.Unmarshal([]byte(valueAfter(t, a, "--settings")), &settings))
	assert.True(t, settings.Sandbox.Enabled)
	assert.True(t, settings.Sandbox.FailIfUnavailable)
	assert.False(t, settings.Sandbox.AllowUnsandboxedCommands)
	assert.Empty(t, settings.Sandbox.Network.AllowedDomains)
	assert.Equal(t, []string{"*"}, settings.Sandbox.Network.DeniedDomains)
	assert.Equal(t, []string{repo}, settings.Sandbox.Filesystem.DenyWrite)
}

// The review's shell runs with permissions skipped, so it must not hold
// the caller's credentials; claude's own login must still get through.
func TestEnv_DropsCredentialsButKeepsClaudesLogin(t *testing.T) {
	for _, name := range []string{
		"SLOP_REVIEW_REPORT_TOKEN", "SLOP_REVIEW_REPORT_URL", "OPENROUTER_API_KEY", "WOODPECKER_TOKEN",
		"AWS_SECRET_ACCESS_KEY", "DB_PASSWORD", "GOOGLE_APPLICATION_CREDENTIALS", "gh_token",
	} {
		t.Setenv(name, "leak")
	}
	kept := []string{"CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "HOME", "PATH", "GIT_CONFIG_COUNT", "GIT_CONFIG_KEY_0", "GIT_CONFIG_VALUE_0"}
	for _, name := range kept {
		t.Setenv(name, "kept")
	}

	env := New(&Options{}, slog.DiscardHandler).env()
	for _, kv := range env {
		assert.NotEqual(t, "leak", strings.SplitN(kv, "=", 2)[1], kv)
	}
	for _, name := range kept {
		assert.Contains(t, env, name+"=kept")
	}
}

func TestEnv_RunChecksPointsTMPDIRAtTheScratchDir(t *testing.T) {
	t.Setenv("TMPDIR", "/inherited")
	env := New(&Options{RunChecks: true, TempDir: "/scratch"}, slog.DiscardHandler).env()
	// exec uses the last value of a duplicated key, so the override must
	// come after the inherited one.
	assert.Equal(t, "TMPDIR=/scratch", lastWithPrefix(env, "TMPDIR="))
	assert.NotContains(t, New(&Options{TempDir: "/scratch"}, slog.DiscardHandler).env(), "TMPDIR=/scratch", "only under run-checks")
}

func TestEnv_RunChecksStopsPythonWritingBytecodeIntoTheRepo(t *testing.T) {
	t.Setenv("PYTHONDONTWRITEBYTECODE", "")
	assert.Contains(t, New(&Options{RunChecks: true}, slog.DiscardHandler).env(), "PYTHONDONTWRITEBYTECODE=1")
	// The parent may already export it, so only what env() adds counts.
	require.NoError(t, os.Unsetenv("PYTHONDONTWRITEBYTECODE"))
	assert.NotContains(t, New(&Options{}, slog.DiscardHandler).env(), "PYTHONDONTWRITEBYTECODE=1")
}

func lastWithPrefix(env []string, prefix string) string {
	last := ""
	for _, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			last = kv
		}
	}
	return last
}

// A plan's limit is what a fallback is for, so it is told apart; but only
// from Claude Code's own error result, never from the model's text, which
// may well talk about limits in the code it reviewed.
func TestDecodeResult_AUsageLimitIsToldApart(t *testing.T) {
	errorResult := func(subtype, text string) []byte {
		b, err := json.Marshal(map[string]any{"is_error": true, "subtype": subtype, "result": text})
		require.NoError(t, err)
		return b
	}
	for _, text := range []string{
		"Claude AI usage limit reached|1762300800",
		"You've hit your usage limit",
		"You've hit your weekly limit · resets Nov 10",
		"5-hour limit reached ∙ resets 3pm",
	} {
		_, err := decodeResult(errorResult("success", text), nil, nil)
		assert.ErrorIs(t, err, ErrUsageLimit, text)
	}
	for name, out := range map[string][]byte{
		"a context limit":         errorResult("success", "Context limit reached"),
		"the model's own words":   errorResult("error_max_turns", "When clients hit your API too often the rate limit applies"),
		"a long result":           errorResult("success", "Reviewing the limiter: when callers hit your endpoint past the limit "+strings.Repeat("and so on ", 40)),
		"an error during the run": errorResult("error_during_execution", "usage limit"),
	} {
		_, err := decodeResult(out, nil, nil)
		require.Error(t, err, name)
		assert.NotErrorIs(t, err, ErrUsageLimit, name)
	}
}

func TestDecodeResult_Tokens(t *testing.T) {
	const prefix = `{"is_error":false,"subtype":"success","structured_output":{"ok":true},`
	tests := []struct {
		name string
		tail string
		want int
	}{
		{
			"sums every model's four kinds of token",
			`"usage":{"input_tokens":1,"output_tokens":1,"cache_read_input_tokens":1,"cache_creation_input_tokens":1},` +
				`"modelUsage":{"a":{"inputTokens":2,"outputTokens":18,"cacheReadInputTokens":11873,"cacheCreationInputTokens":15501},"b":{"inputTokens":10,"outputTokens":5}}}`,
			2 + 18 + 11873 + 15501 + 10 + 5,
		},
		{
			"falls back to usage without modelUsage",
			`"usage":{"input_tokens":2,"output_tokens":18,"cache_read_input_tokens":11873,"cache_creation_input_tokens":15501}}`,
			2 + 18 + 11873 + 15501,
		},
		{"absent", `"modelUsage":{"a":{}}}`, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res, err := decodeResult([]byte(prefix+tc.tail), nil, nil)
			require.NoError(t, err)
			assert.Equal(t, tc.want, res.Tokens)
		})
	}
}

func TestArgs_RefusesWritableRootsThatExposeTheRepo(t *testing.T) {
	repo := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	require.NoError(t, os.Symlink(repo, link))
	tests := map[string]string{
		"root":       "/",
		"the repo":   repo,
		"ancestor":   filepath.Dir(repo),
		"inside":     filepath.Join(repo, ".cache", "go-build"),
		"symlink":    link,
		"relative":   "cache",
		"dot-dot up": filepath.Join(repo, "x", ".."),
	}
	for name, root := range tests {
		t.Run(name, func(t *testing.T) {
			r := New(&Options{RunChecks: true, WritableRoots: []string{root}}, slog.DiscardHandler)
			_, err := r.args(execRequest(repo))
			require.Error(t, err)
			r = New(&Options{RunChecks: true, TempDir: root}, slog.DiscardHandler)
			_, err = r.args(execRequest(repo))
			require.Error(t, err)
		})
	}
	_, err := New(&Options{RunChecks: true, WritableRoots: []string{t.TempDir()}}, slog.DiscardHandler).args(execRequest(repo))
	require.NoError(t, err)
	// Without RunChecks the roots are ignored, so they aren't judged.
	_, err = New(&Options{WritableRoots: []string{"/"}}, slog.DiscardHandler).args(execRequest(repo))
	require.NoError(t, err)
}

func TestCheckLogin_MissingExecutableIsUnavailable(t *testing.T) {
	r := New(&Options{Command: filepath.Join(t.TempDir(), "missing-claude")}, slog.DiscardHandler)
	assert.ErrorIs(t, r.CheckLogin(context.Background()), ErrUnavailable)
	_, err := r.Exec(context.Background(), execRequest(t.TempDir()))
	assert.ErrorIs(t, err, ErrUnavailable)
}

func TestCheckSandbox_NamesThePackageToInstall(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	for tool, pkg := range map[string]string{"bwrap": "bubblewrap", "socat": "socat"} {
		t.Run(tool, func(t *testing.T) {
			err := New(&Options{SandboxTools: []string{tool}}, slog.DiscardHandler).CheckSandbox()
			assert.ErrorIs(t, err, ErrNoSandbox)
			assert.Contains(t, err.Error(), tool+" is not on PATH")
			assert.Contains(t, err.Error(), "apt install "+pkg)
			assert.Contains(t, err.Error(), "not ~/.bashrc")
		})
	}
}

func TestExec_NoSandboxIsReportedAsSuch(t *testing.T) {
	f := newFakeClaude(t, "nosandbox")
	_, err := newRunner(f).Exec(context.Background(), execRequest(t.TempDir()))
	assert.ErrorIs(t, err, ErrNoSandbox)
}

func TestExec_SIGTERMKillsClaudesChildren(t *testing.T) {
	f := newFakeClaude(t, "orphan")
	done := make(chan error, 1)
	go func() {
		_, err := newRunner(f).Exec(context.Background(), execRequest(t.TempDir()))
		done <- err
	}()
	var child int
	require.Eventually(t, func() bool {
		b, err := os.ReadFile(filepath.Join(f.dir, "child"))
		if err != nil {
			return false
		}
		child, err = strconv.Atoi(strings.TrimSpace(string(b)))
		return err == nil
	}, 5*time.Second, 10*time.Millisecond)
	t.Cleanup(func() { _ = syscall.Kill(child, syscall.SIGKILL) })

	require.NoError(t, syscall.Kill(os.Getpid(), syscall.SIGTERM))
	select {
	case err := <-done:
		require.Error(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("Exec did not stop on SIGTERM")
	}
	assert.Eventually(t, func() bool {
		return errors.Is(syscall.Kill(child, 0), syscall.ESRCH)
	}, 2*time.Second, 10*time.Millisecond, "claude's child outlived SIGTERM")
}

// A review run from inside a worker's `claude -p` must not take on that
// worker's identity, settings, MCP servers or permissions. The parent is
// simulated by its environment and a config dir full of settings; what
// the nested run gets is read back from the fake.
func TestExec_NestedInAWorkerInheritsNoneOfItsSession(t *testing.T) {
	f := newFakeClaude(t, "ok")
	home := t.TempDir()
	cfg := filepath.Join(home, ".claude")
	require.NoError(t, os.MkdirAll(cfg, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(cfg, "settings.json"),
		[]byte(`{"permissions":{"allow":["Bash(*)"]},"hooks":{"Stop":[{"hooks":[{"type":"command","command":"x"}]}]},"enableAllProjectMcpServers":true}`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(cfg, "CLAUDE.md"), []byte("run slop-review before done"), 0o644))
	repo := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(repo, "CLAUDE.md"), []byte("project instructions"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(repo, ".mcp.json"), []byte(`{"mcpServers":{"slop-haus":{"command":"x"}}}`), 0o644))

	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", cfg)
	for _, name := range strippedEnv {
		t.Setenv(name, "worker-"+name)
	}

	_, err := newRunner(f).Exec(context.Background(), execRequest(repo))
	require.NoError(t, err)

	env := f.read(t, "env")
	for _, name := range strippedEnv {
		assert.NotContains(t, env, name+"=", "the worker's %s leaked", name)
	}
	assert.NotContains(t, env, "worker-")

	argv := f.argv(t)
	assert.True(t, containsPair(argv, "--setting-sources", ""), "user/project settings, CLAUDE.md and hooks must stay out")
	assert.Contains(t, argv, "--strict-mcp-config", "no MCP servers beyond those passed (none)")
	assert.NotContains(t, argv, "--mcp-config")
	assert.Contains(t, argv, "--disable-slash-commands")
	assert.Contains(t, argv, "--no-session-persistence")
	assert.Equal(t, "Read,Grep,Glob,Bash", valueAfter(t, argv, "--tools"))
	for _, a := range argv {
		assert.NotContains(t, a, "--permission-mode")
		assert.NotContains(t, a, "--allowedTools")
		assert.NotContains(t, a, "--continue")
		assert.NotContains(t, a, "--resume")
	}
	settings := valueAfter(t, argv, "--settings")
	for _, k := range []string{`"hooks"`, `"mcpServers"`, `"enabledPlugins"`} {
		assert.NotContains(t, settings, k, "the inline settings carry nothing but the sandbox")
	}
	// Its only permissions are the read denials: nothing the worker allowed.
	var parsed struct {
		Permissions map[string]json.RawMessage `json:"permissions"`
	}
	require.NoError(t, json.Unmarshal([]byte(settings), &parsed))
	for k := range parsed.Permissions {
		assert.Equal(t, "deny", k)
	}
}

func TestExec_RefusesAScratchDirInsideTheRepo(t *testing.T) {
	f := newFakeClaude(t, "ok")
	repo := t.TempDir()
	t.Setenv("TMPDIR", repo)

	_, err := newRunner(f).Exec(context.Background(), execRequest(repo))
	require.Error(t, err)
	assert.NoFileExists(t, filepath.Join(f.dir, "argv"), "claude ran anyway")
}
