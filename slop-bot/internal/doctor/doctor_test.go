package doctor_test

import (
	"context"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-bot/internal/doctor"
	"github.com/lowaa/slop-factory/slop-bot/internal/service"
)

var updateGolden = flag.Bool("update", false, "rewrite testdata/*.golden from the current report")

func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden")
	if *updateGolden {
		require.NoError(t, os.WriteFile(path, []byte(got), 0o600))
	}
	want, err := os.ReadFile(path) // #nosec G304 -- the test's own golden file
	require.NoError(t, err)
	assert.Equal(t, string(want), got, "re-run with -update if the change is intended")
}

// The full report on Linux: every harness, a briefing, repos good and bad,
// an extra binary missing, and a service installed from a shell whose data
// home differs from this one.
func TestReportGoldenLinux(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "linux")
	good := f.repo("slop-factory")
	briefing := filepath.Join(f.home, ".config", "slop-bot", "briefing.md")
	require.NoError(t, os.MkdirAll(filepath.Dir(briefing), 0o700))
	require.NoError(t, os.WriteFile(briefing, []byte("# mine\n"), 0o600))
	f.config(`briefing_path: briefing.md
default_harness: claude-code
harnesses:
  claude-code: {model: opus}
  codex: {model: gpt-6.2-astra}
  pi: {}
  agy: {}
repos:
  - {name: slop-factory, path: ` + good + `}
  - {name: gone, path: ~/src/gone}
doctor: {require: [gh]}
`)
	// Installed by a shell with the defaults; this one exports a data home.
	_, err := f.opts.Service.Install(context.Background())
	require.NoError(t, err)
	f.runner.Answer("systemctl --user is-active slop-bot.service", &service.Result{Stdout: "active\n"})
	f.runner.Answer("systemctl --user is-enabled slop-bot.service", &service.Result{Stdout: "enabled\n"})
	f.vars["XDG_DATA_HOME"] = filepath.Join(f.home, "data")
	cfg := f.opts.Config
	f.resolve()
	f.opts.Config = cfg

	r := f.run()
	assert.True(t, r.Failed())
	golden(t, "report_linux", f.stable(f.text(r)))
}

// A first look on a Mac: no config yet, nothing installed.
func TestReportGoldenDarwin(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "darwin")
	f.runner.Answer("launchctl print gui/1000/dev.slop-bot.daemon", &service.Result{ExitCode: 113})
	r := f.run()
	assert.False(t, r.Failed())
	golden(t, "report_darwin", f.stable(f.text(r)))
}

func TestAllPassingReportSaysSo(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "linux")
	f.config("repos: [{name: a, path: " + f.repo("a") + "}]\n")
	_, err := f.opts.Service.Install(context.Background())
	require.NoError(t, err)
	f.runner.Answer("systemctl --user is-active slop-bot.service", &service.Result{Stdout: "active\n"})
	r := f.run()
	assert.False(t, r.Failed())
	assert.Contains(t, f.text(r), "all checks passed\n", f.text(r))
}

func TestGit(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		setup func(f *fixture)
		level doctor.Level
		want  string
	}{
		"current": {func(*fixture) {}, doctor.Pass, "2.55.0, with worktree support"},
		"missing": {func(f *fixture) { f.cmds.Remove("git") }, doctor.Fail, "not on PATH"},
		"too old": {func(f *fixture) {
			f.cmds.Answer("git --version", &service.Result{Stdout: "git version 2.34.1\n"})
		}, doctor.Fail, "2.34.1 is too old"},
		"exactly the minimum": {func(f *fixture) {
			f.cmds.Answer("git --version", &service.Result{Stdout: "git version 2.36.0\n"})
		}, doctor.Pass, "2.36.0"},
		"unreadable": {func(f *fixture) {
			f.cmds.Answer("git --version", &service.Result{ExitCode: 1})
		}, doctor.Fail, "couldn't read its version"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t, "linux")
			tc.setup(f)
			res := f.result(f.run(), "prerequisites", "git")
			assert.Equal(t, tc.level, res.Level, res.Detail)
			assert.Contains(t, res.Detail, tc.want)
		})
	}
}

func TestGitFixIsPerOS(t *testing.T) {
	t.Parallel()
	for goos, want := range map[string]string{"linux": "sudo apt install git", "darwin": "brew install git"} {
		f := newFixture(t, goos)
		f.cmds.Remove("git")
		assert.Contains(t, f.result(f.run(), "prerequisites", "git").Fix, want)
	}
}

func TestTmuxOnlyWhenATUIDriverIsConfigured(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "linux")
	f.cmds.Remove("tmux")
	assert.Equal(t, doctor.Skip, f.result(f.run(), "prerequisites", "tmux").Level)

	f.config("harnesses: {claude-code: {}, agy: {}}\n")
	assert.Equal(t, doctor.Skip, f.result(f.run(), "prerequisites", "tmux").Level,
		"no tmux for a tui harness this build can't run")

	f.opts.Implemented = func(string, string) bool { return true }
	res := f.result(f.run(), "prerequisites", "tmux")
	assert.Equal(t, doctor.Fail, res.Level)
	assert.Contains(t, res.Detail, "the tui driver (agy)")
	assert.Contains(t, res.Fix, "apt install tmux")

	f.cmds.OnPath("tmux", "/usr/bin/tmux")
	f.cmds.Answer("tmux -V", &service.Result{Stdout: "tmux 3.2a\n"})
	res = f.result(f.run(), "prerequisites", "tmux")
	assert.Equal(t, doctor.Warn, res.Level, "older than tested warns, never fails")
	assert.Contains(t, res.Detail, "tmux 3.2 is outside the tested range (3.4 or later)")
}

func TestRequiredBinaries(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "darwin")
	f.cmds.OnPath("shellcheck", "/opt/homebrew/bin/shellcheck")
	f.config("doctor: {require: [gh, shellcheck]}\n")
	r := f.run()
	gh := f.result(r, "prerequisites", "gh")
	assert.Equal(t, doctor.Fail, gh.Level)
	assert.Contains(t, gh.Fix, "brew install gh")
	assert.Equal(t, doctor.Pass, f.result(r, "prerequisites", "shellcheck").Level)
	assert.True(t, r.Failed())
}

func TestHarnessMissingSkipsItsOtherChecks(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "linux")
	f.cmds.Remove("claude")
	r := f.run()
	res := f.result(r, "harness claude-code", "binary")
	assert.Equal(t, doctor.Fail, res.Level)
	assert.Contains(t, res.Fix, "https://claude.ai/install.sh")
	for _, c := range f.cmds.Calls() {
		assert.NotContains(t, c, "claude", "nothing runs a binary that isn't there")
	}
}

func TestHarnessVersionOutsideTheTestedRangeWarns(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "linux")
	f.cmds.Answer("claude --version", &service.Result{Stdout: "2.2.0 (Claude Code)\n"})
	res := f.result(f.run(), "harness claude-code", "version")
	assert.Equal(t, doctor.Warn, res.Level)
	assert.Contains(t, res.Detail, "claude 2.2.0 is outside the tested range (2.1.332 up to 2.2)")
}

func TestClaudeSignIn(t *testing.T) {
	t.Parallel()
	loggedOut := &service.Result{Stdout: `{"loggedIn": false}`, ExitCode: 1}
	cases := map[string]struct {
		status *service.Result
		cfg    string
		key    bool
		level  doctor.Level
		want   string
	}{
		"signed in":  {nil, "", false, doctor.Pass, "signed in (claude.ai)"},
		"signed out": {loggedOut, "", false, doctor.Fail, "not signed in"},
		"a key in the env is ignored without the opt-in":      {loggedOut, "", true, doctor.Fail, "not signed in"},
		"an opted-in key warns that the service needs it too": {loggedOut, "harnesses: {claude-code: {api_key_opt_in: true}}\n", true, doctor.Warn, "only ANTHROPIC_API_KEY"},
		"a login and a key bills":                             {nil, "harnesses: {claude-code: {api_key_opt_in: true}}\n", true, doctor.Pass, "bills it at API rates"},
		"unreadable":                                          {&service.Result{Stdout: "error: unknown command"}, "", false, doctor.Fail, "couldn't read"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t, "linux")
			if tc.status != nil {
				f.cmds.Answer("claude auth status", tc.status)
			}
			if tc.key {
				f.vars["ANTHROPIC_API_KEY"] = "sk-test"
			}
			f.config(tc.cfg)
			res := f.result(f.run(), "harness claude-code", "signed in")
			assert.Equal(t, tc.level, res.Level, res.Detail)
			assert.Contains(t, res.Detail, tc.want)
			assert.NotContains(t, res.Detail, "someone@example.com", "the login's email stays out of the report")
		})
	}
}

func TestClaudeModelForm(t *testing.T) {
	t.Parallel()
	for model, level := range map[string]doctor.Level{
		"": doctor.Pass, "opus": doctor.Pass, "claude-opus-5-5": doctor.Pass, "gpt-6": doctor.Warn,
	} {
		f := newFixture(t, "linux")
		f.config("harnesses: {claude-code: {model: " + model + "}}\n")
		if model == "" {
			f.config("harnesses: {claude-code: {}}\n")
		}
		assert.Equal(t, level, f.result(f.run(), "harness claude-code", "model").Level, model)
	}
}

func codexFixture(t *testing.T, model string) *fixture {
	t.Helper()
	f := newFixture(t, "linux")
	f.config("default_harness: codex\nharnesses: {codex: {model: " + model + "}}\n")
	return f
}

func TestCodexSignIn(t *testing.T) {
	t.Parallel()
	f := codexFixture(t, "")
	assert.Equal(t, doctor.Pass, f.result(f.run(), "harness codex", "signed in").Level)
	f.cmds.Answer("codex login status", &service.Result{ExitCode: 1, Stderr: "Not logged in\n"})
	res := f.result(f.run(), "harness codex", "signed in")
	assert.Equal(t, doctor.Fail, res.Level)
	assert.Contains(t, res.Detail, "Not logged in")
	assert.Equal(t, "codex login", res.Fix)
}

func TestCodexModelFromTheAppServersList(t *testing.T) {
	t.Parallel()
	f := codexFixture(t, "")
	res := f.result(f.run(), "harness codex", "model")
	assert.Equal(t, doctor.Pass, res.Level)
	assert.Contains(t, res.Detail, "codex's default, gpt-6.2-sol")

	f = codexFixture(t, "gpt-6.2-astra")
	assert.Equal(t, doctor.Pass, f.result(f.run(), "harness codex", "model").Level)

	f = codexFixture(t, "gpt-9")
	res = f.result(f.run(), "harness codex", "model")
	assert.Equal(t, doctor.Fail, res.Level)
	assert.Contains(t, res.Detail, "gpt-9 isn't in codex's model list (gpt-6.2-sol, gpt-6.2-astra)")
}

func TestCodexAppServerThatNeverAnswersWarns(t *testing.T) {
	t.Parallel()
	f := codexFixture(t, "gpt-6.2-sol")
	// Initialized, then nothing: Done never sees the model list.
	f.cmds.Answer("codex app-server", &service.Result{Stdout: `{"id":1,"result":{}}` + "\n"})
	res := f.result(f.run(), "harness codex", "model")
	assert.Equal(t, doctor.Warn, res.Level)
	assert.Contains(t, res.Detail, "couldn't list codex's models")

	f.cmds.Answer("codex app-server", &service.Result{Stdout: `{"id":1,"error":{"code":-1,"message":"bad client"}}` + "\n"})
	res = f.result(f.run(), "harness codex", "model")
	assert.Equal(t, doctor.Warn, res.Level)
	assert.Contains(t, res.Detail, "bad client")
}

func piFixture(t *testing.T, model string) *fixture {
	t.Helper()
	f := newFixture(t, "linux")
	f.config("default_harness: pi\nharnesses: {pi: {model: " + model + "}}\n")
	return f
}

func TestPiBlankModelWarnsAndNamesTheFallback(t *testing.T) {
	t.Parallel()
	f := piFixture(t, "")
	res := f.result(f.run(), "harness pi", "model")
	assert.Equal(t, doctor.Warn, res.Level)
	assert.Contains(t, res.Detail, "openai/gpt-6.2-sol (in pi's model list)")
	assert.Contains(t, res.Fix, "pi --list-models")
}

func TestPiModel(t *testing.T) {
	t.Parallel()
	f := piFixture(t, "openai/gpt-5.5")
	f.cmds.Answer("pi auth check --model openai/gpt-5.5 --json --no-refresh", &service.Result{Stdout: `{"status":"ready"}`})
	assert.Equal(t, doctor.Pass, f.result(f.run(), "harness pi", "model").Level)

	f = piFixture(t, "openai/gpt-9")
	f.cmds.Answer("pi auth check --model openai/gpt-9 --json --no-refresh", &service.Result{Stdout: `{"status":"ready"}`})
	assert.Equal(t, doctor.Fail, f.result(f.run(), "harness pi", "model").Level)
}

func TestPiSignInIsCheckedForTheModelAgentsUse(t *testing.T) {
	t.Parallel()
	f := piFixture(t, "anthropic/claude-x")
	f.cmds.Answer("pi auth check --model anthropic/claude-x --json --no-refresh", &service.Result{
		ExitCode: 1, Stdout: `{"status":"not_ready","provider":"anthropic","reason":"credentials_not_configured"}`,
	})
	res := f.result(f.run(), "harness pi", "signed in")
	assert.Equal(t, doctor.Fail, res.Level)
	assert.Contains(t, res.Detail, "not ready for anthropic/claude-x: credentials_not_configured (anthropic)")

	f.cmds.Answer("pi auth check --model anthropic/claude-x --json --no-refresh", &service.Result{
		ExitCode: 1, Stdout: `{"status":"not_ready","provider":"anthropic","reason":"oauth_expired"}`,
	})
	assert.Equal(t, doctor.Warn, f.result(f.run(), "harness pi", "signed in").Level)
}

func agyFixture(t *testing.T, harness string) *fixture {
	t.Helper()
	f := newFixture(t, "linux")
	f.config("default_harness: agy\nharnesses: {agy: " + harness + "}\n")
	return f
}

func TestAgyTermsWarnOnAGoogleSignIn(t *testing.T) {
	t.Parallel()
	f := agyFixture(t, "{}")
	f.vars["GEMINI_API_KEY"] = "k"
	res := f.result(f.run(), "harness agy", "terms")
	assert.Equal(t, doctor.Warn, res.Level, "a key without the opt-in never reaches agy")
	assert.Contains(t, res.Detail, "Google's consumer terms")

	f = agyFixture(t, "{api_key_opt_in: true}")
	f.vars["GEMINI_API_KEY"] = "k"
	res = f.result(f.run(), "harness agy", "terms")
	assert.Equal(t, doctor.Warn, res.Level, "the key is in this shell, which the service doesn't read")
	assert.Contains(t, res.Fix, "systemctl --user import-environment GEMINI_API_KEY")
}

func TestAgySignInAndModelShareOneListing(t *testing.T) {
	t.Parallel()
	f := agyFixture(t, "{model: gemini-3.8-flash-low}")
	r := f.run()
	assert.Equal(t, doctor.Pass, f.result(r, "harness agy", "signed in").Level)
	assert.Equal(t, doctor.Pass, f.result(r, "harness agy", "model").Level)
	n := 0
	for _, c := range f.cmds.Calls() {
		if c == "agy models" {
			n++
		}
	}
	assert.Equal(t, 1, n)

	f = agyFixture(t, "{model: gemini-9}")
	assert.Equal(t, doctor.Fail, f.result(f.run(), "harness agy", "model").Level)

	f = agyFixture(t, "{model: gemini-9}")
	f.cmds.Answer("agy models", &service.Result{ExitCode: 1, Stderr: "please sign in\n"})
	r = f.run()
	res := f.result(r, "harness agy", "signed in")
	assert.Equal(t, doctor.Fail, res.Level)
	assert.Contains(t, res.Detail, "please sign in")
	assert.Equal(t, doctor.Skip, f.result(r, "harness agy", "model").Level)
}

func TestRepos(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "linux")
	notGit := filepath.Join(f.home, "plain")
	require.NoError(t, os.MkdirAll(notGit, 0o700))
	f.cmds.Answer("git -C "+notGit+" rev-parse --git-dir", &service.Result{ExitCode: 128, Stderr: "fatal: not a git repository"})
	file := filepath.Join(f.home, "file")
	require.NoError(t, os.WriteFile(file, nil, 0o600))
	f.config("repos:\n  - {name: good, path: " + f.repo("good") + "}\n  - {name: plain, path: ~/plain}\n" +
		"  - {name: file, path: ~/file}\n  - {name: gone, path: ~/gone}\n")
	r := f.run()
	assert.Equal(t, doctor.Pass, f.result(r, "repos", "good").Level)
	assert.Contains(t, f.result(r, "repos", "plain").Detail, "isn't a git repo")
	assert.Contains(t, f.result(r, "repos", "file").Detail, "isn't a directory")
	assert.Contains(t, f.result(r, "repos", "gone").Detail, "no such file or directory")
	for _, n := range []string{"plain", "file", "gone"} {
		assert.Equal(t, doctor.Fail, f.result(r, "repos", n).Level, n)
	}
}

func TestNoReposWarns(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "linux")
	res := f.result(f.run(), "repos", "repos")
	assert.Equal(t, doctor.Warn, res.Level)
	assert.Contains(t, res.Fix, "--repo name=path")
}

func TestWorkspaceDir(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "linux")
	res := f.result(f.run(), "repos", "workspace_dir")
	assert.Equal(t, doctor.Pass, res.Level)
	assert.Contains(t, res.Detail, "created on first use")
	_, err := os.Stat(filepath.Join(f.home, "slop-bot"))
	assert.ErrorIs(t, err, os.ErrNotExist, "doctor creates nothing")

	locked := filepath.Join(f.home, "locked")
	require.NoError(t, os.MkdirAll(locked, 0o500))
	f.config("workspace_dir: ~/locked/ws\n")
	res = f.result(f.run(), "repos", "workspace_dir")
	assert.Equal(t, doctor.Fail, res.Level)
	assert.Contains(t, res.Detail, locked+" isn't writable")

	f.config("workspace_dir: ~\n")
	assert.Equal(t, doctor.Pass, f.result(f.run(), "repos", "workspace_dir").Level)
	entries, err := os.ReadDir(f.home)
	require.NoError(t, err)
	for _, e := range entries {
		assert.NotContains(t, e.Name(), "doctor", "the write probe is removed")
	}
}

func TestConfig(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "linux")
	res := f.result(f.run(), "config", "config file")
	assert.Equal(t, doctor.Warn, res.Level)
	assert.Contains(t, res.Fix, "slop-bot install")

	f.opts.Installing = true
	assert.Equal(t, doctor.Pass, f.result(f.run(), "config", "config file").Level)

	f.opts.Installing = false
	f.config("workspace_dri: x\n")
	r := f.run()
	res = f.result(r, "config", "config file")
	assert.Equal(t, doctor.Fail, res.Level)
	assert.Contains(t, res.Detail, "workspace_dri")
	for _, res := range r.Results {
		assert.NotContains(t, res.Group, "harness", "no harness is checked against defaults the user didn't choose")
	}
}

func TestBriefing(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "linux")
	f.config("briefing_path: briefing.md\n")
	res := f.result(f.run(), "config", "briefing")
	assert.Equal(t, doctor.Fail, res.Level, "set but missing is an error, never skipped")
	assert.Contains(t, res.Detail, f.opts.Paths.DefaultBriefing())

	f.opts.Installing = true
	res = f.result(f.run(), "config", "briefing")
	assert.Equal(t, doctor.Pass, res.Level)
	assert.Contains(t, res.Detail, "install writes a stub")

	f.config("briefing_path: ~/elsewhere.md\n")
	assert.Equal(t, doctor.Fail, f.result(f.run(), "config", "briefing").Level, "install writes only the default")

	f.config("briefing_path: \"~\"\n")
	assert.Contains(t, f.result(f.run(), "config", "briefing").Detail, "is a directory")
}

func TestRuntimeDirThatDoesNotResolveFails(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "darwin")
	delete(f.vars, "TMPDIR")
	f.resolve()
	r := f.run()
	res := f.result(r, "platform", "runtime dir")
	assert.Equal(t, doctor.Fail, res.Level)
	assert.Contains(t, res.Detail, "neither XDG_RUNTIME_DIR nor TMPDIR")
	assert.Contains(t, res.Fix, "export XDG_RUNTIME_DIR")
	assert.Equal(t, doctor.Skip, f.result(r, "platform", "service manager").Level)
}

func TestSocketLengthIsReported(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "linux")
	res := f.result(f.run(), "platform", "socket")
	assert.Equal(t, "/run/user/1000/slop-bot/slop-bot.sock (37 of 103 bytes)", res.Detail)
}

func TestNoUserManagerFailsWithALoginHint(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "linux")
	f.runner.Answer("systemctl --user show-environment", &service.Result{ExitCode: 1, Stderr: "Failed to connect to bus"})
	res := f.result(f.run(), "platform", "service manager")
	assert.Equal(t, doctor.Fail, res.Level)
	assert.Contains(t, res.Detail, "Failed to connect to bus")
	assert.Contains(t, res.Fix, "loginctl enable-linger")
}

func TestLingerOffWarns(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "linux")
	f.runner.Answer("loginctl show-user 1000 -p Linger", &service.Result{Stdout: "Linger=no\n"})
	res := f.result(f.run(), "platform", "linger")
	assert.Equal(t, doctor.Warn, res.Level)
	assert.Contains(t, res.Fix, "loginctl enable-linger $USER")
}

func TestServiceRuntimeMismatchIsNamed(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "linux")
	_, err := f.opts.Service.Install(context.Background())
	require.NoError(t, err)
	f.vars["XDG_RUNTIME_DIR"] = "/tmp/other"
	f.resolve()
	res := f.result(f.run(), "platform", "service paths")
	assert.Equal(t, doctor.Warn, res.Level)
	assert.Equal(t, "control socket: this shell resolves /tmp/other/slop-bot/slop-bot.sock, "+
		"the service /run/user/1000/slop-bot/slop-bot.sock", res.Detail)
	assert.Equal(t, "systemd always gives the service logind's runtime dir: export XDG_RUNTIME_DIR=/run/user/1000 "+
		"in your shell, or unset it", res.Fix, "re-installing can't move the service's runtime dir")

	f.opts.Installing = true
	for _, res := range f.run().Results {
		assert.NotEqual(t, "service paths", res.Name, "install is about to rewrite the unit")
	}
}

func TestNoServiceManagerForTheOS(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "linux")
	f.opts.Service = nil
	f.opts.GOOS = "freebsd"
	res := f.result(f.run(), "platform", "service manager")
	assert.Equal(t, doctor.Fail, res.Level)
	assert.Contains(t, res.Detail, "freebsd")
}

func TestAProbeThatCannotRunIsReported(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "linux")
	f.cmds.Fail("claude auth status", errors.New("exec format error"))
	res := f.result(f.run(), "harness claude-code", "signed in")
	assert.Equal(t, doctor.Fail, res.Level)
	assert.Contains(t, res.Detail, "exec format error")
}

// The default config runs only claude-code, so a fresh machine needs only
// claude and git.
func TestDefaultsCheckOnlyWhatTheyUse(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "linux")
	_ = f.run()
	for _, c := range f.cmds.Calls() {
		assert.NotRegexp(t, `^(codex|pi|agy|tmux) `, c)
	}
	assert.NotEmpty(t, f.cmds.Calls())
}

// Probes get the environment the harness's agents would: an API key they
// don't get can't make a sign-in check pass.
func TestProbesGetTheAgentsEnvironment(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "linux")
	f.vars["ANTHROPIC_API_KEY"] = "sk-test"
	f.vars["CLAUDE_CODE_ENTRYPOINT"] = "cli"
	_ = f.run()
	env := f.cmds.Env("claude auth status")
	assert.Contains(t, env, "PATH=/usr/bin")
	assert.NotContains(t, env, "ANTHROPIC_API_KEY=sk-test")
	assert.NotContains(t, env, "CLAUDE_CODE_ENTRYPOINT=cli")

	f.config("harnesses: {claude-code: {api_key_opt_in: true}}\n")
	_ = f.run()
	assert.Contains(t, f.cmds.Env("claude auth status"), "ANTHROPIC_API_KEY=sk-test")
}

func TestUnimplementedHarness(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "linux")
	f.config("harnesses: {claude-code: {}, agy: {}}\n")
	res := f.result(f.run(), "harness agy", "implemented")
	assert.Equal(t, doctor.Warn, res.Level, "only agy's tickets are parked")
	assert.Contains(t, res.Detail, "can't run agy under the tui driver")
	f.cmds.Remove("agy")
	for _, res := range f.run().Results {
		if res.Group == "harness agy" {
			assert.Equal(t, "implemented", res.Name, "nothing else is probed: a missing agy fails nothing")
		}
	}

	f.config("default_harness: agy\nharnesses: {agy: {}}\n")
	assert.Equal(t, doctor.Fail, f.result(f.run(), "harness agy", "implemented").Level,
		"no ticket with a blank harness could start")

	for _, res := range newFixture(t, "linux").run().Results {
		assert.NotEqual(t, "implemented", res.Name, "a harness this build runs gets no row")
	}
}

func TestCodexModelOnALaterPageWarns(t *testing.T) {
	t.Parallel()
	f := codexFixture(t, "gpt-9")
	f.cmds.Answer("codex app-server", &service.Result{Stdout: `{"id":1,"result":{}}` + "\n" +
		`{"id":2,"result":{"data":[{"id":"gpt-6.2-sol","isDefault":true}],"nextCursor":"p2"}}` + "\n"})
	res := f.result(f.run(), "harness codex", "model")
	assert.Equal(t, doctor.Warn, res.Level)
	assert.Contains(t, res.Detail, "first page")
}

func TestServicePATHThatDiffersWarns(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "linux")
	_, err := f.opts.Service.Install(context.Background())
	require.NoError(t, err)
	assert.Equal(t, doctor.Pass, f.result(f.run(), "platform", "service PATH").Level)
	f.vars["PATH"] = "/opt/new/bin:/usr/bin"
	res := f.result(f.run(), "platform", "service PATH")
	assert.Equal(t, doctor.Warn, res.Level)
	assert.Contains(t, res.Fix, "re-run slop-bot install")
}
