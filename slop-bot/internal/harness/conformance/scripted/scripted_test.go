package scripted_test

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-bot/internal/harness"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/conformance"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/conformance/scripted"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/fake/faketest"
	"github.com/lowaa/slop-factory/slop-bot/internal/scriptedmodel"
)

// Tier 2: the conformance suite against the real harness CLIs, each pointed
// at an in-process scripted model. Opt-in, since it needs the CLIs installed
// (tier2.sh runs it, in Docker or natively); it spends no tokens.

// A step's bound: a safety net for a stuck harness, never a wait. Each step
// takes well under a second when it works.
const stepTimeout = 60 * time.Second

func TestMain(m *testing.M) {
	scripted.ExecIfLauncher()
	execIfHook()
	// Last: as the fake harness, this binary is agy's hook relay (agy_test.go).
	faketest.Main(m)
}

func optIn(t *testing.T) {
	t.Helper()
	scripted.OptIn(t)
}

func binary(t *testing.T, name string) string {
	t.Helper()
	return scripted.Binary(t, name)
}

// fixture is what every harness's subject shares: a scripted model, a
// puppet on it, and one workspace, reused for the resume (a harness finds a
// conversation to resume by its directory).
type fixture struct {
	server *scriptedmodel.Server
	puppet *scripted.Puppet
	// bin and env launch the harness through the puppet's launcher.
	bin string
	env map[string]string
	ws  *harness.Workspace
}

// newFixture fills in opts' Server and Briefing; the rest is the harness's.
func newFixture(t *testing.T, name, target string, opts *scripted.Options) *fixture {
	t.Helper()
	// The first TempDir registers their removal, and cleanups run last
	// first: so it runs after the puppet's, which ends the harnesses still
	// writing to them.
	briefingFile := filepath.Join(t.TempDir(), "briefing.md")
	server, err := scriptedmodel.New(nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = server.Close() })
	briefing := "TIER2-BRIEFING-" + name + "-5c1e"
	o := *opts
	o.Server, o.Briefing = server, briefing
	puppet, err := scripted.New(t, &o)
	require.NoError(t, err)
	bin, env, err := puppet.Launcher(target)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(briefingFile, []byte(briefing), 0o600))
	return &fixture{
		server: server, puppet: puppet, bin: bin, env: env,
		ws: &harness.Workspace{
			Agent: "a1", Ticket: "7", Dir: t.TempDir(), Permissive: true,
			Briefing: briefing, BriefingFile: briefingFile,
		},
	}
}

func (f *fixture) subject(h harness.AIHarness, extraEnv map[string]string) *conformance.Subject {
	for k, v := range f.env {
		extraEnv[k] = v
	}
	f.ws.Env = extraEnv
	return &conformance.Subject{
		Harness:   f.puppet.Harness(h),
		Workspace: func(*testing.T) *harness.Workspace { return f.ws },
		Puppet:    f.puppet,
		Timeout:   stepTimeout,
	}
}

// newTarget is one of scripted's Target constructors.
type newTarget func(*testing.T, *scripted.TargetOptions) *scripted.Target

// targetSubject is the subject for a harness scripted has a Target for,
// launched through the puppet's launcher; o says how the Target is built,
// less its binary and server, which are the fixture's.
func targetSubject(t *testing.T, name, bin string, shell *scripted.Shell, target newTarget, o scripted.TargetOptions) *conformance.Subject {
	t.Helper()
	f := newFixture(t, name, bin, &scripted.Options{Shell: shell})
	o.Binary, o.ServerURL = f.bin, f.server.URL()
	tg := target(t, &o)
	tg.Workspace(f.ws)
	return f.subject(tg.Harness, f.ws.Env)
}

// environ is a harness's whole base environment: nothing of the user's
// beyond PATH, so no credential or config of theirs can reach it.
func environ(home string, extra ...string) func() []string {
	return func() []string {
		return append([]string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "TERM=xterm"}, extra...)
	}
}

func logger() *slog.Logger { return slog.New(slog.DiscardHandler) }

func TestClaudeCodeConformance(t *testing.T) {
	t.Parallel()
	optIn(t)
	bin := binary(t, "claude")
	conformance.Run(t, func(t *testing.T) *conformance.Subject {
		return targetSubject(t, "claude", bin, scripted.ClaudeCodeShell, scripted.ClaudeCode, scripted.TargetOptions{})
	})
}

func TestCodexConformance(t *testing.T) {
	t.Parallel()
	optIn(t)
	bin := binary(t, "codex")
	conformance.Run(t, func(t *testing.T) *conformance.Subject {
		// Guarded, as the daemon runs it: every command goes through an
		// approval, which must not slow or break anything else.
		return targetSubject(t, "codex", bin, scripted.CodexShell, scripted.Codex, scripted.TargetOptions{Guard: codexGuard(t)})
	})
}

func TestPiConformance(t *testing.T) {
	t.Parallel()
	optIn(t)
	bin := binary(t, "pi")
	conformance.Run(t, func(t *testing.T) *conformance.Subject {
		return targetSubject(t, "pi", bin, scripted.PiShell, scripted.Pi, scripted.TargetOptions{})
	})
}

// The scenarios pass on a machine with internet too, so this is what says
// the container's lockdown is really there: the scripted model is reachable,
// and the internet is not.
func TestContainerNetworkIsolation(t *testing.T) {
	t.Parallel()
	optIn(t)
	if os.Getenv(scripted.ContainerEnv) != "1" {
		t.Skipf("%s=1 is the container, whose network is cut", scripted.ContainerEnv)
	}
	server, err := scriptedmodel.New(nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = server.Close() })
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL()+"/healthz", nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	// Addresses, not names: nothing here depends on DNS being cut off too.
	dialer := &net.Dialer{Timeout: 3 * time.Second}
	for _, addr := range []string{"1.1.1.1:443", "8.8.8.8:443", "[2606:4700:4700::1111]:443"} {
		conn, err := dialer.DialContext(t.Context(), "tcp", addr)
		if err == nil {
			_ = conn.Close()
		}
		assert.Error(t, err, "%s is reachable from the tier 2 container", addr)
	}
	_, err = net.DefaultResolver.LookupHost(t.Context(), "api.anthropic.com")
	assert.Error(t, err, "names resolve in the tier 2 container")
}

// The image installs each harness at its pin (tier2/harness_versions.env),
// and the run checks the pin took: a version that silently differs would
// make tier 2 test something else.
func TestContainerHarnessVersions(t *testing.T) {
	t.Parallel()
	optIn(t)
	if os.Getenv(scripted.ContainerEnv) != "1" {
		t.Skipf("%s=1 is the container, which installs the pinned versions", scripted.ContainerEnv)
	}
	for name, pin := range map[string]string{
		"claude": os.Getenv("CLAUDE_CODE_VERSION"),
		"codex":  os.Getenv("CODEX_VERSION"),
		"pi":     os.Getenv("PI_VERSION"),
		"agy":    os.Getenv("AGY_VERSION"),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			require.NotEmpty(t, pin, "tier2.sh passes the pins in")
			if pin == "latest" {
				t.Skip("HARNESS_LATEST=1 runs whatever is latest")
			}
			ctx, cancel := context.WithTimeout(t.Context(), stepTimeout)
			defer cancel()
			out, err := exec.CommandContext(ctx, binary(t, name), "--version").CombinedOutput() // #nosec G204 -- the harness names above
			require.NoError(t, err, string(out))
			assert.Contains(t, string(out), pin, fmt.Sprintf("%s --version", name))
		})
	}
}

// The TUI harnesses run under the image's tmux, which must be one slop-bot
// supports (doctor's minimum, internal/doctor/versions.go): Debian
// bookworm's own is 3.3a, so the image takes backports'.
func TestContainerTmuxIsSupported(t *testing.T) {
	t.Parallel()
	optIn(t)
	if os.Getenv(scripted.ContainerEnv) != "1" {
		t.Skipf("%s=1 is the container, which installs tmux", scripted.ContainerEnv)
	}
	ctx, cancel := context.WithTimeout(t.Context(), stepTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, binary(t, "tmux"), "-V").CombinedOutput() // #nosec G204 -- a fixed command
	require.NoError(t, err, string(out))
	var major, minor int
	_, err = fmt.Sscanf(string(out), "tmux %d.%d", &major, &minor)
	require.NoError(t, err, string(out))
	assert.True(t, major > 3 || major == 3 && minor >= 4, "tmux %d.%d is below 3.4", major, minor)
}
