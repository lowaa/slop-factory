package hookserver_test

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// claudePayload is what a real Claude Code sent a hook, from testdata/claude
// (harness_e2e/test.sh --capture-hook-payloads writes them). The payload is a
// contract Claude Code owns, so a test of the request schema starts from a
// real one; the hand-written payloads elsewhere in this package are for what a
// real session does not produce (a subagent's Stop, a malformed description).
func claudePayload(t *testing.T, name string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "claude", name+".json"))
	require.NoError(t, err)
	var payload map[string]any
	require.NoError(t, json.Unmarshal(raw, &payload))
	return payload
}

func TestRoutes_TheRequestSchemaAcceptsWhatClaudeCodeSends(t *testing.T) {
	for route, fixture := range map[string]string{
		"/hooks/session-start":      "SessionStart",
		"/hooks/stop":               "Stop",
		"/hooks/user-prompt-submit": "UserPromptSubmit",
	} {
		t.Run(fixture, func(t *testing.T) {
			api, _ := newRoutes(t)
			payload := claudePayload(t, fixture)
			require.NotEmpty(t, payload["session_id"])

			resp := api.Post(route, payload)

			assert.Equal(t, http.StatusOK, resp.Code, resp.Body.String())
		})
	}
}

func TestRoutes_ASessionStartPayloadFromClaudeCodeBindsItsSession(t *testing.T) {
	api, h := newRoutes(t)

	resp := api.Post("/hooks/session-start", claudePayload(t, "SessionStart"))

	require.Equal(t, http.StatusOK, resp.Code, resp.Body.String())
	payload := claudePayload(t, "SessionStart")
	assert.Equal(t, [][2]string{{payload["session_id"].(string), payload["cwd"].(string)}}, h.sessions.started)
}
