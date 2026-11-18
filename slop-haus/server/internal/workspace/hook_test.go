package workspace_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/workspace"
)

// The Stop hook is the thing that makes an agent a fleet member rather
// than a one-shot: it is the only mechanism that sends one back to work.
// These run the actual installed script against a real server, because a
// shell script that is only read and never executed is a guess.
//
// Its whole contract is an exit code:
//
//	2 -- go back to work, with stderr as the reason
//	0 -- stop
//
// and anything that goes wrong has to mean 0, or a server outage wedges
// every agent at once.

// stringWriter collects a process's stderr, which is the hook's only way
// of explaining itself.
type stringWriter struct{ text string }

func (w *stringWriter) Write(p []byte) (int, error) {
	w.text += string(p)
	return len(p), nil
}

type hookRun struct {
	exitCode int
	stderr   string
}

// runHook installs the script the way setup-workspace does and runs it,
// so what is tested is the copy a workspace actually gets.
//
// token, if non-empty, is seeded as this workspace's already-valid, cached
// OAuth access token -- there is no environment variable to hand it
// through any more, only .slop-haus/credentials.json, which token.sh reads
// straight from disk. An empty token seeds nothing at all, which is what a
// workspace that never finished signing in looks like.
func runHook(t *testing.T, server, token, payload string) hookRun {
	t.Helper()
	return runHookWithPath(t, server, token, payload, os.Getenv("PATH"))
}

func runHookWithPath(t *testing.T, server, token, payload, path string) hookRun {
	t.Helper()
	return runHookWithEnv(t, server, token, payload, "PATH="+path)
}

// hookEnv is this process's environment (which TestMain has already
// scrubbed of ambient SLOP_HAUS_* run variables) plus env.
//
// The hook's restart retries are off unless env turns them on (a later
// entry wins), or every unreachable-server test would take their full
// length to prove it fails open.
func hookEnv(env ...string) []string {
	out := append([]string{"SLOP_HAUS_STOP_RETRY_FOR=0"}, os.Environ()...)
	return append(out, env...)
}

// runHookWithEnv is runHook with extra NAME=value environment entries.
func runHookWithEnv(t *testing.T, server, token, payload string, env ...string) hookRun {
	t.Helper()
	return runHookInDir(t, t.TempDir(), server, token, payload, env...)
}

// runHookInDir installs a workspace into dir -- which may already hold
// repos the test laid out -- and runs its Stop hook.
func runHookInDir(t *testing.T, dir, server, token, payload string, env ...string) hookRun {
	t.Helper()

	script := install(t, dir, workspace.Params{AgentName: "builder-1", Server: server})
	if token != "" {
		seedValidCredentials(t, dir, token)
	}

	cmd := exec.Command(script, "-server="+server)
	cmd.Stdin = strings.NewReader(payload)
	cmd.Env = hookEnv(env...)

	var stderr stringWriter
	cmd.Stderr = &stderr
	cmd.Stdout = io.Discard

	return hookRun{exitCode: exitCodeOf(t, cmd.Run()), stderr: stderr.text}
}

// seedValidCredentials writes .slop-haus/credentials.json as token.sh
// would after a successful login or refresh: an access token that won't
// expire for the lifetime of this test, so cmd_get returns it straight
// from disk without needing a working OAuth server behind it.
func seedValidCredentials(t *testing.T, dir, accessToken string) {
	t.Helper()
	path := filepath.Join(dir, ".slop-haus", "credentials.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	body := fmt.Sprintf(`{"access_token":%q,"refresh_token":"unused-refresh-token","expires_at":%d}`,
		accessToken, time.Now().Add(time.Hour).Unix())
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
}

// seedNearExpiryCredentials seeds credentials due to expire in under a
// minute -- the point at which token.sh's own cmd_get decides it needs to
// refresh before handing one back.
func seedNearExpiryCredentials(t *testing.T, dir, accessToken, refreshToken string) {
	t.Helper()
	path := filepath.Join(dir, ".slop-haus", "credentials.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	body := fmt.Sprintf(`{"access_token":%q,"refresh_token":%q,"expires_at":%d}`,
		accessToken, refreshToken, time.Now().Add(30*time.Second).Unix())
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
}

// curlRecorder puts a curl on PATH that writes its arguments to a file
// before handing over to the real one. A bearer token in curl's arguments
// can be read by any process on the machine (/proc/<pid>/cmdline, ps), so
// the hooks hand it over on stdin instead; this is what proves they do.
type curlRecorder struct {
	bin, argsFile string
}

func newCurlRecorder(t *testing.T) *curlRecorder {
	t.Helper()
	real, err := exec.LookPath("curl")
	require.NoError(t, err)

	rec := &curlRecorder{bin: t.TempDir()}
	rec.argsFile = filepath.Join(rec.bin, "curl.args")
	stub := fmt.Sprintf("#!/usr/bin/env bash\nprintf '%%s\\n' \"$@\" >> %q\nexec %q \"$@\"\n", rec.argsFile, real)
	require.NoError(t, os.WriteFile(filepath.Join(rec.bin, "curl"), []byte(stub), 0o755))
	return rec
}

// path is a PATH that finds the recorder ahead of the real curl.
func (r *curlRecorder) path() string {
	return r.bin + string(os.PathListSeparator) + os.Getenv("PATH")
}

// assertTokenNotInArgs checks curl was run calls times for url -- so the
// recorder was on the path the hook used -- and that token was nowhere in
// the arguments of any of them.
func (r *curlRecorder) assertTokenNotInArgs(t *testing.T, token, url string, calls int) {
	t.Helper()
	args, err := os.ReadFile(r.argsFile)
	require.NoError(t, err, "curl was never run through the recorder")
	ran := 0
	for _, arg := range strings.Split(string(args), "\n") {
		if arg == url {
			ran++
		}
	}
	assert.Equal(t, calls, ran)
	assert.NotContains(t, string(args), token)
}

func exitCodeOf(t *testing.T, err error) int {
	t.Helper()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode()
	}
	require.NoError(t, err)
	return 0
}

// pathWithoutPython builds a PATH holding just the tools the fallback
// needs, so `command -v python3` finds nothing and the script takes the
// branch a machine without python3 would.
func pathWithoutPython(t *testing.T) string {
	t.Helper()

	bin := t.TempDir()
	for _, tool := range []string{
		"bash", "curl", "grep", "sed", "head", "cat", "mktemp", "rm",
		// token.sh's own fallback path needs these too.
		"date", "dirname", "mv", "chmod", "mkdir",
	} {
		real, err := exec.LookPath(tool)
		require.NoError(t, err, "%s is needed by the fallback path", tool)
		require.NoError(t, os.Symlink(real, filepath.Join(bin, tool)))
	}
	_, err := exec.LookPath("python3")
	require.NoError(t, err, "this test is only meaningful where python3 normally exists")

	return bin
}

func TestHookScript_BlockSendsTheAgentBackToWork(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"hookSpecificOutput":{"hookEventName":"Stop","decision":"block","reason":"Ticket #16 is still yours."}}`)
	}))
	defer server.Close()

	run := runHook(t, server.URL, "agent-token", claudePayload(t, "Stop"))

	assert.Equal(t, 2, run.exitCode, "only exit 2 sends an agent back to work")
	assert.Contains(t, run.stderr, "Ticket #16 is still yours.",
		"stderr is what Claude Code shows as the reason")
}

func TestHookScript_AllowLetsItStop(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"hookSpecificOutput":{"hookEventName":"Stop","decision":"allow"}}`)
	}))
	defer server.Close()

	run := runHook(t, server.URL, "agent-token", claudePayload(t, "Stop_after_block"))

	assert.Equal(t, 0, run.exitCode)
}

// A block with nothing to say still has to block. Claude Code shows
// stderr, so silence there would send the agent back with no explanation.
func TestHookScript_BlockWithoutAReasonStillExplainsItself(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"hookSpecificOutput":{"decision":"block"}}`)
	}))
	defer server.Close()

	run := runHook(t, server.URL, "agent-token", "{}")

	assert.Equal(t, 2, run.exitCode)
	assert.NotEmpty(t, run.stderr)
}

// systemMessage is the older shape of the same answer.
func TestHookScript_FallsBackToSystemMessageForTheReason(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"hookSpecificOutput":{"decision":"block"},"systemMessage":"chat is waiting on you"}`)
	}))
	defer server.Close()

	run := runHook(t, server.URL, "agent-token", "{}")

	assert.Equal(t, 2, run.exitCode)
	assert.Contains(t, run.stderr, "chat is waiting on you")
}

// The payload is Claude Code's, not ours, and it is forwarded whole --
// the server reads fields this script has never heard of.
func TestHookScript_ForwardsThePayloadAndTheToken(t *testing.T) {
	var (
		gotBody  string
		gotAuth  string
		gotType  string
		gotPath  string
		gotVerb  string
		received = make(chan struct{}, 1)
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotBody, gotAuth = string(body), r.Header.Get("Authorization")
		gotType, gotPath, gotVerb = r.Header.Get("Content-Type"), r.URL.Path, r.Method
		received <- struct{}{}
		_, _ = io.WriteString(w, `{"hookSpecificOutput":{"decision":"allow"}}`)
	}))
	defer server.Close()

	payload := claudePayload(t, "Stop")
	run := runHook(t, server.URL, "agent-token", payload)
	require.Equal(t, 0, run.exitCode)
	<-received

	assert.JSONEq(t, payload, withoutSlopHaus(t, gotBody), "the payload itself is forwarded untouched")
	assert.Equal(t, "Bearer agent-token", gotAuth)
	assert.Equal(t, "application/json", gotType)
	assert.Equal(t, "/hooks/stop", gotPath)
	assert.Equal(t, http.MethodPost, gotVerb)
}

// The retry after a restarting server's 502 is a curl call of its own, so
// it is covered too.
func TestHookScript_KeepsTheTokenOutOfCurlsArguments(t *testing.T) {
	var calls atomic.Int32
	gotAuth := make(chan string, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth <- r.Header.Get("Authorization")
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		_, _ = io.WriteString(w, `{"hookSpecificOutput":{"decision":"allow"}}`)
	}))
	defer server.Close()

	curl := newCurlRecorder(t)
	run := runHookWithEnv(t, server.URL, "agent-token", `{"session_id":"abc"}`,
		"PATH="+curl.path(), "SLOP_HAUS_STOP_RETRY_FOR=30", "SLOP_HAUS_STOP_RETRY_EVERY=0.05")
	require.Equal(t, 0, run.exitCode)

	assert.Equal(t, "Bearer agent-token", <-gotAuth)
	assert.Equal(t, "Bearer agent-token", <-gotAuth)
	curl.assertTokenNotInArgs(t, "agent-token", server.URL+"/hooks/stop", 2)
}

// Fail open. A server that cannot be reached must let the agent stop:
// an outage should idle the fleet, not freeze it.
func TestHookScript_UnreachableServerLetsItStop(t *testing.T) {
	// Started and immediately closed, so the port is closed rather than
	// merely slow -- a timeout would make this test take its full ten
	// seconds to prove the same thing.
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := server.URL
	server.Close()

	run := runHook(t, url, "agent-token", "{}")

	assert.Equal(t, 0, run.exitCode)
	assert.Contains(t, run.stderr, "slop-haus")
}

// A restarting server answers 502-504 through the proxy in front of it,
// and every idle agent's stop lands in that window. Riding it out is what
// keeps a deploy from putting the idle fleet to sleep.
func TestHookScript_RidesOutARestartingServer(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) <= 2 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"hookSpecificOutput":{"decision":"block","reason":"call checkin"}}`)
	}))
	defer server.Close()

	run := runHookWithEnv(t, server.URL, "agent-token", "{}",
		"PATH="+os.Getenv("PATH"), "SLOP_HAUS_STOP_RETRY_FOR=30", "SLOP_HAUS_STOP_RETRY_EVERY=0.05")

	assert.Equal(t, 2, run.exitCode, "the answer after the restart is the one acted on")
	assert.Contains(t, run.stderr, "call checkin")
	assert.EqualValues(t, 3, calls.Load())
}

// With no knobs set -- as in every real workspace -- a restart is ridden
// out too. Empty counts as unset, so this runs on the script's own defaults
// (one retry, about three seconds).
func TestHookScript_RidesOutARestartByDefault(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		_, _ = io.WriteString(w, `{"hookSpecificOutput":{"decision":"block","reason":"call checkin"}}`)
	}))
	defer server.Close()

	run := runHookWithEnv(t, server.URL, "agent-token", "{}", "PATH="+os.Getenv("PATH"), "SLOP_HAUS_STOP_RETRY_FOR=")

	assert.Equal(t, 2, run.exitCode)
	assert.EqualValues(t, 2, calls.Load())
}

// A server mid-restart may not answer HTTP at all: the connection is
// refused, or dropped before a response. That is retried the same way.
func TestHookScript_RidesOutAConnectionThatFails(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = listener.Close() }()
	var dropped atomic.Int32
	go func() {
		// The first two connections are dropped without a response, which
		// curl reports as a failure of its own rather than an HTTP status.
		for dropped.Load() < 2 {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			dropped.Add(1)
			_ = conn.Close()
		}
		_ = http.Serve(listener, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, `{"hookSpecificOutput":{"decision":"block","reason":"call checkin"}}`)
		}))
	}()

	run := runHookWithEnv(t, "http://"+listener.Addr().String(), "agent-token", "{}",
		"PATH="+os.Getenv("PATH"), "SLOP_HAUS_STOP_RETRY_FOR=30", "SLOP_HAUS_STOP_RETRY_EVERY=0.05")

	assert.Equal(t, 2, run.exitCode, run.stderr)
	assert.EqualValues(t, 2, dropped.Load())
}

// Riding out a restart is bounded: a server that never comes back still
// lets the agent stop.
func TestHookScript_GivesUpOnAServerThatStaysDown(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	run := runHookWithEnv(t, server.URL, "agent-token", "{}",
		"PATH="+os.Getenv("PATH"), "SLOP_HAUS_STOP_RETRY_FOR=30", "SLOP_HAUS_STOP_RETRY_ATTEMPTS=3", "SLOP_HAUS_STOP_RETRY_EVERY=0.01")

	assert.Equal(t, 0, run.exitCode)
	assert.Contains(t, run.stderr, "503")
	assert.EqualValues(t, 3, calls.Load(), "it retried, then gave up")
}

// Only a restart's answers are retried. A server that is up and failing
// says so at once -- retrying would only make every stop slow.
func TestHookScript_DoesNotRetryAServerError(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	run := runHookWithEnv(t, server.URL, "agent-token", "{}",
		"PATH="+os.Getenv("PATH"), "SLOP_HAUS_STOP_RETRY_FOR=30", "SLOP_HAUS_STOP_RETRY_EVERY=0.05")

	assert.Equal(t, 0, run.exitCode)
	assert.EqualValues(t, 1, calls.Load())
}

func TestHookScript_ServerErrorLetsItStop(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, "boom")
	}))
	defer server.Close()

	run := runHook(t, server.URL, "agent-token", "{}")

	assert.Equal(t, 0, run.exitCode)
	assert.Contains(t, run.stderr, "500")
}

// A 422 was the failure that kept the loop from ever running (the payload
// carries more than the server used to accept). It must not wedge an
// agent either, and it must say what happened.
func TestHookScript_RejectedPayloadLetsItStopAndSaysSo(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = io.WriteString(w, `{"detail":"unexpected property"}`)
	}))
	defer server.Close()

	run := runHook(t, server.URL, "agent-token", "{}")

	assert.Equal(t, 0, run.exitCode)
	assert.Contains(t, run.stderr, "422")
	assert.Contains(t, run.stderr, "unexpected property", "the body is what says which field")
}

func TestHookScript_MissingTokenLetsItStop(t *testing.T) {
	run := runHook(t, "http://127.0.0.1:1", "", "{}")

	assert.Equal(t, 0, run.exitCode)
	assert.Contains(t, run.stderr, "token")
}

// Nonsense on the wire is not a reason to trap an agent.
func TestHookScript_UnparseableResponseLetsItStop(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "not json at all")
	}))
	defer server.Close()

	run := runHook(t, server.URL, "agent-token", "{}")

	assert.Equal(t, 0, run.exitCode)
}

// The fallback for a machine with no python3 is the one path nothing else
// exercises, and it is the path a plain laptop takes. An untested branch
// in the hook that decides whether agents keep working is not a branch,
// it is a hope.
func TestHookScript_ParsesTheAnswerWithoutPython(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"hookSpecificOutput":{"decision":"block","reason":"still holding #4"}}`)
	}))
	defer server.Close()

	run := runHookWithPath(t, server.URL, "agent-token", "{}", pathWithoutPython(t))

	assert.Equal(t, 2, run.exitCode, "a machine without python3 still has to block")
	assert.Contains(t, run.stderr, "still holding #4")
}

func TestHookScript_WithoutPythonStillLetsAnAllowStop(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"hookSpecificOutput":{"decision":"allow"}}`)
	}))
	defer server.Close()

	run := runHookWithPath(t, server.URL, "agent-token", "{}", pathWithoutPython(t))

	assert.Equal(t, 0, run.exitCode)
}

// settings.json passes the url as the -server argument, but the script
// also accepts SLOP_HAUS_URL, which is what agent_workspace.sh exports.
func TestHookScript_TakesTheServerFromTheEnvironmentToo(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"hookSpecificOutput":{"decision":"block","reason":"from the environment"}}`)
	}))
	defer server.Close()

	dir := t.TempDir()
	path := install(t, dir, workspace.Params{AgentName: "builder-1", Server: server.URL})
	seedValidCredentials(t, dir, "agent-token")

	cmd := exec.Command(path)
	cmd.Stdin = strings.NewReader("{}")
	cmd.Env = append(os.Environ(), "SLOP_HAUS_URL="+server.URL)
	var stderr stringWriter
	cmd.Stderr = &stderr
	cmd.Stdout = io.Discard

	assert.Equal(t, 2, exitCodeOf(t, cmd.Run()))
	assert.Contains(t, stderr.text, "from the environment")
}

// A trailing slash on the url must not produce //hooks/stop.
func TestHookScript_ToleratesATrailingSlashOnTheServer(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = io.WriteString(w, `{"hookSpecificOutput":{"decision":"allow"}}`)
	}))
	defer server.Close()

	run := runHook(t, server.URL+"/", "agent-token", "{}")

	assert.Equal(t, 0, run.exitCode)
	assert.Equal(t, "/hooks/stop", gotPath)
}

// The hook resolves its own server from -server, which wins over any
// SLOP_HAUS_URL a human has set in their shell (settings.json always bakes
// in an absolute -server=). token.sh, run as a subprocess, has to be told
// that same address explicitly, or its own independent resolution
// (SLOP_HAUS_URL first) could send a refresh request -- and thus this
// workspace's single-use refresh token -- to a completely different
// server than the one the hook itself is about to call.
func TestHookScript_RefreshesAgainstTheHooksOwnServerNotTheAmbientOne(t *testing.T) {
	var refreshHits, stopHits int32
	var gotAuth string
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/token", func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&refreshHits, 1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "AT-refreshed", "refresh_token": "RT-refreshed",
			"token_type": "Bearer", "expires_in": 900,
		})
	})
	mux.HandleFunc("/hooks/stop", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&stopHits, 1)
		gotAuth = r.Header.Get("Authorization")
		_, _ = io.WriteString(w, `{"hookSpecificOutput":{"decision":"allow"}}`)
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	dir := t.TempDir()
	script := install(t, dir, workspace.Params{AgentName: "builder-1", Server: server.URL})
	seedNearExpiryCredentials(t, dir, "AT-old", "RT-old")

	cmd := exec.Command(script, "-server="+server.URL)
	cmd.Stdin = strings.NewReader("{}")
	// The environment a human's shell might carry: an address that would
	// fail if token.sh ever consulted it instead of the -server value
	// above, which is what settings.json actually configured this hook
	// with.
	cmd.Env = append(os.Environ(), "SLOP_HAUS_URL=http://127.0.0.1:1")

	var stderr stringWriter
	cmd.Stderr = &stderr
	cmd.Stdout = io.Discard

	assert.Equal(t, 0, exitCodeOf(t, cmd.Run()), "stderr: %s", stderr.text)
	assert.EqualValues(t, 1, atomic.LoadInt32(&refreshHits), "the refresh must reach this hook's own server")
	assert.EqualValues(t, 1, atomic.LoadInt32(&stopHits))
	assert.Equal(t, "Bearer AT-refreshed", gotAuth)
}

// curl verifies the server it talks to the ordinary way, against the
// system's trusted roots. An untrusted self-signed certificate must
// therefore fail the connection -- which has to mean fail open, the same
// as any other unreachable server, never a block on an answer this hook
// can't actually be sure came from the real server.
func TestHookScript_UntrustedTLSServer_FailsOpen(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"hookSpecificOutput":{"decision":"block","reason":"should never be seen"}}`)
	}))
	defer server.Close()

	run := runHook(t, server.URL, "agent-token", "{}")

	assert.Equal(t, 0, run.exitCode, "an untrusted certificate must fail open, not block")
	assert.NotContains(t, run.stderr, "should never be seen")
}

// A session the conductor's host started is not a worker's: the guard is what
// skips it, so the same server that blocks the worker's Stop answers nothing
// here, and is never asked.
func TestHookScript_SkipsASessionTheConductorHostStarted(t *testing.T) {
	var asked atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked.Add(1)
		_, _ = io.WriteString(w, `{"hookSpecificOutput":{"hookEventName":"Stop","decision":"block","reason":"go claim work"}}`)
	}))
	defer server.Close()

	worker := runHook(t, server.URL, "agent-token", `{"session_id":"abc"}`)
	require.Equal(t, 2, worker.exitCode, "without the variable the same server blocks")
	before := asked.Load()
	hosted := runHookWithEnv(t, server.URL, "agent-token", `{"session_id":"abc"}`, "SLOP_HAUS_HOST_SESSION=1")

	assert.Equal(t, 0, hosted.exitCode)
	assert.Empty(t, hosted.stderr)
	assert.Equal(t, before, asked.Load(), "the server was not asked")
}
