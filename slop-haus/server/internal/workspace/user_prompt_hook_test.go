package workspace_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/workspace"
)

// userPromptRun runs the UserPromptSubmit hook from a freshly installed
// workspace, with credentials unless token is empty.
func userPromptRun(t *testing.T, server, token, payload string, env ...string) (stdout, stderr string, exitCode int) {
	t.Helper()

	dir := t.TempDir()
	install(t, dir, workspace.Params{AgentName: "builder-1", Server: server})
	if token != "" {
		seedValidCredentials(t, dir, token)
	}

	cmd := exec.Command(workspace.UserPromptHookPathIn(dir), "-server="+server)
	cmd.Stdin = strings.NewReader(payload)
	cmd.Env = hookEnv(env...)
	var outBuf, errBuf stringWriter
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	code := exitCodeOf(t, cmd.Run())
	return outBuf.text, errBuf.text, code
}

func TestUserPromptHook_PostsThePayloadWithAFreshTokenAndSaysNothing(t *testing.T) {
	var gotBody, gotAuth, gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotBody, gotAuth, gotPath = string(body), r.Header.Get("Authorization"), r.URL.Path
		// Anything it answered must still not reach Claude Code's context.
		_, _ = io.WriteString(w, `{"hookSpecificOutput":{"additionalContext":"leak"}}`)
	}))
	defer server.Close()

	payload := claudePayload(t, "UserPromptSubmit")
	stdout, _, exitCode := userPromptRun(t, server.URL, "agent-token", payload)

	assert.Equal(t, 0, exitCode)
	assert.Empty(t, stdout, "stdout becomes the prompt's context")
	assert.JSONEq(t, payload, gotBody)
	assert.Equal(t, "Bearer agent-token", gotAuth)
	assert.Equal(t, "/hooks/user-prompt-submit", gotPath)
}

func TestUserPromptHook_KeepsTheTokenOutOfCurlsArguments(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer server.Close()

	curl := newCurlRecorder(t)
	_, _, exitCode := userPromptRun(t, server.URL, "agent-token", `{"session_id":"abc"}`, "PATH="+curl.path())
	require.Equal(t, 0, exitCode)

	curl.assertTokenNotInArgs(t, "agent-token", server.URL+"/hooks/user-prompt-submit", 1)
}

// Never in a human's way: whatever goes wrong, the prompt goes through.
func TestUserPromptHook_FailsOpenAndSilent(t *testing.T) {
	unreachable := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	unreachableURL := unreachable.URL
	unreachable.Close()

	refusing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, "boom")
	}))
	defer refusing.Close()

	release := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	defer slow.Close()
	defer close(release)

	for name, tc := range map[string]struct {
		server, token string
		env           []string
	}{
		"no token":    {server: "http://127.0.0.1:1"},
		"unreachable": {server: unreachableURL, token: "agent-token"},
		"an error":    {server: refusing.URL, token: "agent-token"},
		// The knob makes the bound short enough to test.
		"a server that never answers": {server: slow.URL, token: "agent-token", env: []string{"SLOP_HAUS_PROMPT_HOOK_TIMEOUT=1"}},
	} {
		t.Run(name, func(t *testing.T) {
			stdout, stderr, exitCode := userPromptRun(t, tc.server, tc.token, `{"session_id":"abc"}`, tc.env...)

			assert.Equal(t, 0, exitCode, "exit 2 would erase the human's prompt")
			assert.Empty(t, stdout)
			assert.Empty(t, stderr)
		})
	}
}

func TestUserPromptHook_SaysNothingToASessionTheConductorHostStarted(t *testing.T) {
	var asked atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { asked.Add(1) }))
	defer server.Close()

	stdout, _, exitCode := userPromptRun(t, server.URL, "agent-token", `{"session_id":"abc"}`, "SLOP_HAUS_HOST_SESSION=1")

	assert.Equal(t, 0, exitCode)
	assert.Empty(t, stdout)
	assert.Zero(t, asked.Load())
}

func TestSettings_RegistersTheUserPromptHookAsyncSoItNeverDelaysAPrompt(t *testing.T) {
	dir := t.TempDir()
	install(t, dir, workspace.Params{AgentName: "builder-1", Server: "http://s"})

	settings := fileIn(t, dir, workspace.Settings)

	var doc struct {
		Hooks map[string][]struct {
			Hooks []map[string]any `json:"hooks"`
		} `json:"hooks"`
	}
	require.NoError(t, json.Unmarshal([]byte(settings), &doc))
	groups := doc.Hooks["UserPromptSubmit"]
	require.Len(t, groups, 1)
	require.Len(t, groups[0].Hooks, 1)
	hook := groups[0].Hooks[0]
	assert.Equal(t, true, hook["async"])
	assert.Contains(t, hook["command"], workspace.UserPromptHookPathIn(dir))
}
