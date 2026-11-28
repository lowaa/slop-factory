package scripted_test

import (
	"context"
	"fmt"
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
	os.Exit(m.Run())
}

func optIn(t *testing.T) {
	t.Helper()
	scripted.OptIn(t)
}

func binary(t *testing.T, name string) string {
	t.Helper()
	return scripted.Binary(t, name)
}

// newTarget is one of scripted's Target constructors.
type newTarget func(*testing.T, *scripted.TargetOptions) *scripted.Target

// subject is a harness's conformance subject: a scripted model, a puppet on
// it, and one workspace, reused for the resume (a harness finds a
// conversation to resume by its directory). The harness is launched through
// the puppet's launcher, which reports its pid for the crash test.
func subject(t *testing.T, name, bin string, shell *scripted.Shell, target newTarget) *conformance.Subject {
	t.Helper()
	server, err := scriptedmodel.New(nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = server.Close() })
	briefing := "TIER2-BRIEFING-" + name + "-5c1e"
	puppet, err := scripted.New(t, &scripted.Options{Server: server, Shell: shell, Briefing: briefing})
	require.NoError(t, err)
	launcher, env, err := puppet.Launcher(bin)
	require.NoError(t, err)
	tg := target(t, &scripted.TargetOptions{Binary: launcher, ServerURL: server.URL()})
	briefingFile := filepath.Join(t.TempDir(), "briefing.md")
	require.NoError(t, os.WriteFile(briefingFile, []byte(briefing), 0o600))
	ws := &harness.Workspace{
		Agent: "a1", Ticket: "7", Dir: t.TempDir(), Permissive: true,
		Briefing: briefing, BriefingFile: briefingFile, Env: env,
	}
	tg.Workspace(ws)
	return &conformance.Subject{
		Harness:   puppet.Harness(tg.Harness),
		Workspace: func(*testing.T) *harness.Workspace { return ws },
		Puppet:    puppet,
		Timeout:   stepTimeout,
	}
}

func TestClaudeCodeConformance(t *testing.T) {
	t.Parallel()
	optIn(t)
	bin := binary(t, "claude")
	conformance.Run(t, func(t *testing.T) *conformance.Subject {
		return subject(t, "claude", bin, scripted.ClaudeCodeShell, scripted.ClaudeCode)
	})
}

func TestCodexConformance(t *testing.T) {
	t.Parallel()
	optIn(t)
	bin := binary(t, "codex")
	conformance.Run(t, func(t *testing.T) *conformance.Subject {
		return subject(t, "codex", bin, scripted.CodexShell, scripted.Codex)
	})
}

func TestPiConformance(t *testing.T) {
	t.Parallel()
	optIn(t)
	bin := binary(t, "pi")
	conformance.Run(t, func(t *testing.T) *conformance.Subject {
		return subject(t, "pi", bin, scripted.PiShell, scripted.Pi)
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
