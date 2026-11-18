package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-review/internal/providers"
	"github.com/lowaa/slop-factory/slop-review/report"
	"github.com/lowaa/slop-factory/slop-review/report/providerconfig"
)

// capturedMessage and capturedRequest decode just enough of the raw
// OpenAI-compatible chat-completion request body (see
// internal/llm.OpenAIAPILLMProvider) for the end-to-end tests to assert on
// what a session actually sent, rather than trusting the fake server's
// canned responses alone -- a fake that answers from a request counter
// without inspecting the request would pass even if the session sent the
// wrong prompt, tools, or tool results.
type capturedMessage struct {
	Role       string `json:"role"`
	Content    string `json:"content"`
	ToolCallID string `json:"tool_call_id"`
	ToolCalls  []struct {
		ID       string `json:"id"`
		Function struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
	} `json:"tool_calls"`
}

type capturedRequest struct {
	Model    string            `json:"model"`
	Messages []capturedMessage `json:"messages"`
	Tools    []struct {
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	} `json:"tools"`
}

func (r capturedRequest) toolNames() []string {
	names := make([]string, len(r.Tools))
	for i, t := range r.Tools {
		names[i] = t.Function.Name
	}
	return names
}

func (r capturedRequest) toolResult(toolCallID string) (capturedMessage, bool) {
	for _, m := range r.Messages {
		if m.Role == "tool" && m.ToolCallID == toolCallID {
			return m, true
		}
	}
	return capturedMessage{}, false
}

// requestRecorder captures every request body an httptest handler sees,
// guarded by a mutex since httptest handlers run each request on its own
// goroutine (TestRun_AllPersonasEndToEnd reviews two personas whose
// sessions can be in flight at once).
type requestRecorder struct {
	mu       sync.Mutex
	requests []capturedRequest
}

func (r *requestRecorder) record(req *http.Request) capturedRequest {
	body, err := io.ReadAll(req.Body)
	if err != nil {
		panic(err) // httptest handler: no *testing.T to fail with here
	}
	var captured capturedRequest
	if err := json.Unmarshal(body, &captured); err != nil {
		panic(err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.requests = append(r.requests, captured)
	return captured
}

func (r *requestRecorder) all() []capturedRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]capturedRequest(nil), r.requests...)
}

func (r *requestRecorder) count() int {
	return len(r.all())
}

// TestRun_EndToEnd exercises the full stack -- config, git diff resolution,
// persona loading, the session loop, and the toolset -- against a real
// throwaway git repo and a fake OpenAI-compatible server, verifying the
// pieces built in internal/* actually wire together into a working review.
func TestRun_EndToEnd(t *testing.T) {
	repoDir := newTestRepo(t)
	personaDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(personaDir, "standard.md"), []byte("You are a reviewer."), 0o644))

	// Script: first turn reads the changed file, second turn submits.
	recorder := &requestRecorder{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorder.record(r)
		w.Header().Set("Content-Type", "application/json")
		turn := recorder.count()
		if turn == 1 {
			_, _ = w.Write([]byte(`{
				"id": "1", "object": "chat.completion", "created": 0, "model": "m",
				"choices": [{"index": 0, "finish_reason": "tool_calls", "message": {
					"role": "assistant", "content": "",
					"tool_calls": [{"id": "call-1", "type": "function", "function": {"name": "read_file", "arguments": "{\"path\":\"feature.go\"}"}}]
				}}]
			}`))
			return
		}
		_, _ = w.Write([]byte(`{
			"id": "2", "object": "chat.completion", "created": 0, "model": "m",
			"choices": [{"index": 0, "finish_reason": "tool_calls", "message": {
				"role": "assistant", "content": "",
				"tool_calls": [{"id": "call-2", "type": "function", "function": {
					"name": "submit_review",
					"arguments": "{\"verdict\":\"approve\",\"summary\":\"Looks fine.\",\"findings\":[]}"
				}}]
			}}]
		}`))
	}))
	defer server.Close()

	t.Setenv("SLOP_REVIEW_REPO_ROOT", repoDir)
	t.Setenv("SLOP_REVIEW_PERSONA_DIR", personaDir)
	t.Setenv("SLOP_REVIEW_BASE_URL", server.URL)
	t.Setenv("SLOP_REVIEW_PROVIDER", "openrouter")
	t.Setenv("OPENROUTER_API_KEY", "test-key")

	var stdout bytes.Buffer
	err := run(nil, &stdout, &bytes.Buffer{})
	require.NoError(t, err)

	var got output
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &got))

	assert.Equal(t, "approve", string(got.Review.Verdict))
	assert.Equal(t, "Looks fine.", got.Review.Summary)
	assert.Equal(t, "feature", got.Meta.Branch)
	assert.Contains(t, got.Markdown, "Approved")
	requests := recorder.all()
	require.Len(t, requests, 2, "session should have made exactly two round trips: one tool call, one submit")
	require.Len(t, got.Sessions, 1)
	assert.Equal(t, "standard", got.Sessions[0].Persona)

	// Request 1: the persona system prompt, a user prompt carrying the
	// diff, and the full navigation + reporting toolset on offer -- a fake
	// server that never looks at this could equally be backing a session
	// that got no persona, no diff, or no tools at all.
	first := requests[0]
	require.NotEmpty(t, first.Messages)
	assert.Equal(t, "system", first.Messages[0].Role)
	assert.Equal(t, "You are a reviewer.", personaPrompt(first.Messages[0].Content))
	assert.Contains(t, first.Messages[0].Content, guidanceHeading, "the shared guidance follows the persona")
	require.Len(t, first.Messages, 2)
	assert.Equal(t, "user", first.Messages[1].Role)
	assert.Contains(t, first.Messages[1].Content, "feature.go")
	assert.Contains(t, first.toolNames(), "read_file")
	assert.Contains(t, first.toolNames(), "get_diff")
	assert.Contains(t, first.toolNames(), "record_finding")
	assert.Contains(t, first.toolNames(), "submit_review")

	// Request 2: the read_file call's result must actually have been fed
	// back as the tool-role message answering call-1 -- not dropped, not
	// answered with something else -- or the model would be "reviewing"
	// blind on its second turn.
	second := requests[1]
	toolResult, ok := second.toolResult("call-1")
	require.True(t, ok, "request 2 must carry a tool-role message answering call-1")
	assert.Contains(t, toolResult.Content, "func main() {}")
}

// TestRun_AllPersonasEndToEnd runs every persona in the library through
// the orchestrator in one invocation, against the same fake server.
func TestRun_AllPersonasEndToEnd(t *testing.T) {
	repoDir := newTestRepo(t)
	personaDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(personaDir, "standard.md"), []byte("You are a reviewer."), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(personaDir, "security.md"), []byte("---\nchunking: whole\n---\nYou are a security reviewer."), 0o644))

	recorder := &requestRecorder{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorder.record(r)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id": "1", "object": "chat.completion", "created": 0, "model": "m",
			"choices": [{"index": 0, "finish_reason": "tool_calls", "message": {
				"role": "assistant", "content": "",
				"tool_calls": [{"id": "call-1", "type": "function", "function": {
					"name": "submit_review",
					"arguments": "{\"verdict\":\"approve_with_comments\",\"summary\":\"One nit.\",\"findings\":[{\"file\":\"feature.go\",\"line\":3,\"severity\":\"nit\",\"summary\":\"empty main\",\"detail\":\"d\"}]}"
				}}]
			}}]
		}`))
	}))
	defer server.Close()

	t.Setenv("SLOP_REVIEW_REPO_ROOT", repoDir)
	t.Setenv("SLOP_REVIEW_PERSONA_DIR", personaDir)
	t.Setenv("SLOP_REVIEW_BASE_URL", server.URL)
	t.Setenv("OPENROUTER_API_KEY", "test-key")
	t.Setenv("SLOP_REVIEW_PROVIDER", "openrouter") // the fake server speaks chat completions

	var stdout bytes.Buffer
	require.NoError(t, run([]string{"--persona=all"}, &stdout, &bytes.Buffer{}))

	var got output
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &got))
	assert.Equal(t, "security,standard", got.Meta.Persona)
	require.Len(t, got.Sessions, 2, "a small change is one session per persona, and no seam pass")
	requests := recorder.all()
	require.Len(t, requests, 2)
	require.Len(t, got.Review.Findings, 1, "the same finding from both personas is merged")
	assert.Equal(t, []string{"security", "standard"}, got.Review.Findings[0].Personas)
	assert.Contains(t, got.Markdown, "_(security, standard)_")

	// Each request's system message must be exactly its persona's prompt --
	// not the same prompt reused for both (which "two sessions ran" alone
	// can't rule out), and with the security persona's front matter
	// actually stripped rather than leaking into the prompt the model sees.
	var systemPrompts []string
	for _, req := range requests {
		require.NotEmpty(t, req.Messages)
		require.Equal(t, "system", req.Messages[0].Role)
		systemPrompts = append(systemPrompts, personaPrompt(req.Messages[0].Content))
	}
	assert.ElementsMatch(t, []string{"You are a reviewer.", "You are a security reviewer."}, systemPrompts)
	for _, prompt := range systemPrompts {
		if prompt == "You are a security reviewer." {
			assert.NotContains(t, prompt, "chunking")
		}
	}
}

// guidanceHeading starts the guidance every session's system prompt ends
// with.
const guidanceHeading = "Guidance for every review"

// personaPrompt is a system prompt without the shared guidance.
func personaPrompt(system string) string {
	before, _, _ := strings.Cut(system, "\n\n"+guidanceHeading)
	return before
}

func TestRun_IntentFileAndCommitLogReachTheSession(t *testing.T) {
	repoDir := newTestRepo(t)
	personaDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(personaDir, "standard.md"), []byte("You are a reviewer."), 0o644))
	intent := filepath.Join(t.TempDir(), "pr.md")
	require.NoError(t, os.WriteFile(intent, []byte("Adds feature.go so that main exists."), 0o644))

	recorder := &requestRecorder{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorder.record(r)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id": "1", "object": "chat.completion", "created": 0, "model": "m",
			"choices": [{"index": 0, "finish_reason": "tool_calls", "message": {
				"role": "assistant", "content": "",
				"tool_calls": [{"id": "call-1", "type": "function", "function": {
					"name": "submit_review",
					"arguments": "{\"verdict\":\"approve\",\"summary\":\"ok\",\"findings\":[]}"
				}}]
			}}]
		}`))
	}))
	defer server.Close()

	t.Setenv("SLOP_REVIEW_REPO_ROOT", repoDir)
	t.Setenv("SLOP_REVIEW_PERSONA_DIR", personaDir)
	t.Setenv("SLOP_REVIEW_BASE_URL", server.URL)
	t.Setenv("SLOP_REVIEW_PROVIDER", "openrouter")
	t.Setenv("OPENROUTER_API_KEY", "test-key")

	require.NoError(t, run([]string{"--intent=" + intent}, &bytes.Buffer{}, &bytes.Buffer{}))

	requests := recorder.all()
	require.Len(t, requests, 1)
	user := requests[0].Messages[1].Content
	assert.Contains(t, user, "Adds feature.go so that main exists.")
	assert.Contains(t, user, "add feature", "the branch's commit message")
}

func TestRun_OnMainWithoutFromCommitHash(t *testing.T) {
	repoDir := newTestRepo(t)
	runGit(t, repoDir, "checkout", "main")
	personaDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(personaDir, "standard.md"), []byte("persona"), 0o644))

	t.Setenv("SLOP_REVIEW_REPO_ROOT", repoDir)
	t.Setenv("SLOP_REVIEW_PERSONA_DIR", personaDir)
	t.Setenv("OPENROUTER_API_KEY", "test-key")
	t.Setenv("SLOP_REVIEW_PROVIDER", "openrouter") // the fake server speaks chat completions

	err := run(nil, &bytes.Buffer{}, &bytes.Buffer{})
	assert.ErrorContains(t, err, "--from-commit-hash")
}

// oneFindingLLMServer is a fake OpenAI-compatible server that immediately
// submits a review with one finding, for the reporting end-to-end tests
// below -- they care about what leaves the process afterward, not the
// session loop itself (already covered by TestRun_EndToEnd and friends).
func oneFindingLLMServer() *httptest.Server {
	return recordingOneFindingLLMServer(nil)
}

// recordingOneFindingLLMServer is oneFindingLLMServer, recording each
// request into recorder when it's set.
func recordingOneFindingLLMServer(recorder *requestRecorder) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if recorder != nil {
			recorder.record(r)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id": "1", "object": "chat.completion", "created": 0, "model": "m",
			"choices": [{"index": 0, "finish_reason": "tool_calls", "message": {
				"role": "assistant", "content": "",
				"tool_calls": [{"id": "call-1", "type": "function", "function": {
					"name": "submit_review",
					"arguments": "{\"verdict\":\"approve_with_comments\",\"summary\":\"one nit\",\"findings\":[{\"file\":\"feature.go\",\"line\":3,\"severity\":\"nit\",\"summary\":\"nit\",\"detail\":\"d\"}]}"
				}}]
			}}]
		}`))
	}))
}

// reportServer is a fake report server ingest endpoint: it answers
// every POST to report.IngestPath with status, capturing the request so a
// test can assert on what was actually sent (the auth header, the run,
// including its findings' ids).
type reportServer struct {
	*httptest.Server
	mu       sync.Mutex
	requests []reportRequest
	status   int
	body     string
	// providerConfig is what GET providerconfig.Path answers, with
	// providerConfigStatus; an empty body is a 404, as from a server with
	// no config for the tenant.
	providerConfig       string
	providerConfigStatus int
	configFetches        int
}

type reportRequest struct {
	auth string
	run  report.Run
}

func newReportServer(t *testing.T, status int, body string) *reportServer {
	t.Helper()
	rs := &reportServer{status: status, body: body}
	// The central config's saved copy is kept per test, never in the
	// user's own cache.
	t.Setenv("SLOP_REVIEW_PROVIDER_CONFIG_CACHE_DIR", filepath.Join(t.TempDir(), "provider-config"))
	rs.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == providerconfig.Path {
			rs.mu.Lock()
			rs.configFetches++
			body, status := rs.providerConfig, rs.providerConfigStatus
			rs.mu.Unlock()
			if body == "" {
				status = http.StatusNotFound
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_, _ = w.Write([]byte(body))
			return
		}
		require.Equal(t, report.IngestPath, r.URL.Path)
		var run report.Run
		require.NoError(t, json.NewDecoder(r.Body).Decode(&run))
		rs.mu.Lock()
		rs.requests = append(rs.requests, reportRequest{auth: r.Header.Get("Authorization"), run: run})
		rs.mu.Unlock()

		rs.mu.Lock()
		status, body := rs.status, rs.body
		rs.mu.Unlock()
		// Success acknowledges the run actually posted, as the real server
		// does: the client checks the acknowledged ID is its own.
		if status == http.StatusOK || status == http.StatusCreated {
			ack, err := json.Marshal(report.Accepted{RunID: run.ID, Created: status == http.StatusCreated, URL: "http://example/runs/" + run.ID.String()})
			require.NoError(t, err)
			body = string(ack)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	return rs
}

// setProviderConfig makes the server answer the config fetch with status
// and body.
func (rs *reportServer) setProviderConfig(status int, body string) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	rs.providerConfigStatus, rs.providerConfig = status, body
}

func (rs *reportServer) setStatus(status int, body string) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	rs.status, rs.body = status, body
}

func (rs *reportServer) all() []reportRequest {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return append([]reportRequest(nil), rs.requests...)
}

func setUpReportingRepoAndLLM(t *testing.T) (repoDir string, llmServer *httptest.Server) {
	t.Helper()
	repoDir = newTestRepo(t)
	personaDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(personaDir, "standard.md"), []byte("You are a reviewer."), 0o644))
	llmServer = oneFindingLLMServer()

	t.Setenv("SLOP_REVIEW_REPO_ROOT", repoDir)
	t.Setenv("SLOP_REVIEW_PERSONA_DIR", personaDir)
	t.Setenv("SLOP_REVIEW_BASE_URL", llmServer.URL)
	t.Setenv("OPENROUTER_API_KEY", "test-key")
	t.Setenv("SLOP_REVIEW_PROVIDER", "openrouter") // the fake server speaks chat completions
	t.Setenv("SLOP_REVIEW_TOOL_VERSION", "abc1234")
	return repoDir, llmServer
}

// TestRun_ReportsRunAndAddsReportInfoToOutput checks the success path of
// --report-to end to end: the run is posted with a bearer token, and the
// server's Accepted answer shows up in stdout's "report" field.
func TestRun_ReportsRunAndAddsReportInfoToOutput(t *testing.T) {
	_, llmServer := setUpReportingRepoAndLLM(t)
	defer llmServer.Close()

	rs := newReportServer(t, http.StatusCreated, "")
	defer rs.Close()

	t.Setenv("SLOP_REVIEW_REPORT_URL", rs.URL)
	t.Setenv("SLOP_REVIEW_REPORT_TOKEN", "secret-token")
	t.Setenv("SLOP_REVIEW_REPORT_TOKEN_COMMAND", "")

	var stdout bytes.Buffer
	require.NoError(t, run(nil, &stdout, &bytes.Buffer{}))

	var got output
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &got))
	require.NotNil(t, got.Report)
	require.Len(t, rs.all(), 1)
	assert.Equal(t, rs.all()[0].run.ID.String(), got.Report.RunID)
	assert.True(t, got.Report.Created)
	assert.Empty(t, got.Review.Warnings, "a successful report must not add a warning")

	requests := rs.all()
	require.Len(t, requests, 1)
	assert.Equal(t, "Bearer secret-token", requests[0].auth)
	assert.Equal(t, "abc1234", requests[0].run.ToolVersion.Value)
	assert.Equal(t, "git@github.com:acme/widget.git", requests[0].run.RepoRemote, "the repo's origin, from newTestRepo")
	assert.Equal(t, "test <test@example.com>", requests[0].run.HeadAuthor)

	require.Len(t, got.Review.Findings, 1)
	require.Len(t, requests[0].run.Findings, 1)
	assert.NotEqual(t, uuid.Nil, got.Review.Findings[0].ID)
	assert.Equal(t, requests[0].run.Findings[0].ID, got.Review.Findings[0].ID, "the printed id is the server's finding id")
	assert.Contains(t, got.Markdown, got.Review.Findings[0].ID.String())
}

// TestRun_ReportRepoOverridesConfiguredRemote checks that --report-repo
// wins over whatever remote is actually configured -- for a repo whose
// remote isn't the shared identity (a personal fork, say).
func TestRun_ReportRepoOverridesConfiguredRemote(t *testing.T) {
	_, llmServer := setUpReportingRepoAndLLM(t)
	defer llmServer.Close()

	rs := newReportServer(t, http.StatusCreated, "")
	defer rs.Close()

	t.Setenv("SLOP_REVIEW_REPORT_URL", rs.URL)
	t.Setenv("SLOP_REVIEW_REPORT_TOKEN", "secret-token")
	t.Setenv("SLOP_REVIEW_REPORT_TOKEN_COMMAND", "")
	t.Setenv("SLOP_REVIEW_REPORT_REPO", "git@github.com:acme/upstream.git")

	var stdout bytes.Buffer
	require.NoError(t, run(nil, &stdout, &bytes.Buffer{}))

	requests := rs.all()
	require.Len(t, requests, 1)
	assert.Equal(t, "git@github.com:acme/upstream.git", requests[0].run.RepoRemote)
}

// TestRun_ReportSkippedWhenNoRemote checks that reporting is skipped
// outright -- with a clear warning, and nothing spooled -- for a repo with
// no remote and no --report-repo override, since such a run can never pass
// report.Run.Validate and so could never be delivered by retrying it.
func TestRun_ReportSkippedWhenNoRemote(t *testing.T) {
	dir := t.TempDir()
	runGit(t, dir, "init", "--initial-branch=main")
	writeFile(t, dir, "README.md", "hello\n")
	runGit(t, dir, "add", "README.md")
	runGit(t, dir, "commit", "-m", "initial commit")
	runGit(t, dir, "checkout", "-b", "feature")
	writeFile(t, dir, "feature.go", "package main\n\nfunc main() {}\n")
	runGit(t, dir, "add", "feature.go")
	runGit(t, dir, "commit", "-m", "add feature")

	personaDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(personaDir, "standard.md"), []byte("You are a reviewer."), 0o644))
	llmServer := oneFindingLLMServer()
	defer llmServer.Close()

	rs := newReportServer(t, http.StatusCreated, "")
	defer rs.Close()

	spoolDir := filepath.Join(t.TempDir(), "spool")
	t.Setenv("SLOP_REVIEW_REPO_ROOT", dir)
	t.Setenv("SLOP_REVIEW_PERSONA_DIR", personaDir)
	t.Setenv("SLOP_REVIEW_BASE_URL", llmServer.URL)
	t.Setenv("OPENROUTER_API_KEY", "test-key")
	t.Setenv("SLOP_REVIEW_PROVIDER", "openrouter") // the fake server speaks chat completions
	t.Setenv("SLOP_REVIEW_REPORT_URL", rs.URL)
	t.Setenv("SLOP_REVIEW_REPORT_TOKEN", "secret-token")
	t.Setenv("SLOP_REVIEW_REPORT_TOKEN_COMMAND", "")
	t.Setenv("SLOP_REVIEW_REPORT_SPOOL_DIR", spoolDir)

	var stdout bytes.Buffer
	require.NoError(t, run(nil, &stdout, &bytes.Buffer{}), "a run that can't be reported must not fail the review")

	var got output
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &got))
	assert.Nil(t, got.Report)

	found := false
	for _, w := range got.Review.Warnings {
		if strings.Contains(w, "no git remote identifies this repo") {
			found = true
		}
	}
	assert.True(t, found, "warnings: %v", got.Review.Warnings)

	assert.Empty(t, rs.all(), "nothing must be posted when there's no remote to report it under")
	assert.Empty(t, spooledRuns(t, spoolDir), "a run that can never be valid must not be spooled")
}

// TestRun_ReportedWarningsMatchStdout checks that a scope-level warning
// (here: a secret-shaped file let through by --allow-secrets) shows up in
// the posted report's warnings, not just stdout's -- sendReport builds the
// report from the orchestrator result directly, which doesn't itself carry
// scope.Warnings, so this is exactly the kind of mismatch that stays
// invisible unless something asserts both sides agree.
func TestRun_ReportedWarningsMatchStdout(t *testing.T) {
	repoDir := newTestRepo(t)
	writeFile(t, repoDir, ".env", "API_KEY=top-secret-value\n")
	personaDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(personaDir, "standard.md"), []byte("You are a reviewer."), 0o644))
	llmServer := oneFindingLLMServer()
	defer llmServer.Close()

	t.Setenv("SLOP_REVIEW_REPO_ROOT", repoDir)
	t.Setenv("SLOP_REVIEW_PERSONA_DIR", personaDir)
	t.Setenv("SLOP_REVIEW_BASE_URL", llmServer.URL)
	t.Setenv("OPENROUTER_API_KEY", "test-key")
	t.Setenv("SLOP_REVIEW_PROVIDER", "openrouter") // the fake server speaks chat completions
	t.Setenv("SLOP_REVIEW_TOOL_VERSION", "abc1234")
	t.Setenv("SLOP_REVIEW_ALLOW_SECRETS", "true")

	rs := newReportServer(t, http.StatusCreated, "")
	defer rs.Close()

	t.Setenv("SLOP_REVIEW_REPORT_URL", rs.URL)
	t.Setenv("SLOP_REVIEW_REPORT_TOKEN", "secret-token")
	t.Setenv("SLOP_REVIEW_REPORT_TOKEN_COMMAND", "")

	var stdout bytes.Buffer
	require.NoError(t, run(nil, &stdout, &bytes.Buffer{}))

	var got output
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &got))
	require.NotEmpty(t, got.Review.Warnings, "the .env file let through by --allow-secrets must be warned about")

	requests := rs.all()
	require.Len(t, requests, 1)
	assert.ElementsMatch(t, got.Review.Warnings, requests[0].run.Warnings,
		"the posted report's warnings must match stdout's exactly")
}

// TestRun_StdoutFindingIDsMatchReportedFindingIDs is the guarantee the
// whole ingest contract rests on: `slop-review label <id>` only makes
// sense if the id a human/Claude sees on stdout is the same one the server
// stored.
func TestRun_StdoutFindingIDsMatchReportedFindingIDs(t *testing.T) {
	_, llmServer := setUpReportingRepoAndLLM(t)
	defer llmServer.Close()

	rs := newReportServer(t, http.StatusCreated, "")
	defer rs.Close()

	t.Setenv("SLOP_REVIEW_REPORT_URL", rs.URL)
	t.Setenv("SLOP_REVIEW_REPORT_TOKEN", "secret-token")
	t.Setenv("SLOP_REVIEW_REPORT_TOKEN_COMMAND", "")

	var stdout bytes.Buffer
	require.NoError(t, run(nil, &stdout, &bytes.Buffer{}))

	var got output
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &got))
	require.Len(t, got.Review.Findings, 1)
	require.NotEqual(t, uuid.Nil, got.Review.Findings[0].ID)

	requests := rs.all()
	require.Len(t, requests, 1)
	require.Len(t, requests[0].run.Findings, 1)
	assert.Equal(t, got.Review.Findings[0].ID, requests[0].run.Findings[0].ID)
}

// TestRun_ReportFailureIsWarningNotError checks that a reporting failure
// (a 500 here) never fails the review: the command still exits 0, the
// review's own output is otherwise intact, and the run is spooled with
// restrictive permissions for a later invocation to retry.
func TestRun_ReportFailureIsWarningNotError(t *testing.T) {
	_, llmServer := setUpReportingRepoAndLLM(t)
	defer llmServer.Close()

	rs := newReportServer(t, http.StatusInternalServerError, `{"error":"boom"}`)
	defer rs.Close()

	spoolDir := filepath.Join(t.TempDir(), "spool")
	t.Setenv("SLOP_REVIEW_REPORT_URL", rs.URL)
	t.Setenv("SLOP_REVIEW_REPORT_TOKEN", "secret-token")
	t.Setenv("SLOP_REVIEW_REPORT_TOKEN_COMMAND", "")
	t.Setenv("SLOP_REVIEW_REPORT_SPOOL_DIR", spoolDir)

	var stdout bytes.Buffer
	require.NoError(t, run(nil, &stdout, &bytes.Buffer{}), "a reporting failure must not fail the review")

	var got output
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &got))
	assert.Nil(t, got.Report)
	assert.Equal(t, "approve_with_comments", string(got.Review.Verdict), "the review's own result must be unaffected")
	require.Len(t, got.Review.Findings, 1)

	found := false
	for _, w := range got.Review.Warnings {
		if strings.Contains(w, "not reported") {
			found = true
		}
	}
	assert.True(t, found, "a warning must explain the run wasn't reported")

	runs := spooledRuns(t, spoolDir)
	require.Len(t, runs, 1)
	info, err := os.Stat(runs[0])
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	body, err := os.ReadFile(runs[0])
	require.NoError(t, err)
	var spooled report.Run
	require.NoError(t, json.Unmarshal(body, &spooled))
	require.Len(t, spooled.Findings, 1)
	assert.Equal(t, got.Review.Findings[0].ID, spooled.Findings[0].ID, "a spooled run keeps the printed finding ids")
}

// TestRun_SpooledRunFromPriorInvocationIsRetried runs once while the report
// server is failing (spooling the run), then again once it recovers, and
// checks the earlier run is delivered alongside the new one and removed
// from the spool.
func TestRun_SpooledRunFromPriorInvocationIsRetried(t *testing.T) {
	_, llmServer := setUpReportingRepoAndLLM(t)
	defer llmServer.Close()

	rs := newReportServer(t, http.StatusInternalServerError, `{}`)
	defer rs.Close()
	spoolDir := filepath.Join(t.TempDir(), "spool")
	t.Setenv("SLOP_REVIEW_REPORT_URL", rs.URL)
	t.Setenv("SLOP_REVIEW_REPORT_TOKEN", "secret-token")
	t.Setenv("SLOP_REVIEW_REPORT_TOKEN_COMMAND", "")
	t.Setenv("SLOP_REVIEW_REPORT_SPOOL_DIR", spoolDir)

	require.NoError(t, run(nil, &bytes.Buffer{}, &bytes.Buffer{}))
	require.Len(t, spooledRuns(t, spoolDir), 1, "the first run must have been spooled")
	first := rs.all()[0].run.ID

	rs.setStatus(http.StatusCreated, "")
	require.NoError(t, run(nil, &bytes.Buffer{}, &bytes.Buffer{}))

	var delivered []uuid.UUID
	for _, req := range rs.all()[1:] {
		delivered = append(delivered, req.run.ID)
	}
	assert.Contains(t, delivered, first, "the spooled run must be delivered once the server recovers")
	assert.Len(t, delivered, 2, "the spooled run and the new one")
	assert.Empty(t, spooledRuns(t, spoolDir), "the spooled run must be gone once reported")
}

// A run spooled for one server is that server's: pointing --report-to
// elsewhere must not send it there, under another server's token.
func TestRun_SpooledRunIsNeverSentToADifferentServer(t *testing.T) {
	_, llmServer := setUpReportingRepoAndLLM(t)
	defer llmServer.Close()

	failing := newReportServer(t, http.StatusInternalServerError, `{}`)
	defer failing.Close()
	spoolDir := filepath.Join(t.TempDir(), "spool")
	t.Setenv("SLOP_REVIEW_REPORT_URL", failing.URL)
	t.Setenv("SLOP_REVIEW_REPORT_TOKEN", "secret-token")
	t.Setenv("SLOP_REVIEW_REPORT_TOKEN_COMMAND", "")
	t.Setenv("SLOP_REVIEW_REPORT_SPOOL_DIR", spoolDir)
	require.NoError(t, run(nil, &bytes.Buffer{}, &bytes.Buffer{}))
	spooledID := failing.all()[0].run.ID

	other := newReportServer(t, http.StatusCreated, "")
	defer other.Close()
	t.Setenv("SLOP_REVIEW_REPORT_URL", other.URL)
	require.NoError(t, run(nil, &bytes.Buffer{}, &bytes.Buffer{}))

	require.Len(t, other.all(), 1, "only the new run goes to the other server")
	assert.NotEqual(t, spooledID, other.all()[0].run.ID)
	assert.Len(t, spooledRuns(t, spoolDir), 1, "the first server's run stays spooled for it")
}

// spooledRuns lists every spooled run file under spoolDir, whichever
// server's subdirectory it is in.
func spooledRuns(t *testing.T, spoolDir string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(spoolDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if !d.IsDir() && strings.HasSuffix(path, ".json") {
			out = append(out, path)
		}
		return nil
	})
	require.NoError(t, err)
	return out
}

// newTestRepo mirrors internal/gitctx's own test fixture: a main branch
// with one commit, and a feature branch with a second commit on top, with
// an "origin" remote configured -- so report.RemoteKey has something to
// key a posted run's repo by (see the reporting end-to-end tests below).
// Tests exercising the no-remote case (TestRun_ReportSkippedWhenNoRemote)
// build their own repo without one.
func newTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init", "--initial-branch=main")
	runGit(t, dir, "remote", "add", "origin", "git@github.com:acme/widget.git")
	writeFile(t, dir, "README.md", "hello\n")
	runGit(t, dir, "add", "README.md")
	runGit(t, dir, "commit", "-m", "initial commit")
	runGit(t, dir, "checkout", "-b", "feature")
	writeFile(t, dir, "feature.go", "package main\n\nfunc main() {}\n")
	runGit(t, dir, "add", "feature.go")
	runGit(t, dir, "commit", "-m", "add feature")
	return dir
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com",
	)
	out, err := cmd.CombinedOutput()
	require.NoErrorf(t, err, "git %v: %s", args, out)
}

func writeFile(t *testing.T, repoDir, relPath, content string) {
	t.Helper()
	full := filepath.Join(repoDir, relPath)
	require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
	require.NoError(t, os.WriteFile(full, []byte(content), 0o644))
}

// failingLLMServer answers every chat completion with status and body.
func failingLLMServer(status int, body string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
}

// setUpFailedRunReporting points reporting at a fresh report server and
// returns it.
func setUpFailedRunReporting(t *testing.T) *reportServer {
	t.Helper()
	rs := newReportServer(t, http.StatusCreated, "")
	t.Cleanup(rs.Close)
	t.Setenv("SLOP_REVIEW_REPORT_URL", rs.URL)
	t.Setenv("SLOP_REVIEW_REPORT_TOKEN", "secret-token")
	t.Setenv("SLOP_REVIEW_REPORT_TOKEN_COMMAND", "")
	t.Setenv("SLOP_REVIEW_REPORT_SPOOL_DIR", filepath.Join(t.TempDir(), "spool"))
	return rs
}

// A run that fails is reported as failed, with the class that says what to
// do about it -- at whichever stage it failed -- and still fails.
func TestRun_FailedRunsAreReportedWithTheirClass(t *testing.T) {
	notLoggedIn := func(t *testing.T) *providers.Factory {
		path := filepath.Join(t.TempDir(), "pi")
		require.NoError(t, os.WriteFile(path, []byte("#!/bin/sh\necho '{\"status\":\"not_ready\",\"provider\":\"openai\",\"reason\":\"credentials_not_configured\"}'\nexit 1\n"), 0o755))
		return &providers.Factory{PiCommand: []string{path}}
	}
	for name, tc := range map[string]struct {
		setUp        func(t *testing.T) *providers.Factory
		wantClass    report.FailureClass
		wantRepo     bool
		wantPersonas []string
	}{
		"config: no api key": {func(t *testing.T) *providers.Factory {
			t.Setenv("OPENROUTER_API_KEY", "")
			return &providers.Factory{}
		}, report.FailureConfig, false, nil},
		"scope: not a git repository": {func(t *testing.T) *providers.Factory {
			t.Setenv("SLOP_REVIEW_REPO_ROOT", t.TempDir())
			return &providers.Factory{}
		}, report.FailureConfig, false, nil},
		"provider setup: chatgpt not logged in": {func(t *testing.T) *providers.Factory {
			t.Setenv("SLOP_REVIEW_PROVIDER", "chatgpt")
			t.Setenv("SLOP_REVIEW_BASE_URL", "")
			return notLoggedIn(t)
		}, report.FailureAuth, true, []string{"standard"}},
		"orchestration: openrouter out of credits": {func(t *testing.T) *providers.Factory {
			llm := failingLLMServer(http.StatusPaymentRequired, `{"error":{"message":"Insufficient credits","code":402}}`)
			t.Cleanup(llm.Close)
			t.Setenv("SLOP_REVIEW_BASE_URL", llm.URL)
			return &providers.Factory{}
		}, report.FailureCredits, true, []string{"standard"}},
		"orchestration: openrouter key rejected": {func(t *testing.T) *providers.Factory {
			llm := failingLLMServer(http.StatusUnauthorized, `{"error":{"message":"No auth credentials found","code":401}}`)
			t.Cleanup(llm.Close)
			t.Setenv("SLOP_REVIEW_BASE_URL", llm.URL)
			return &providers.Factory{}
		}, report.FailureAuth, true, []string{"standard"}},
		"orchestration: no provider error": {func(t *testing.T) *providers.Factory {
			// A model that never submits: every session runs out of turns.
			llm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"id":"1","object":"chat.completion","created":0,"model":"m","choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"thinking"}}]}`))
			}))
			t.Cleanup(llm.Close)
			t.Setenv("SLOP_REVIEW_BASE_URL", llm.URL)
			t.Setenv("SLOP_REVIEW_MAX_TURNS", "1")
			return &providers.Factory{}
		}, report.FailureOther, true, []string{"standard"}},
	} {
		t.Run(name, func(t *testing.T) {
			_, llmServer := setUpReportingRepoAndLLM(t)
			defer llmServer.Close()
			rs := setUpFailedRunReporting(t)
			factory := tc.setUp(t)

			var stdout, stderr bytes.Buffer
			err := runWith(nil, &stdout, &stderr, factory)
			require.Error(t, err, "a failed run still fails")
			assert.Empty(t, stdout.String())

			requests := rs.all()
			require.Len(t, requests, 1, "stderr: %s", stderr.String())
			got := requests[0].run
			require.NoError(t, got.Validate())
			assert.Equal(t, report.StatusFailed, got.Status)
			require.NotNil(t, got.Failure)
			assert.Equal(t, tc.wantClass, got.Failure.Class)
			assert.Equal(t, err.Error(), got.Failure.Message)
			host, _ := os.Hostname()
			assert.Equal(t, host, got.Failure.Host)
			assert.Equal(t, "Bearer secret-token", requests[0].auth)
			assert.Equal(t, tc.wantPersonas, got.Personas)
			if tc.wantRepo {
				assert.Equal(t, "git@github.com:acme/widget.git", got.RepoRemote)
			} else {
				assert.Empty(t, got.RepoRemote, "the failure came before the scope was known")
			}
		})
	}
}

// A failed run the server can't take is spooled like any other, and
// delivered by the next invocation.
func TestRun_FailedRunIsSpooledWhenTheServerIsDown(t *testing.T) {
	_, llmServer := setUpReportingRepoAndLLM(t)
	defer llmServer.Close()
	rs := setUpFailedRunReporting(t)
	rs.setStatus(http.StatusServiceUnavailable, `{}`)
	spoolDir := os.Getenv("SLOP_REVIEW_REPORT_SPOOL_DIR")
	t.Setenv("OPENROUTER_API_KEY", "")

	var stderr bytes.Buffer
	require.Error(t, runWith(nil, &bytes.Buffer{}, &stderr, &providers.Factory{}))
	assert.Contains(t, stderr.String(), "not reported", "reporting trouble is said on stderr, since a failed run prints no JSON")
	require.Len(t, spooledRuns(t, spoolDir), 1)

	rs.setStatus(http.StatusCreated, "")
	t.Setenv("OPENROUTER_API_KEY", "test-key")
	require.NoError(t, run(nil, &bytes.Buffer{}, &bytes.Buffer{}))
	assert.Empty(t, spooledRuns(t, spoolDir))
	var statuses []report.Status
	for _, req := range rs.all()[1:] {
		statuses = append(statuses, req.run.Status)
	}
	assert.ElementsMatch(t, []report.Status{report.StatusFailed, report.StatusCompleted}, statuses)
}

func TestRun_FailedRunIsNotReportedWithoutAReportURL(t *testing.T) {
	_, llmServer := setUpReportingRepoAndLLM(t)
	defer llmServer.Close()
	rs := newReportServer(t, http.StatusCreated, "")
	defer rs.Close()
	t.Setenv("OPENROUTER_API_KEY", "")

	require.Error(t, run(nil, &bytes.Buffer{}, &bytes.Buffer{}))
	assert.Empty(t, rs.all())
}
