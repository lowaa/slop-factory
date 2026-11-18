package workspace_test

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/workspace"
)

// The usage hook runs async beside the Stop hook and on every
// SubagentStop. Its contract is the opposite of the Stop hook's: it decides
// nothing, so whatever happens it exits 0 and prints nothing on stdout.
// These run the installed script against a server that records what
// arrives.

type usageRequest struct {
	auth string
	body map[string]any
	raw  string
}

func captureUsage(t *testing.T, status int) (*httptest.Server, <-chan usageRequest) {
	t.Helper()
	got := make(chan usageRequest, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/hooks/usage" || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		got <- usageRequest{auth: r.Header.Get("Authorization"), body: body, raw: string(raw)}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, `{"scope":"ticket","ticket_id":7}`)
	}))
	t.Cleanup(server.Close)
	return server, got
}

type usageRun struct {
	exitCode       int
	stdout, stderr string
}

// runUsageHook installs the workspace, seeds token unless it is empty, and
// runs the usage hook with payload. env is extra NAME=value entries.
func runUsageHook(t *testing.T, server, token, payload string, env ...string) usageRun {
	t.Helper()
	dir := t.TempDir()
	installForUsage(t, dir, server, token)
	return runUsageHookIn(t, dir, server, payload, env...)
}

func installForUsage(t *testing.T, dir, server, token string) {
	t.Helper()
	install(t, dir, workspace.Params{AgentName: "builder-1", Server: server})
	if token != "" {
		seedValidCredentials(t, dir, token)
	}
}

func runUsageHookIn(t *testing.T, dir, server, payload string, env ...string) usageRun {
	t.Helper()
	cmd := exec.Command(workspace.UsageHookPathIn(dir), "-server="+server)
	cmd.Stdin = strings.NewReader(payload)
	// No waiting for the transcript to settle: these write it whole first.
	cmd.Env = hookEnv(append([]string{"SLOP_HAUS_USAGE_SETTLE_SECONDS=0"}, env...)...)
	var stdout, stderr stringWriter
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	code := exitCodeOf(t, cmd.Run())
	return usageRun{exitCode: code, stdout: stdout.text, stderr: stderr.text}
}

const secretInCommand = "hunter2-in-a-command-line"

// A subagent's transcript as Claude Code writes it: one line per
// content block, repeating the usage, and a bash call whose command line
// holds a secret.
func subagentTranscript(t *testing.T) string {
	t.Helper()
	return writeTranscript(t,
		`{"type":"assistant","timestamp":"2026-09-30T10:00:00Z","message":{"id":"msg_1","model":"claude-opus-5-5","content":[{"type":"text","text":"Checking."}],"usage":{"input_tokens":3,"output_tokens":5,"cache_read_input_tokens":100,"cache_creation_input_tokens":10}}}`,
		`{"type":"assistant","timestamp":"2026-09-30T10:00:01Z","message":{"id":"msg_1","model":"claude-opus-5-5","content":[{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"curl -u admin:`+secretInCommand+` https://x | jq .","description":"query"}}],"usage":{"input_tokens":3,"output_tokens":50,"cache_read_input_tokens":100,"cache_creation_input_tokens":10}}}`,
		`{"type":"user","timestamp":"2026-09-30T10:00:03Z","message":{"content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"{}","is_error":false}]}}`,
		`{"type":"assistant","timestamp":"2026-09-30T10:00:04Z","message":{"id":"msg_2","model":"claude-opus-5-5","content":[{"type":"text","text":"Done."}],"usage":{"input_tokens":4,"output_tokens":6,"cache_read_input_tokens":200,"cache_creation_input_tokens":0}}}`,
	)
}

func subagentStopPayload(transcript string) string {
	return `{"hook_event_name":"SubagentStop","session_id":"sess-1","agent_id":"a1b2","agent_type":"reviewer",` +
		`"transcript_path":"/nonexistent/main.jsonl","agent_transcript_path":` + jsonString(transcript) + `,"stop_hook_active":false}`
}

func TestUsageHook_ReportsASubagentsTranscriptTotals(t *testing.T) {
	server, got := captureUsage(t, http.StatusOK)
	curl := newCurlRecorder(t)

	run := runUsageHook(t, server.URL, "agent-token", subagentStopPayload(subagentTranscript(t)), "PATH="+curl.path())
	require.Equal(t, 0, run.exitCode, run.stderr)
	assert.Empty(t, run.stdout, "an async hook's output goes nowhere useful; it prints none")

	req := <-got
	assert.Equal(t, "Bearer agent-token", req.auth)
	curl.assertTokenNotInArgs(t, "agent-token", server.URL+"/hooks/usage", 1)
	assert.Equal(t, map[string]any{
		"transcript_key": "sess-1:a1b2",
		"harness":        "claude",
		"session_id":     "sess-1",
		"scope":          "subagent:reviewer",
		"records": []any{map[string]any{
			"schema_version":     float64(1),
			"provider":           "anthropic",
			"model":              "claude-opus-5-5",
			"harness":            "claude",
			"billing_mode":       "subscription",
			"requests":           float64(2),
			"input_tokens":       float64(7),
			"output_tokens":      float64(56),
			"cache_read_tokens":  float64(300),
			"cache_write_tokens": float64(10),
			"reasoning_tokens":   float64(0),
			"started_at":         "2026-09-30T10:00:00.000Z",
			"ended_at":           "2026-09-30T10:00:04.000Z",
			"tools": []any{
				map[string]any{"tool": "Bash", "command": "curl", "calls": float64(1), "errors": float64(0), "duration_ms": float64(0), "result_bytes": float64(0)},
				map[string]any{"tool": "Bash", "command": "jq", "calls": float64(1), "errors": float64(0), "duration_ms": float64(2000), "result_bytes": float64(2)},
			},
		}},
	}, req.body)
	// What actually went over the wire, not just what it decoded to.
	assert.NotContains(t, req.raw, secretInCommand)
	assert.NotContains(t, req.raw, "admin")
	assert.NotContains(t, req.raw, "https://x")
}

func TestUsageHook_ReportsTheMainTranscriptOnStop(t *testing.T) {
	server, got := captureUsage(t, http.StatusOK)
	transcript := writeTranscript(t,
		`{"type":"assistant","timestamp":"2026-09-30T10:00:00Z","message":{"id":"m1","model":"claude-opus-5-5","content":[],"usage":{"input_tokens":1,"output_tokens":2}}}`,
	)
	payload := `{"hook_event_name":"Stop","session_id":"sess-1","transcript_path":` + jsonString(transcript) + `}`

	run := runUsageHook(t, server.URL, "agent-token", payload)
	require.Equal(t, 0, run.exitCode, run.stderr)

	req := <-got
	assert.Equal(t, "sess-1", req.body["transcript_key"])
	assert.Equal(t, "main", req.body["scope"])
}

// Nothing it can go wrong on may fail the hook, and nothing may make it
// hang: it runs on every stop.
func TestUsageHook_FailsOpen(t *testing.T) {
	transcript := subagentTranscript(t)

	t.Run("server error", func(t *testing.T) {
		server, got := captureUsage(t, http.StatusUnprocessableEntity)
		run := runUsageHook(t, server.URL, "agent-token", subagentStopPayload(transcript))
		<-got
		assert.Equal(t, 0, run.exitCode)
		assert.Contains(t, run.stderr, "HTTP 422")
	})

	t.Run("unreachable server", func(t *testing.T) {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		url := "http://" + listener.Addr().String()
		require.NoError(t, listener.Close())

		run := runUsageHook(t, url, "agent-token", subagentStopPayload(transcript))
		assert.Equal(t, 0, run.exitCode)
		assert.Contains(t, run.stderr, "unreachable")
	})

	for name, tc := range map[string]struct {
		token, payload, stderr string
	}{
		"no token":            {"", subagentStopPayload(transcript), "no valid token"},
		"malformed payload":   {"agent-token", "{not json", "unreadable hook payload"},
		"missing transcript":  {"agent-token", subagentStopPayload("/nonexistent/agent.jsonl"), "cannot read"},
		"garbage transcript":  {"agent-token", subagentStopPayload(writeTranscript(t, "\x00\xff not json", `{"type":`)), "no model calls"},
		"other hook event":    {"agent-token", `{"hook_event_name":"SessionStart","session_id":"s"}`, "not a Stop or SubagentStop"},
		"directory not file":  {"agent-token", subagentStopPayload(t.TempDir()), "cannot read"},
		"no agent id at all":  {"agent-token", `{"hook_event_name":"SubagentStop","session_id":"s","agent_transcript_path":` + jsonString(transcript) + `}`, "no usable agent_id"},
		"empty payload input": {"agent-token", "", "unreadable hook payload"},
	} {
		t.Run(name, func(t *testing.T) {
			server, got := captureUsage(t, http.StatusOK)
			run := runUsageHook(t, server.URL, tc.token, tc.payload)
			assert.Equal(t, 0, run.exitCode)
			assert.Empty(t, run.stdout)
			assert.Contains(t, run.stderr, tc.stderr)
			assert.Empty(t, got, "nothing is sent")
		})
	}
}

// Without python3 there is no report, and no failure either.
func TestUsageHook_WithoutPythonReportsNothing(t *testing.T) {
	server, got := captureUsage(t, http.StatusOK)
	run := runUsageHook(t, server.URL, "agent-token", subagentStopPayload(subagentTranscript(t)),
		"PATH="+pathWithoutPython(t))
	assert.Equal(t, 0, run.exitCode)
	assert.Contains(t, run.stderr, "no python3")
	assert.Empty(t, got)
}

// Reports run one at a time: async hooks overlap, and an older report
// posted after a newer one would replace it. While another holds the lock
// this one waits -- here, not at all -- and sends nothing.
func TestUsageHook_WaitsForTheReportBeforeIt(t *testing.T) {
	server, got := captureUsage(t, http.StatusOK)
	dir := t.TempDir()
	installForUsage(t, dir, server.URL, "agent-token")

	lock, err := os.OpenFile(filepath.Join(dir, ".slop-haus", "usage.lock"), os.O_CREATE|os.O_WRONLY, 0o644)
	require.NoError(t, err)
	t.Cleanup(func() { _ = lock.Close() })
	require.NoError(t, syscall.Flock(int(lock.Fd()), syscall.LOCK_EX))

	run := runUsageHookIn(t, dir, server.URL, subagentStopPayload(subagentTranscript(t)), "SLOP_HAUS_USAGE_LOCK_WAIT=0")
	assert.Equal(t, 0, run.exitCode)
	assert.Contains(t, run.stderr, "held the lock")
	assert.Empty(t, got)

	require.NoError(t, syscall.Flock(int(lock.Fd()), syscall.LOCK_UN))
	run = runUsageHookIn(t, dir, server.URL, subagentStopPayload(subagentTranscript(t)), "SLOP_HAUS_USAGE_LOCK_WAIT=0")
	require.Equal(t, 0, run.exitCode, run.stderr)
	assert.Equal(t, "sess-1:a1b2", (<-got).body["transcript_key"])
}

// The host reports a conversation's usage itself, from each turn's result; the
// hook would count the same turns again, as overhead.
func TestUsageHook_SkipsASessionTheConductorHostStarted(t *testing.T) {
	server, got := captureUsage(t, http.StatusOK)
	transcript := subagentTranscript(t)

	worker := runUsageHook(t, server.URL, "agent-token", subagentStopPayload(transcript))
	require.Equal(t, 0, worker.exitCode)
	select {
	case <-got:
	default:
		t.Fatal("without the variable the hook reports")
	}
	hosted := runUsageHook(t, server.URL, "agent-token", subagentStopPayload(transcript), "SLOP_HAUS_HOST_SESSION=1")

	assert.Equal(t, 0, hosted.exitCode)
	assert.Empty(t, hosted.stdout)
	select {
	case r := <-got:
		t.Fatalf("a hosted session's usage was reported by the hook: %s", r.raw)
	default:
	}
}
