package workspace_test

import (
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

// The SessionStart hook used to be an http hook, since all it ever does is
// hand back a JSON body for Claude Code to read as hookSpecificOutput. It
// became a command hook only because an http hook's Authorization header
// can only ever be a fixed string, and this workspace has none any more --
// see workspace.go's settingsFor comment. These tests are about proving
// that move didn't change the contract: on success the server's response
// body comes back unchanged, and on any failure there is silence and exit
// 0, never a block.

// sessionStartRun installs the workspace's files the way setup does and
// runs the SessionStart hook directly, seeding credentials.json first
// unless token is empty. env is extra NAME=value entries, as for hookEnv.
func sessionStartRun(t *testing.T, server, token, payload string, env ...string) (stdout, stderr string, exitCode int) {
	t.Helper()

	dir := t.TempDir()
	install(t, dir, workspace.Params{AgentName: "builder-1", Server: server})
	if token != "" {
		seedValidCredentials(t, dir, token)
	}

	cmd := exec.Command(workspace.SessionStartHookPathIn(dir), "-server="+server)
	cmd.Stdin = strings.NewReader(payload)
	cmd.Env = hookEnv(env...)

	var outBuf, errBuf stringWriter
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf

	// Run first, then read the buffers: in one return statement Go does not
	// promise to read outBuf.text after calling cmd.Run().
	code := exitCodeOf(t, cmd.Run())
	return outBuf.text, errBuf.text, code
}

func TestSessionStartHook_PrintsTheServersResponseBodyOnSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"hookSpecificOutput":{"hookEventName":"SessionStart","additionalContext":"you are builder-1"}}`)
	}))
	defer server.Close()

	stdout, _, exitCode := sessionStartRun(t, server.URL, "agent-token", claudePayload(t, "SessionStart"))

	assert.Equal(t, 0, exitCode)
	assert.JSONEq(t, `{"hookSpecificOutput":{"hookEventName":"SessionStart","additionalContext":"you are builder-1"}}`, stdout)
}

func TestSessionStartHook_ForwardsThePayloadAndAFreshToken(t *testing.T) {
	var (
		gotBody, gotAuth, gotPath string
		received                  = make(chan struct{}, 1)
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotBody, gotAuth, gotPath = string(body), r.Header.Get("Authorization"), r.URL.Path
		received <- struct{}{}
		_, _ = io.WriteString(w, `{"hookSpecificOutput":{"hookEventName":"SessionStart"}}`)
	}))
	defer server.Close()

	payload := claudePayload(t, "SessionStart")
	_, _, exitCode := sessionStartRun(t, server.URL, "agent-token", payload)
	require.Equal(t, 0, exitCode)
	<-received

	assert.JSONEq(t, payload, withoutSlopHaus(t, gotBody), "the payload itself is forwarded untouched")
	assert.Equal(t, "Bearer agent-token", gotAuth)
	assert.Equal(t, "/hooks/session-start", gotPath)
}

func TestSessionStartHook_KeepsTheTokenOutOfCurlsArguments(t *testing.T) {
	gotAuth := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth <- r.Header.Get("Authorization")
		_, _ = io.WriteString(w, `{"hookSpecificOutput":{"hookEventName":"SessionStart"}}`)
	}))
	defer server.Close()

	curl := newCurlRecorder(t)
	_, _, exitCode := sessionStartRun(t, server.URL, "agent-token", `{"session_id":"abc"}`, "PATH="+curl.path())
	require.Equal(t, 0, exitCode)

	assert.Equal(t, "Bearer agent-token", <-gotAuth)
	curl.assertTokenNotInArgs(t, "agent-token", server.URL+"/hooks/session-start", 1)
}

// Nothing here ever blocks: SessionStart only ever adds context, so every
// failure mode is silence on stdout and a clean exit 0, not an error
// Claude Code has to do anything about.
func TestSessionStartHook_FailsSilentlyWhenThereIsNoValidToken(t *testing.T) {
	stdout, _, exitCode := sessionStartRun(t, "http://127.0.0.1:1", "", "{}")

	assert.Equal(t, 0, exitCode)
	assert.Empty(t, stdout)
}

func TestSessionStartHook_FailsSilentlyWhenTheServerIsUnreachable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := server.URL
	server.Close()

	stdout, _, exitCode := sessionStartRun(t, url, "agent-token", "{}")

	assert.Equal(t, 0, exitCode)
	assert.Empty(t, stdout)
}

func TestSessionStartHook_FailsSilentlyOnANon200(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, "boom")
	}))
	defer server.Close()

	stdout, _, exitCode := sessionStartRun(t, server.URL, "agent-token", "{}")

	assert.Equal(t, 0, exitCode)
	assert.Empty(t, stdout)
}

func TestSessionStartHook_ToleratesATrailingSlashOnTheServer(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = io.WriteString(w, `{"hookSpecificOutput":{"hookEventName":"SessionStart"}}`)
	}))
	defer server.Close()

	_, _, exitCode := sessionStartRun(t, server.URL+"/", "agent-token", "{}")

	assert.Equal(t, 0, exitCode)
	assert.Equal(t, "/hooks/session-start", gotPath)
}

// curl verifies the server the ordinary way, against the system's trusted
// roots, so an untrusted self-signed certificate must fail silently, the
// same as any other unreachable server -- never surface a body this
// workspace can't actually be sure came from the real server.
func TestSessionStartHook_UntrustedTLSServer_FailsSilently(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"hookSpecificOutput":{"hookEventName":"SessionStart","additionalContext":"should never be seen"}}`)
	}))
	defer server.Close()

	stdout, _, exitCode := sessionStartRun(t, server.URL, "agent-token", "{}")

	assert.Equal(t, 0, exitCode)
	assert.Empty(t, stdout)
}

func TestSessionStartHook_SaysNothingToASessionTheConductorHostStarted(t *testing.T) {
	var asked atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked.Add(1)
		_, _ = io.WriteString(w, `{"hookSpecificOutput":{"hookEventName":"SessionStart","additionalContext":"you are builder-1"}}`)
	}))
	defer server.Close()

	stdout, _, exitCode := sessionStartRun(t, server.URL, "agent-token", `{"session_id":"abc"}`, "SLOP_HAUS_HOST_SESSION=1")

	assert.Equal(t, 0, exitCode)
	assert.Empty(t, stdout, "a worker's briefing is not for a conductor's conversation")
	assert.Zero(t, asked.Load(), "and the server is never asked for one")
}
