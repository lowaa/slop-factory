package cli

import (
	"context"
	"flag"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-bot/internal/doctor/doctortest"
	"github.com/lowaa/slop-factory/slop-bot/internal/service"
	"github.com/lowaa/slop-factory/slop-bot/internal/service/servicetest"
	"github.com/lowaa/slop-factory/slop-bot/internal/setup"
)

var updateGolden = flag.Bool("update", false, "rewrite testdata/*.golden from the current output")

const (
	installUID  = 1000
	lingerQuery = "loginctl show-user 1000 -p Linger"
)

// installHarness is the service harness with a fake PATH for doctor's
// probes, a fixed uid, and stdin for purge's confirmation.
type installHarness struct {
	*harness
	runner *servicetest.Runner
	cmds   *doctortest.Commands
	vars   map[string]string
	stdin  *strings.Reader
}

// newInstallHarness runs with logind's runtime dir, so the service and the
// CLI agree unless a test changes it. Nothing listens there.
func newInstallHarness(t *testing.T) *installHarness {
	t.Helper()
	h, runner := newServiceHarness(t)
	ih := &installHarness{harness: h, runner: runner, cmds: doctortest.Healthy(filepath.Join(h.home, ".local", "bin")),
		vars: map[string]string{"XDG_RUNTIME_DIR": "/run/user/1000", "PATH": "/usr/local/bin:/usr/bin"}}
	h.deps.Commands = ih.cmds
	h.deps.Env.UID = installUID
	h.deps.Env.Lookup = func(k string) (string, bool) { v, ok := ih.vars[k]; return v, ok }
	ih.stdin = strings.NewReader("")
	h.deps.Stdin = ih.stdin
	runner.Answer(lingerQuery, &service.Result{Stdout: "Linger=no\n"})
	return ih
}

func (h *installHarness) configFile() string {
	return filepath.Join(h.home, ".config", "slop-bot", "slop-bot-config.yaml")
}

func (h *installHarness) briefing() string {
	return filepath.Join(h.home, ".config", "slop-bot", "briefing.md")
}

func (h *installHarness) unit() string {
	return filepath.Join(h.home, ".config", "systemd", "user", "slop-bot.service")
}

func (h *installHarness) stable(s string) string { return strings.ReplaceAll(s, h.home, "/home/ada") }

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

func TestDoctorExitsNonZeroOnlyOnAFailure(t *testing.T) {
	t.Parallel()
	h := newInstallHarness(t)
	require.Equal(t, 0, h.run(t, "doctor"), h.err.String())
	assert.Contains(t, h.out.String(), "no failures, ")
	assert.Contains(t, h.out.String(), "warn  config file")

	h.cmds.Remove("git")
	assert.Equal(t, 1, h.run(t, "doctor"))
	assert.Contains(t, h.out.String(), "FAIL  git")
	assert.Contains(t, h.err.String(), "some checks failed")
}

func TestDoctorWithAMissingExplicitConfigFails(t *testing.T) {
	t.Parallel()
	h := newInstallHarness(t)
	assert.Equal(t, 1, h.run(t, "--config", filepath.Join(h.home, "nope.yaml"), "doctor"))
	assert.Contains(t, h.out.String(), "nope.yaml doesn't exist")
}

func TestInstallSummaryGolden(t *testing.T) {
	t.Parallel()
	h := newInstallHarness(t)
	repo := filepath.Join(h.home, "src", "app")
	require.NoError(t, os.MkdirAll(repo, 0o700))
	h.cmds.Answer("git -C "+repo+" rev-parse --git-dir", &service.Result{Stdout: ".git\n"})

	require.Equal(t, 0, h.run(t, "install", "--repo", "app=~/src/app"), h.err.String()+h.out.String())
	golden(t, "install", h.stable(h.out.String()))

	cfg := readFile(t, h.configFile())
	assert.Contains(t, cfg, "briefing_path: ~/.config/slop-bot/briefing.md")
	assert.Contains(t, cfg, "  - name: app\n    path: ~/src/app\n")
	assert.Equal(t, setup.BriefingStub, readFile(t, h.briefing()))
	assert.FileExists(t, h.unit())
	assert.Equal(t, []string{
		"systemctl --user show-environment", lingerQuery, // doctor
		"systemctl --user show-environment", "systemctl --user daemon-reload",
		"systemctl --user enable slop-bot.service", "systemctl --user restart slop-bot.service", lingerQuery,
	}, h.runner.Calls())
}

func TestInstallRefusesOnAFailedCheckAndWritesNothing(t *testing.T) {
	t.Parallel()
	h := newInstallHarness(t)
	h.cmds.Remove("claude")
	assert.Equal(t, 1, h.run(t, "install"))
	assert.Contains(t, h.out.String(), "FAIL  binary")
	assert.Contains(t, h.err.String(), "install stopped before writing anything")
	assert.NoFileExists(t, h.configFile())
	assert.NoFileExists(t, h.briefing())
	assert.NoFileExists(t, h.unit())
	for _, c := range h.runner.Calls() {
		assert.NotContains(t, c, "enable")
		assert.NotContains(t, c, "restart")
	}
}

func TestInstallTakesHarnessesForANewConfig(t *testing.T) {
	t.Parallel()
	h := newInstallHarness(t)
	require.Equal(t, 0, h.run(t, "install", "--harness", "codex,pi"), h.err.String()+h.out.String())
	cfg := readFile(t, h.configFile())
	assert.Contains(t, cfg, "default_harness: codex\n")
	assert.Contains(t, cfg, "  codex: {}\n  pi: {}\n  # claude-code: {}\n  # agy: {}\n")
	assert.Contains(t, h.out.String(), "harness pi", "the new config's harnesses are what doctor checked")

	h = newInstallHarness(t)
	assert.Equal(t, 1, h.run(t, "install", "--harness", "cursor"))
	assert.Contains(t, h.err.String(), "unknown harness")
	assert.NoFileExists(t, h.configFile())
}

func TestInstallRefusesABadRepoFlag(t *testing.T) {
	t.Parallel()
	h := newInstallHarness(t)
	assert.Equal(t, 1, h.run(t, "install", "--repo", "nameonly"))
	assert.Contains(t, h.err.String(), `--repo "nameonly": want name=path`)
}

func TestInstallTwiceKeepsTheUsersConfigAndBriefing(t *testing.T) {
	t.Parallel()
	h := newInstallHarness(t)
	require.Equal(t, 0, h.run(t, "install"), h.err.String()+h.out.String())
	mine := "briefing_path: briefing.md\nworkspace_dir: ~/ws\n"
	require.NoError(t, os.WriteFile(h.configFile(), []byte(mine), 0o600))
	require.NoError(t, os.WriteFile(h.briefing(), []byte("# my workflow\n"), 0o600))

	require.Equal(t, 0, h.run(t, "install", "--repo", "x=/x"), h.err.String()+h.out.String())
	assert.Equal(t, mine, readFile(t, h.configFile()))
	assert.Equal(t, "# my workflow\n", readFile(t, h.briefing()))
	out := h.out.String()
	assert.Contains(t, out, "keeping your config at "+h.configFile())
	assert.Regexp(t, `config\s+\S+slop-bot-config.yaml\s+yours, kept`, out)
	assert.Regexp(t, `briefing\s+\S+briefing.md\s+yours, kept`, out)
	assert.Regexp(t, `workspace_dir\s+`+filepath.Join(h.home, "ws"), out)
}

// A briefing deleted after install comes back as a stub, so the config
// doesn't name a missing file: a missing briefing stops agents starting.
func TestInstallRewritesAMissingDefaultBriefing(t *testing.T) {
	t.Parallel()
	h := newInstallHarness(t)
	require.Equal(t, 0, h.run(t, "install"), h.err.String()+h.out.String())
	require.NoError(t, os.Remove(h.briefing()))
	require.Equal(t, 0, h.run(t, "install"), h.err.String()+h.out.String())
	assert.FileExists(t, h.briefing())
}

func TestInstallWithAnExplicitConfigWritesItThere(t *testing.T) {
	t.Parallel()
	h := newInstallHarness(t)
	cfg := filepath.Join(h.home, "elsewhere", "sb.yaml")
	require.Equal(t, 0, h.run(t, "--config", cfg, "install"), h.err.String()+h.out.String())
	assert.FileExists(t, cfg)
	assert.Contains(t, readFile(t, h.unit()), `"--config" "`+cfg+`"`)
}

func TestUninstallKeepsDataUnlessPurged(t *testing.T) {
	t.Parallel()
	h := newInstallHarness(t)
	ended := 0
	h.deps.EndSessions = func(context.Context) error { ended++; return nil }
	require.Equal(t, 0, h.run(t, "install"), h.err.String()+h.out.String())
	require.Equal(t, 0, h.run(t, "uninstall"), h.err.String())
	assert.Equal(t, 1, ended, "with no daemon to come back, the TUI agents' sessions go too")
	assert.Contains(t, h.out.String(), "stopped and removed the slop-bot service")
	assert.Contains(t, h.out.String(), "`slop-bot uninstall --purge` deletes them")
	assert.NoFileExists(t, h.unit())
	assert.FileExists(t, h.configFile())

	require.Equal(t, 0, h.run(t, "uninstall"), h.err.String())
	assert.Contains(t, h.out.String(), "isn't installed")
}

func TestPurgeAsksFirst(t *testing.T) {
	t.Parallel()
	h := newInstallHarness(t)
	require.Equal(t, 0, h.run(t, "install"), h.err.String()+h.out.String())
	state := filepath.Join(h.home, ".local", "state", "slop-bot")
	require.NoError(t, os.MkdirAll(state, 0o700))

	h.stdin.Reset("no\n")
	assert.Equal(t, 1, h.run(t, "uninstall", "--purge"))
	assert.Contains(t, h.err.String(), "nothing was deleted")
	assert.FileExists(t, h.unit(), "declining keeps the service too")
	assert.Contains(t, h.out.String(), "  "+filepath.Join(h.home, ".config", "slop-bot")+"\n")
	assert.Contains(t, h.out.String(), "leaves workspace_dir alone")
	assert.DirExists(t, state)

	h.stdin.Reset("purge\n")
	require.Equal(t, 0, h.run(t, "uninstall", "--purge"), h.err.String())
	assert.NoDirExists(t, state)
	assert.NoFileExists(t, h.configFile())
	assert.DirExists(t, filepath.Join(h.home, ".config", "systemd"), "only slop-bot's own directories go")
}

func TestPurgeWithYesDoesNotAsk(t *testing.T) {
	t.Parallel()
	h := newInstallHarness(t)
	// The test's own: purge looks for a daemon there, and the default would
	// find the machine's real one, if it runs.
	h.vars["XDG_RUNTIME_DIR"] = h.runtime
	require.NoError(t, os.MkdirAll(filepath.Join(h.home, ".cache", "slop-bot"), 0o700))
	require.Equal(t, 0, h.run(t, "uninstall", "--purge", "--yes"), h.err.String())
	assert.NoDirExists(t, filepath.Join(h.home, ".cache", "slop-bot"))

	require.Equal(t, 0, h.run(t, "uninstall", "--purge", "--yes"), h.err.String())
	assert.Contains(t, h.out.String(), "nothing to purge")
}

func TestPurgeRefusesWhileADaemonListens(t *testing.T) {
	t.Parallel()
	h := newInstallHarness(t)
	h.vars["XDG_RUNTIME_DIR"] = h.runtime
	sock := filepath.Join(h.runtime, "slop-bot", "slop-bot.sock")
	require.NoError(t, os.MkdirAll(filepath.Dir(sock), 0o700))
	l, err := net.Listen("unix", sock)
	require.NoError(t, err)
	t.Cleanup(func() { _ = l.Close() })
	require.NoError(t, os.MkdirAll(filepath.Join(h.home, ".cache", "slop-bot"), 0o700))

	assert.Equal(t, 1, h.run(t, "uninstall", "--purge", "--yes"))
	assert.Contains(t, h.err.String(), "still running at "+sock)
	assert.DirExists(t, filepath.Join(h.home, ".cache", "slop-bot"))
}

// A shell whose runtime dir isn't the service's can't reach the daemon,
// and the error says where the service listens.
func TestClientCommandsNameTheServicesSocketWhenTheyDiffer(t *testing.T) {
	t.Parallel()
	h := newInstallHarness(t)
	require.Equal(t, 0, h.run(t, "install"), h.err.String()+h.out.String())
	h.vars["XDG_RUNTIME_DIR"] = h.runtime
	cliSock := filepath.Join(h.runtime, "slop-bot", "slop-bot.sock")

	assert.Equal(t, 1, h.run(t, "health"))
	assert.Contains(t, h.err.String(), "can't reach the slop-bot daemon at "+cliSock)
	assert.Contains(t, h.err.String(), "note: the installed service ("+h.unit()+") listens at "+
		"/run/user/1000/slop-bot/slop-bot.sock, not "+cliSock)

	require.Equal(t, 0, h.run(t, "paths"), h.err.String())
	assert.Contains(t, h.out.String(), "warning: control socket: this shell resolves "+cliSock)
	assert.Contains(t, h.out.String(), "fix: systemd always gives the service logind's runtime dir: "+
		"export XDG_RUNTIME_DIR=/run/user/1000 in your shell, or unset it")

	require.Equal(t, 0, h.run(t, "doctor"), h.err.String())
	assert.Contains(t, h.out.String(), "warn  service paths    control socket")
}

func TestClientCommandsSayWhenTheServiceIsNotInstalled(t *testing.T) {
	t.Parallel()
	h := newInstallHarness(t)
	h.vars["XDG_RUNTIME_DIR"] = h.runtime
	assert.Equal(t, 1, h.run(t, "health"))
	assert.Contains(t, h.err.String(), "note: the slop-bot service isn't installed")

	require.Equal(t, 0, h.run(t, "paths"), h.err.String())
	assert.NotContains(t, h.out.String(), "warning:")
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path) // #nosec G304 -- a path under the test's temp dir
	require.NoError(t, err)
	return string(b)
}

// workspace_dir may hold uncommitted work, so purge never deletes a
// directory it lies in, even through a symlink.
func TestPurgeRefusesToTakeTheWorkspaceWithIt(t *testing.T) {
	t.Parallel()
	for name, ws := range map[string]string{
		"nested":         "~/.local/share/slop-bot/workspace",
		"relative":       "workspace",
		"via symlink":    "~/link/workspace",
		"the dir itself": "~/.cache/slop-bot",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			h := newInstallHarness(t)
			require.NoError(t, os.MkdirAll(filepath.Join(h.home, ".local", "share", "slop-bot"), 0o700))
			require.NoError(t, os.MkdirAll(filepath.Join(h.home, ".cache", "slop-bot"), 0o700))
			require.NoError(t, os.Symlink(filepath.Join(h.home, ".local", "share", "slop-bot"), filepath.Join(h.home, "link")))
			require.NoError(t, os.MkdirAll(filepath.Dir(h.configFile()), 0o700))
			require.NoError(t, os.WriteFile(h.configFile(), []byte("workspace_dir: "+ws+"\n"), 0o600))

			assert.Equal(t, 1, h.run(t, "uninstall", "--purge", "--yes"))
			assert.Contains(t, h.err.String(), "which --purge would delete with any work in progress")
			assert.FileExists(t, h.configFile())
			assert.DirExists(t, filepath.Join(h.home, ".local", "share", "slop-bot"))
		})
	}
}

func TestPurgeRefusesWhenTheConfigDoesNotLoad(t *testing.T) {
	t.Parallel()
	h := newInstallHarness(t)
	require.NoError(t, os.MkdirAll(filepath.Dir(h.configFile()), 0o700))
	require.NoError(t, os.WriteFile(h.configFile(), []byte("workspace_dri: x\n"), 0o600))
	assert.Equal(t, 1, h.run(t, "uninstall", "--purge", "--yes"))
	assert.Contains(t, h.err.String(), "can't tell where workspace_dir is")
	assert.FileExists(t, h.configFile())
}

// Purging from a shell whose paths differ from the service's would leave
// the service's data and delete some other directories.
func TestPurgeRefusesFromAShellThatDisagreesWithTheService(t *testing.T) {
	t.Parallel()
	h := newInstallHarness(t)
	require.Equal(t, 0, h.run(t, "install"), h.err.String()+h.out.String())
	h.vars["XDG_DATA_HOME"] = filepath.Join(h.home, "other")
	require.NoError(t, os.MkdirAll(filepath.Join(h.home, "other", "slop-bot"), 0o700))
	assert.Equal(t, 1, h.run(t, "uninstall", "--purge", "--yes"))
	assert.Contains(t, h.err.String(), "purge from the shell that installed it")
	assert.FileExists(t, h.unit())
	assert.DirExists(t, filepath.Join(h.home, "other", "slop-bot"))
}

func TestConfirmationIsANoOnceCancelled(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	assert.True(t, confirmed(ctx, strings.NewReader("purge\n"), "purge"))
	// A reader that never answers: only the cancel can end the prompt.
	r, w := io.Pipe()
	t.Cleanup(func() { _ = w.Close() })
	cancel()
	assert.False(t, confirmed(ctx, r, "purge"))
}

func TestInstallSummaryRepeatsAnExplicitConfig(t *testing.T) {
	t.Parallel()
	h := newInstallHarness(t)
	cfg := filepath.Join(h.home, "sb.yaml")
	require.Equal(t, 0, h.run(t, "--config", cfg, "install"), h.err.String()+h.out.String())
	assert.Contains(t, h.out.String(), "re-running `slop-bot --config "+cfg+" install` keeps both")
}

// On Linux the service's socket is logind's whatever the shell says, and
// the summary shows the one the daemon listens on.
func TestInstallSummaryShowsTheServicesSocket(t *testing.T) {
	t.Parallel()
	h := newInstallHarness(t)
	h.vars["XDG_RUNTIME_DIR"] = h.runtime
	require.Equal(t, 0, h.run(t, "install"), h.err.String()+h.out.String())
	assert.Regexp(t, `socket\s+/run/user/1000/slop-bot/slop-bot.sock\s+the service's; this shell resolves `+
		filepath.Join(h.runtime, "slop-bot", "slop-bot.sock"), h.out.String())
}
