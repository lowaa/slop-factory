package scripted_test

import (
	"context"
	"encoding/json"
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

	"github.com/lowaa/slop-factory/go-clock"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/claudecode"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/codex"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/conformance"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/conformance/scripted"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/fake/faketest"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/pi"
	"github.com/lowaa/slop-factory/slop-bot/internal/scriptedmodel"
)

// Tier 2: the conformance suite against the real harness CLIs, each pointed
// at an in-process scripted model. Opt-in, since it needs the CLIs installed
// (tier2.sh runs it, in Docker or natively); it spends no tokens.
const (
	tier2Env = "SLOP_BOT_TIER2"
	// containerEnv is set by tier2.sh's Docker mode, where every harness
	// must be installed at its pinned version and the network must be cut.
	containerEnv = "SLOP_BOT_TIER2_CONTAINER"
)

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
	if os.Getenv(tier2Env) != "1" {
		t.Skipf("set %s=1 to run the real harnesses against the scripted model (tier2.sh)", tier2Env)
	}
}

// binary finds a harness CLI: a skip on a dev box without it, a failure in
// the container, which installs them all.
func binary(t *testing.T, name string) string {
	t.Helper()
	path, err := exec.LookPath(name)
	if err != nil {
		if os.Getenv(containerEnv) == "1" {
			require.NoError(t, err, "the tier 2 image installs %s", name)
		}
		t.Skipf("%s is not installed", name)
	}
	return path
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
	target := binary(t, "claude")
	conformance.Run(t, func(t *testing.T) *conformance.Subject {
		f := newFixture(t, "claude", target, &scripted.Options{Shell: &scripted.Shell{Tool: "Bash", Input: func(cmd string) map[string]any {
			return map[string]any{"command": cmd, "description": "Wait for the test"}
		}}})
		h := &claudecode.Harness{
			Binary: f.bin, Logger: logger(), Clock: clock.Real{},
			// The key is in the base environment, which agentproc passes on
			// only because the workspace opts in: the production path for
			// an API-key agent.
			Environ: environ(t.TempDir(), "ANTHROPIC_API_KEY=sk-ant-scripted-model-not-a-real-key"),
		}
		f.ws.APIKeyOptIn = true
		return f.subject(h, map[string]string{
			// A config dir of its own: nothing is written to ~/.claude or
			// ~/.claude.json, and no login of the user's is seen.
			"CLAUDE_CONFIG_DIR":   t.TempDir(),
			"ANTHROPIC_BASE_URL":  f.server.URL(),
			"DISABLE_AUTOUPDATER": "1", "DISABLE_TELEMETRY": "1", "DISABLE_ERROR_REPORTING": "1",
			// CLAUDE_CODE_* is dropped from the base environment (a parent
			// session's), so these go in the workspace's.
			"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1",
			// A suggested next prompt is a request with tools that the
			// script has no turn for.
			"CLAUDE_CODE_ENABLE_PROMPT_SUGGESTION": "false",
		})
	})
}

func TestCodexConformance(t *testing.T) {
	t.Parallel()
	optIn(t)
	target := binary(t, "codex")
	conformance.Run(t, func(t *testing.T) *conformance.Subject {
		f := newFixture(t, "codex", target, &scripted.Options{Shell: &scripted.Shell{Tool: "exec_command", Input: func(cmd string) map[string]any {
			// exec_command hands the model a still-running command back
			// after yield_time_ms, capped at 30s (its schema, 0.172.0): the
			// most a long tool can last. The suite releases one within
			// milliseconds of it starting.
			return map[string]any{"cmd": cmd, "yield_time_ms": 30000}
		}}})
		h := &codex.Harness{
			Binary: f.bin, Logger: logger(), Clock: clock.Real{},
			// A CODEX_HOME of its own: the user's login, config and
			// sessions are never read or written.
			Environ:       environ(t.TempDir(), "CODEX_HOME="+t.TempDir(), "SCRIPTED_MODEL_API_KEY=scripted-model-not-a-real-key"),
			ModelProvider: &codex.ModelProvider{ID: "scripted", BaseURL: f.server.URL() + "/v1", EnvKey: "SCRIPTED_MODEL_API_KEY"},
			Config:        []string{"check_for_update_on_startup=false", "analytics.enabled=false", "feedback.enabled=false"},
		}
		// A model Codex has no metadata for keeps its tools plain
		// (scriptedmodel's README).
		f.ws.Model = "scripted-model-1"
		return f.subject(h, map[string]string{})
	})
}

func TestPiConformance(t *testing.T) {
	t.Parallel()
	optIn(t)
	target := binary(t, "pi")
	conformance.Run(t, func(t *testing.T) *conformance.Subject {
		f := newFixture(t, "pi", target, &scripted.Options{Shell: &scripted.Shell{Tool: "bash", Input: func(cmd string) map[string]any {
			return map[string]any{"command": cmd}
		}}})
		h := &pi.Harness{
			Binary: f.bin, Logger: logger(), Clock: clock.Real{},
			// An agent dir of its own: the user's ~/.pi/agent (login,
			// settings, sessions) is never read or written. pi's launcher
			// still finds its install under the real HOME.
			AgentDir: piAgentDir(t, f.server.URL()),
			Environ:  environ(os.Getenv("HOME")),
		}
		f.ws.Model = "scripted/scripted-model-1"
		return f.subject(h, map[string]string{
			"SCRIPTED_MODEL_API_KEY": "scripted-model-not-a-real-key",
			"PI_OFFLINE":             "1", "PI_SKIP_VERSION_CHECK": "1", "PI_TELEMETRY": "0",
		})
	})
}

// piAgentDir defines the scripted model as a pi provider: OpenAI chat
// completions, with the dummy key from the environment.
func piAgentDir(t *testing.T, serverURL string) string {
	t.Helper()
	dir := t.TempDir()
	models := map[string]any{"providers": map[string]any{"scripted": map[string]any{
		"baseUrl": serverURL + "/v1",
		"api":     "openai-completions",
		"apiKey":  "$SCRIPTED_MODEL_API_KEY",
		"models": []any{map[string]any{
			"id": "scripted-model-1", "name": "scripted-model-1", "reasoning": true, "input": []string{"text"},
			"contextWindow": 128000, "maxTokens": 8192,
			"cost": map[string]int{"input": 0, "output": 0, "cacheRead": 0, "cacheWrite": 0},
		}},
	}}}
	for name, v := range map[string]any{
		"models.json":   models,
		"settings.json": map[string]any{"enableInstallTelemetry": false, "collapseChangelog": true},
	} {
		b, err := json.Marshal(v)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), b, 0o600))
	}
	return dir
}

// The scenarios pass on a machine with internet too, so this is what says
// the container's lockdown is really there: the scripted model is reachable,
// and the internet is not.
func TestContainerNetworkIsolation(t *testing.T) {
	t.Parallel()
	optIn(t)
	if os.Getenv(containerEnv) != "1" {
		t.Skipf("%s=1 is the container, whose network is cut", containerEnv)
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
	if os.Getenv(containerEnv) != "1" {
		t.Skipf("%s=1 is the container, which installs the pinned versions", containerEnv)
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
	if os.Getenv(containerEnv) != "1" {
		t.Skipf("%s=1 is the container, which installs tmux", containerEnv)
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
