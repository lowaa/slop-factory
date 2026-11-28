package scripted_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-clock"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/agy"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/conformance"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/conformance/scripted"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/fake"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/fake/faketest"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/sessionhost"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/sessionhost/tmuxtest"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/tuidriver"
)

// agy/tui in tmux, on API-key mode: GEMINI_API_KEY, opted in, makes the
// harness set modelProvider gemini for the session, and
// GOOGLE_GEMINI_BASE_URL sends the Gemini API calls to the scripted model.
// That is the production API-key path with only the endpoint changed, where
// AGY_LLM_GATEWAY_URL (scriptedmodel's README) is a gateway mode no user
// configures.
func TestAgyConformance(t *testing.T) {
	t.Parallel()
	optIn(t)
	target := binary(t, "agy")
	conformance.Run(t, func(t *testing.T) *conformance.Subject {
		f := newFixture(t, "agy", target, &scripted.Options{
			Shell: &scripted.Shell{Tool: "run_command", Input: func(cmd string) map[string]any {
				// run_command hands a still-running command back to the
				// model after WaitMsBeforeAsync, at most 10s (its schema,
				// 1.5.19): the most a long tool can last. The suite releases
				// one within moments of it starting.
				return map[string]any{"CommandLine": cmd, "toolSummary": "Wait for the test", "WaitMsBeforeAsync": 10000}
			}},
			BootReply: "ready",
		})
		home := agyHome(t)
		host := tmuxtest.Host(t, &sessionhost.TmuxOptions{ExitRecheck: 200 * time.Millisecond})
		self, err := os.Executable()
		require.NoError(t, err)
		// The hooks relay to the test through this binary, as fake/tui's
		// do (faketest.Main).
		hub, err := fake.NewHookHub(self)
		require.NoError(t, err)
		t.Cleanup(func() { _ = hub.Close() })
		h := &agy.Harness{
			Sessions: tuidriver.Sessions{Host: host, Hub: hub, Pointer: faketest.Pointer, Logger: logger(), Clock: clock.Real{}},
			Binary:   f.bin,
			// The key is in the base environment, passed on only because
			// the workspace opts in, as for an API-key agent in production.
			Environ: environ(home, "GEMINI_API_KEY=scripted-model-not-a-real-key"),
		}
		f.ws.APIKeyOptIn = true
		return f.subject(h, map[string]string{
			"GOOGLE_GEMINI_BASE_URL": f.server.URL(),
			// Each start otherwise spawns an updater that would replace the
			// installed agy (which also outlives the test). "true", not "1".
			"AGY_CLI_DISABLE_AUTO_UPDATE": "true",
			"SLOP_BOT_FAKE_REEXEC":        "1",
			// Each hook is a start of this race-built binary; without it,
			// each would wait a second at exit.
			"GORACE": "atexit_sleep_ms=0",
		})
	})
}

// agyHome is a HOME of agy's own: its ~/.gemini (conversations, history,
// settings) is never the user's, and a conversation cut off mid-turn can't
// be resumed into a later run's script (scriptedmodel's README). The
// onboarding is marked done, as a first run in it would otherwise stop at
// the colour-scheme and terms screens; the file is what agy writes when
// they are finished (1.5.19).
func agyHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	cache := filepath.Join(home, ".gemini", "antigravity-cli", "cache")
	require.NoError(t, os.MkdirAll(cache, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(cache, "onboarding.json"),
		[]byte(`{"consumerOnboardingComplete":true,"enterpriseOnboardingComplete":false,"onboardingComplete":true}`), 0o600))
	return home
}
