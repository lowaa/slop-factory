package scripted

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-bot/internal/harness"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/claudecode"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/codex"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/pi"
)

// The targets' setup without the harnesses: what each one points its
// adapter at, and that none of it is the user's.

const testURL = "http://127.0.0.1:1"

func readJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path) // #nosec G304 -- the test's own temp file
	require.NoError(t, err)
	var v map[string]any
	require.NoError(t, json.Unmarshal(raw, &v))
	return v
}

func envValue(env []string, name string) (string, bool) {
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok && k == name {
			return v, true
		}
	}
	return "", false
}

func TestClaudeCodeTargetRunsOnItsOwnConfigAndTheDummyKey(t *testing.T) {
	t.Parallel()
	tg := ClaudeCode(t, &TargetOptions{Binary: "/bin/claude", ServerURL: testURL, PreToolHook: []string{"hook"}})
	assert.Equal(t, claudecode.Name, tg.Name)
	assert.True(t, tg.APIKeyOptIn)
	assert.True(t, tg.Guarded)
	assert.Equal(t, testURL, tg.Env["ANTHROPIC_BASE_URL"])
	h, ok := tg.Harness.(*claudecode.Harness)
	require.True(t, ok)
	assert.Equal(t, []string{"hook"}, h.PreToolHook)

	env := h.Environ()
	config, ok := envValue(env, "CLAUDE_CONFIG_DIR")
	require.True(t, ok, "the config dir is in the base environment, where the transcript reader looks")
	key, _ := envValue(env, "ANTHROPIC_API_KEY")
	assert.Equal(t, ClaudeKey, key)
	home, _ := envValue(env, "HOME")
	assert.NotEqual(t, os.Getenv("HOME"), home)

	cfg := readJSON(t, filepath.Join(config, ".claude.json"))
	assert.Equal(t, true, cfg["hasCompletedOnboarding"])
	assert.Equal(t, map[string]any{"approved": []any{ClaudeKey[len(ClaudeKey)-20:]}, "rejected": []any{}}, cfg["customApiKeyResponses"])
	assert.Equal(t, true, readJSON(t, filepath.Join(config, "settings.json"))["skipDangerousModePermissionPrompt"])
}

func TestTrustClaudeFolderAddsTheFolderAndKeepsTheRest(t *testing.T) {
	t.Parallel()
	tg := ClaudeCode(t, &TargetOptions{ServerURL: testURL})
	tg.TrustClaudeFolder(t, "/work/t1-app")
	tg.TrustClaudeFolder(t, "/work/t2-app")
	cfg := readJSON(t, filepath.Join(tg.claudeConfig, ".claude.json"))
	assert.Equal(t, true, cfg["hasCompletedOnboarding"], "what was there stays")
	projects, ok := cfg["projects"].(map[string]any)
	require.True(t, ok)
	for _, dir := range []string{"/work/t1-app", "/work/t2-app"} {
		assert.Equal(t, map[string]any{"hasTrustDialogAccepted": true}, projects[dir])
	}
}

func TestCodexTargetPointsItsProviderAtTheServer(t *testing.T) {
	t.Parallel()
	tg := Codex(t, &TargetOptions{Binary: "/bin/codex", ServerURL: testURL})
	assert.Equal(t, codex.Name, tg.Name)
	assert.False(t, tg.Guarded, "app-server runs no hooks")
	h, ok := tg.Harness.(*codex.Harness)
	require.True(t, ok)
	assert.Equal(t, testURL+"/v1", h.ModelProvider.BaseURL)
	env := h.Environ()
	codexHome, ok := envValue(env, "CODEX_HOME")
	require.True(t, ok)
	assert.DirExists(t, codexHome)
	_, ok = envValue(env, h.ModelProvider.EnvKey)
	assert.True(t, ok, "the provider's key is set")
}

func TestPiTargetDefinesTheScriptedProvider(t *testing.T) {
	t.Parallel()
	tg := Pi(t, &TargetOptions{Binary: "/bin/pi", ServerURL: testURL})
	assert.Equal(t, pi.Name, tg.Name)
	h, ok := tg.Harness.(*pi.Harness)
	require.True(t, ok)
	models := readJSON(t, filepath.Join(h.AgentDir, "models.json"))
	provider := models["providers"].(map[string]any)["scripted"].(map[string]any)
	assert.Equal(t, testURL+"/v1", provider["baseUrl"])
	assert.Equal(t, "scripted/scripted-model-1", tg.Model)
	assert.FileExists(t, filepath.Join(h.AgentDir, "settings.json"))
}

func TestWorkspaceAddsTheTargetsModelKeyAndVariables(t *testing.T) {
	t.Parallel()
	ws := &harness.Workspace{}
	Pi(t, &TargetOptions{ServerURL: testURL}).Workspace(ws)
	assert.Equal(t, "scripted/scripted-model-1", ws.Model)
	assert.False(t, ws.APIKeyOptIn)
	assert.Equal(t, "1", ws.Env["PI_OFFLINE"])

	ws = &harness.Workspace{Model: "kept", Env: map[string]string{"MINE": "1"}}
	ClaudeCode(t, &TargetOptions{ServerURL: testURL}).Workspace(ws)
	assert.Equal(t, "kept", ws.Model, "a target with no model leaves the workspace's")
	assert.True(t, ws.APIKeyOptIn)
	assert.Equal(t, "1", ws.Env["MINE"])
	assert.Equal(t, testURL, ws.Env["ANTHROPIC_BASE_URL"])
}

func TestShellsCallEachHarnesssTool(t *testing.T) {
	t.Parallel()
	for shell, want := range map[*Shell]string{ClaudeCodeShell: "command", CodexShell: "cmd", PiShell: "command"} {
		assert.Equal(t, "echo hi", shell.Input("echo hi")[want], shell.Tool)
	}
}

func TestEnvironIsCleanAndACopy(t *testing.T) {
	t.Parallel()
	get := environ("/home/x", "EXTRA=1")
	first := get()
	keys := make([]string, len(first))
	for i, kv := range first {
		keys[i], _, _ = strings.Cut(kv, "=")
	}
	slices.Sort(keys)
	assert.Equal(t, []string{"EXTRA", "HOME", "PATH", "TERM"}, keys)
	first[0] = "CHANGED=1"
	assert.NotEqual(t, "CHANGED=1", get()[0], "each call is a fresh slice")
}

func TestBinarySkipsWhatIsntInstalled(t *testing.T) {
	t.Parallel()
	if os.Getenv(ContainerEnv) == "1" {
		t.Skip("in the container a missing harness is a failure")
	}
	var found string
	t.Run("missing", func(t *testing.T) { found = Binary(t, "slop-bot-no-such-harness") })
	assert.Empty(t, found)
	t.Run("present", func(t *testing.T) { found = Binary(t, "sh") })
	assert.NotEmpty(t, found)
}
