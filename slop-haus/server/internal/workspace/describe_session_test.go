package workspace_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/workspace"
)

// Both hooks put each payload through describe-session.py, which adds what
// the run says about itself: the harness, its version, the model, and the
// owner's labels. These run the installed hook against a server that
// captures what arrives.

func captureStop(t *testing.T) (*httptest.Server, <-chan map[string]any) {
	t.Helper()
	got := make(chan map[string]any, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var payload map[string]any
		_ = json.Unmarshal(body, &payload)
		got <- payload
		_, _ = io.WriteString(w, `{}`)
	}))
	t.Cleanup(server.Close)
	return server, got
}

// A Claude Code transcript as it ends: the latest assistant message names
// the model, and every entry names the version that wrote it.
func writeTranscript(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "session.jsonl")
	require.NoError(t, os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600))
	return path
}

func TestDescribe_ClaudesStopIsDescribedFromItsTranscriptAndRunEnv(t *testing.T) {
	server, got := captureStop(t)
	transcript := writeTranscript(t,
		`{"type":"assistant","version":"2.1.280","message":{"model":"claude-sonnet-4-5"}}`,
		`{"type":"assistant","version":"2.1.283","message":{"model":"claude-opus-5-5"}}`,
		`{"type":"assistant","version":"2.1.283","message":{"model":"<synthetic>"}}`,
		`{"type":"system","subtype":"turn_duration","version":"2.1.283"}`,
		`not json at all`,
	)
	payload := `{"session_id":"abc","transcript_path":` + jsonString(transcript) + `}`

	run := runHookWithEnv(t, server.URL, "agent-token", payload,
		"SLOP_HAUS_HARNESS=claude", "SLOP_HAUS_LABELS=gpu=a100, docker ,,=nokey")
	require.Equal(t, 0, run.exitCode, run.stderr)

	body := <-got
	assert.Equal(t, "abc", body["session_id"], "the payload itself is untouched")
	assert.Equal(t, map[string]any{
		"harness":         "claude",
		"harness_version": "2.1.283",
		"model":           "claude-opus-5-5",
		"labels":          map[string]any{"gpu": "a100", "docker": ""},
	}, withoutLayout(body))
}

// pi's extension knows its own version and model and says so through the
// environment; nothing is read from a transcript it does not have.
func TestDescribe_AHarnessCanDescribeItselfThroughTheEnvironment(t *testing.T) {
	server, got := captureStop(t)
	run := runHookWithEnv(t, server.URL, "agent-token", `{"session_id":"pi-1","cwd":"/w"}`,
		"SLOP_HAUS_HARNESS=pi", "SLOP_HAUS_HARNESS_VERSION=0.87.0",
		"SLOP_HAUS_PROVIDER=anthropic", "SLOP_HAUS_MODEL=claude-sonnet-4-5")
	require.Equal(t, 0, run.exitCode, run.stderr)
	assert.Equal(t, map[string]any{
		"harness": "pi", "harness_version": "0.87.0", "provider": "anthropic", "model": "claude-sonnet-4-5",
	}, withoutLayout(<-got))
}

func TestDescribe_ClaudesSessionStartModelIsTaken(t *testing.T) {
	server, got := captureStop(t)
	run := runHookWithEnv(t, server.URL, "agent-token", `{"session_id":"abc","model":"claude-opus-5-5"}`,
		"SLOP_HAUS_HARNESS=claude")
	require.Equal(t, 0, run.exitCode, run.stderr)
	assert.Equal(t, map[string]any{"harness": "claude", "model": "claude-opus-5-5"}, withoutLayout(<-got))
}

// run_remote_control.sh exports the harness config revision the run was started with; the
// Agents screen compares it with the current one to mark a stale session.
func TestDescribe_TheConfigRevisionIsReported(t *testing.T) {
	server, got := captureStop(t)
	run := runHookWithEnv(t, server.URL, "agent-token", `{"session_id":"abc","model":"claude-sonnet-4-5"}`,
		"SLOP_HAUS_HARNESS=claude", "SLOP_HAUS_CONFIG_REVISION=12")
	require.Equal(t, 0, run.exitCode, run.stderr)
	assert.Equal(t, map[string]any{"harness": "claude", "model": "claude-sonnet-4-5", "config_revision": "12"},
		withoutLayout(<-got))

	for _, bad := range []string{"", "-1", "12a", "1.5", "٣"} {
		server, got := captureStop(t)
		run := runHookWithEnv(t, server.URL, "agent-token", `{"session_id":"abc"}`,
			"SLOP_HAUS_HARNESS=claude", "SLOP_HAUS_CONFIG_REVISION="+bad)
		require.Equal(t, 0, run.exitCode, run.stderr)
		assert.Equal(t, map[string]any{"harness": "claude"}, withoutLayout(<-got), bad)
	}
}

// setup and update record the bundle they installed, and the hook reports it,
// so the board can mark a workspace that has not updated since the bundle changed.
func TestDescribe_TheBundleHashIsReported(t *testing.T) {
	server, got := captureStop(t)
	run := runHookWithEnv(t, server.URL, "agent-token", `{"session_id":"abc"}`, "SLOP_HAUS_HARNESS=claude")
	require.Equal(t, 0, run.exitCode, run.stderr)
	described, _ := (<-got)["slop_haus"].(map[string]any)
	assert.Equal(t, workspace.BundleHash(), described["bundle_hash"])
}

// What is reported is what the workspace recorded, not what the server serves
// now: that difference is how a stale workspace shows.
func TestDescribe_AnOlderInstalledBundleIsReportedAsItIs(t *testing.T) {
	dir := t.TempDir()
	server, got := captureStop(t)
	script := install(t, dir, workspace.Params{AgentName: "builder-1", Server: server.URL})
	seedValidCredentials(t, dir, "agent-token")
	require.NoError(t, os.WriteFile(filepath.Join(dir, workspace.BundleHashFile), []byte("0123456789abcdef\n"), 0o644))
	cmd := exec.Command(script, "-server="+server.URL)
	cmd.Stdin = strings.NewReader(`{"session_id":"abc"}`)
	cmd.Env = hookEnv("SLOP_HAUS_HARNESS=claude")
	require.NoError(t, cmd.Run())

	described, _ := (<-got)["slop_haus"].(map[string]any)
	assert.Equal(t, "0123456789abcdef", described["bundle_hash"])
	assert.NotEqual(t, workspace.BundleHash(), described["bundle_hash"])
}

// A hash a person or an old update left damaged is not a version: it is
// left out, so the board says unknown rather than showing junk.
func TestDescribe_ADamagedBundleHashFileIsLeftOut(t *testing.T) {
	for _, content := range []string{"", "not hex!\n", "ABCDEF\n", strings.Repeat("a", 500)} {
		dir := t.TempDir()
		server, got := captureStop(t)
		// Installed by the run; the file is replaced before the hook runs.
		script := install(t, dir, workspace.Params{AgentName: "builder-1", Server: server.URL})
		seedValidCredentials(t, dir, "agent-token")
		require.NoError(t, os.WriteFile(filepath.Join(dir, workspace.BundleHashFile), []byte(content), 0o644))
		cmd := exec.Command(script, "-server="+server.URL)
		cmd.Stdin = strings.NewReader(`{"session_id":"abc"}`)
		cmd.Env = hookEnv("SLOP_HAUS_HARNESS=claude")
		require.NoError(t, cmd.Run())
		described, _ := (<-got)["slop_haus"].(map[string]any)
		assert.NotContains(t, described, "bundle_hash", content)
	}
}

// A payload it cannot read is sent exactly as it came.
func TestDescribe_LeavesAPayloadItCannotReadAlone(t *testing.T) {
	for _, payload := range []string{`not json`, `[1,2]`} {
		var body string
		received := make(chan struct{}, 1)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw, _ := io.ReadAll(r.Body)
			body = string(raw)
			received <- struct{}{}
			_, _ = io.WriteString(w, `{}`)
		}))
		run := runHook(t, server.URL, "agent-token", payload)
		require.Equal(t, 0, run.exitCode, run.stderr)
		<-received
		server.Close()
		assert.Equal(t, payload, body)
	}
}

// Without python3 the description is lost, and nothing else is: the
// payload still goes, and the hook still answers.
func TestDescribe_WithoutPythonThePayloadGoesAsItCame(t *testing.T) {
	var body string
	received := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		body = string(raw)
		received <- struct{}{}
		_, _ = io.WriteString(w, `{"hookSpecificOutput":{"decision":"block","reason":"still holding #4"}}`)
	}))
	defer server.Close()

	run := runHookWithEnv(t, server.URL, "agent-token", `{"session_id":"abc"}`,
		"PATH="+pathWithoutPython(t), "SLOP_HAUS_HARNESS=claude")
	assert.Equal(t, 2, run.exitCode)
	<-received
	assert.Equal(t, `{"session_id":"abc"}`, body)
}

// withoutLayout is the payload's slop_haus object less its layout and its
// bundle hash, which every payload carries and layout_test.go and
// TestDescribe_TheBundleHashIsReported check.
func withoutLayout(payload map[string]any) map[string]any {
	described, _ := payload["slop_haus"].(map[string]any)
	out := map[string]any{}
	for k, v := range described {
		if k != "layout" && k != "bundle_hash" {
			out[k] = v
		}
	}
	return out
}

// withoutSlopHaus is a forwarded body less the slop_haus object the hook
// adds, to compare what is left with what Claude Code sent.
func withoutSlopHaus(t *testing.T, body string) string {
	t.Helper()
	var doc map[string]any
	require.NoError(t, json.Unmarshal([]byte(body), &doc))
	delete(doc, "slop_haus")
	out, err := json.Marshal(doc)
	require.NoError(t, err)
	return string(out)
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
