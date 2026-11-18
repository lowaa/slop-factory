package reporter

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-review/report"
)

// validRun builds a minimal report.Run that passes Validate, for tests
// that care about the reporter's HTTP/spool behaviour rather than the
// contract itself (see report/report_test.go for that).
func validRun(t *testing.T) *report.Run {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	return &report.Run{
		SchemaVersion: report.SchemaVersion,
		ID:            uuid.New(),
		Kind:          report.KindReview,
		Branch:        "feature",
		BaseRef:       "main",
		HeadSHA:       "0123456789abcdef0123456789abcdef01234567",
		ToolVersion:   report.ToolVersion{Kind: "git_sha", Value: "deadbee"},
		Provider:      "openrouter",
		Model:         "m",
		Personas:      []string{"standard"},
		Verdict:       "approve",
		Summary:       "fine",
		Sessions:      []report.Session{{Persona: "standard", Part: 1, Parts: 1}},
		StartedAt:     now,
		FinishedAt:    now.Add(time.Second),
	}
}

func TestNew_RejectsPlainHTTPToNonLoopback(t *testing.T) {
	_, err := New("http://example.com", "tok", t.TempDir(), slog.DiscardHandler)
	assert.ErrorContains(t, err, "https")
}

func TestNew_AllowsPlainHTTPToLoopback(t *testing.T) {
	for _, u := range []string{"http://127.0.0.1:8080", "http://localhost:8080", "http://[::1]:8080"} {
		_, err := New(u, "tok", t.TempDir(), slog.DiscardHandler)
		assert.NoError(t, err, u)
	}
}

func TestNew_AllowsHTTPS(t *testing.T) {
	_, err := New("https://slop-review.example.ts.net", "tok", t.TempDir(), slog.DiscardHandler)
	assert.NoError(t, err)
}

func TestNew_RejectsUnparseableURL(t *testing.T) {
	_, err := New("://not-a-url", "tok", t.TempDir(), slog.DiscardHandler)
	assert.Error(t, err)
}

func TestNew_RejectsAURLWithNoHost(t *testing.T) {
	_, err := New("https:///path", "tok", t.TempDir(), slog.DiscardHandler)
	assert.ErrorContains(t, err, "host")
}

// Credentials belong to SLOP_REVIEW_REPORT_TOKEN, never the URL: a stray
// user:pass@ (or a token pasted in as the username) would otherwise travel
// wherever the URL does -- config, process listings, logs -- instead of
// staying wherever secrets are supposed to live.
func TestNew_RejectsUserinfoInTheURL(t *testing.T) {
	_, err := New("https://someone:hunter2@slop-review.example.ts.net", "tok", t.TempDir(), slog.DiscardHandler)
	require.Error(t, err)
	assert.ErrorContains(t, err, "credentials")
	assert.NotContains(t, err.Error(), "hunter2", "the credential itself must never appear in the error")
	assert.NotContains(t, err.Error(), "someone", "the username must never appear in the error either")
}

func TestNew_RejectsAQuery(t *testing.T) {
	_, err := New("https://slop-review.example.ts.net?x=1", "tok", t.TempDir(), slog.DiscardHandler)
	assert.ErrorContains(t, err, "query")
}

func TestNew_RejectsAFragment(t *testing.T) {
	_, err := New("https://slop-review.example.ts.net#frag", "tok", t.TempDir(), slog.DiscardHandler)
	assert.ErrorContains(t, err, "fragment")
}

// recordingServer is an httptest server that captures every request it
// sees before answering with a scripted status and body.
type recordingServer struct {
	*httptest.Server
	requests []capturedRequest
}

type capturedRequest struct {
	auth string
	body []byte
}

func newRecordingServer(t *testing.T, status int, body string) *recordingServer {
	t.Helper()
	rs := &recordingServer{}
	rs.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, report.IngestPath, r.URL.Path)
		b, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		rs.requests = append(rs.requests, capturedRequest{auth: r.Header.Get("Authorization"), body: b})
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	return rs
}

func TestSend_SuccessReturnsAcceptedAndSendsBearerToken(t *testing.T) {
	run := validRun(t)
	accepted := report.Accepted{RunID: run.ID, Created: true, URL: "https://example/runs/" + run.ID.String()}
	acceptedJSON, err := json.Marshal(accepted)
	require.NoError(t, err)

	server := newRecordingServer(t, http.StatusCreated, string(acceptedJSON))
	defer server.Close()

	r, err := New(server.URL, "secret-token", t.TempDir(), slog.DiscardHandler)
	require.NoError(t, err)

	result := r.Send(context.Background(), run)
	require.NotNil(t, result.Accepted)
	assert.Empty(t, result.Warning)
	assert.Equal(t, run.ID, result.Accepted.RunID)
	assert.True(t, result.Accepted.Created)

	require.Len(t, server.requests, 1)
	assert.Equal(t, "Bearer secret-token", server.requests[0].auth)
	assert.NotContains(t, string(server.requests[0].body), "\"warning\"") // sanity: the posted body is the run, not something wrapping it
}

func TestSend_5xxIsSpooledWithRestrictivePermsAndWarns(t *testing.T) {
	run := validRun(t)
	server := newRecordingServer(t, http.StatusInternalServerError, `{"error":"boom"}`)
	defer server.Close()

	spoolDir := filepath.Join(t.TempDir(), "spool") // must not exist yet -- New/Send must create it
	r, err := New(server.URL, "tok", spoolDir, slog.DiscardHandler)
	require.NoError(t, err)

	result := r.Send(context.Background(), run)
	assert.Nil(t, result.Accepted)
	assert.Contains(t, result.Warning, "not reported")
	assert.Contains(t, result.Warning, spoolDir)

	path := filepath.Join(serverSpoolDir(spoolDir, server.URL), run.ID.String()+".json")
	info, err := os.Stat(path)
	require.NoError(t, err, "the run must have been spooled")
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	dirInfo, err := os.Stat(spoolDir)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), dirInfo.Mode().Perm())

	spooled, err := os.ReadFile(path)
	require.NoError(t, err)
	var got report.Run
	require.NoError(t, json.Unmarshal(spooled, &got))
	assert.Equal(t, run.ID, got.ID)
}

func TestSend_429IsSpooled(t *testing.T) {
	run := validRun(t)
	server := newRecordingServer(t, http.StatusTooManyRequests, `{"error":"slow down"}`)
	defer server.Close()

	spoolDir := t.TempDir()
	r, err := New(server.URL, "tok", spoolDir, slog.DiscardHandler)
	require.NoError(t, err)

	result := r.Send(context.Background(), run)
	assert.Contains(t, result.Warning, "not reported")
	_, err = os.Stat(filepath.Join(serverSpoolDir(spoolDir, server.URL), run.ID.String()+".json"))
	assert.NoError(t, err, "429 is transient and must be spooled for retry")
}

func TestSend_PermanentClientErrorsAreNotSpooled(t *testing.T) {
	for _, status := range []int{http.StatusConflict, http.StatusUnprocessableEntity} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			run := validRun(t)
			server := newRecordingServer(t, status, `{"error":"no"}`)
			defer server.Close()

			spoolDir := t.TempDir()
			r, err := New(server.URL, "tok", spoolDir, slog.DiscardHandler)
			require.NoError(t, err)

			result := r.Send(context.Background(), run)
			assert.Nil(t, result.Accepted)
			assert.Contains(t, result.Warning, "not reported")
			assert.NotContains(t, result.Warning, "spooled", "a permanent rejection must not be spooled -- retrying can't help")

			entries, err := os.ReadDir(spoolDir)
			require.NoError(t, err)
			assert.Empty(t, entries)
		})
	}
}

func TestRetrySpooled_SendsAndDeletesOnSuccess(t *testing.T) {
	run := validRun(t)
	spoolDir := t.TempDir()

	body, err := json.Marshal(run)
	require.NoError(t, err)

	accepted := report.Accepted{RunID: run.ID, Created: true, URL: "https://example/runs/" + run.ID.String()}
	acceptedJSON, err := json.Marshal(accepted)
	require.NoError(t, err)
	server := newRecordingServer(t, http.StatusOK, string(acceptedJSON))
	defer server.Close()
	spoolPath := filepath.Join(serverSpoolDir(spoolDir, server.URL), run.ID.String()+".json")
	require.NoError(t, os.MkdirAll(filepath.Dir(spoolPath), 0o700))
	require.NoError(t, os.WriteFile(spoolPath, body, 0o600))

	r, err := New(server.URL, "tok", spoolDir, slog.DiscardHandler)
	require.NoError(t, err)

	r.RetrySpooled(context.Background())

	require.Len(t, server.requests, 1, "the spooled run must have been posted")
	_, err = os.Stat(spoolPath)
	assert.True(t, os.IsNotExist(err), "a successfully reported spooled run must be removed")
}

func TestRetrySpooled_KeepsOnTransientFailure(t *testing.T) {
	run := validRun(t)
	spoolDir := t.TempDir()
	body, err := json.Marshal(run)
	require.NoError(t, err)

	server := newRecordingServer(t, http.StatusInternalServerError, `{}`)
	defer server.Close()
	spoolPath := filepath.Join(serverSpoolDir(spoolDir, server.URL), run.ID.String()+".json")
	require.NoError(t, os.MkdirAll(filepath.Dir(spoolPath), 0o700))
	require.NoError(t, os.WriteFile(spoolPath, body, 0o600))

	r, err := New(server.URL, "tok", spoolDir, slog.DiscardHandler)
	require.NoError(t, err)

	r.RetrySpooled(context.Background())

	_, err = os.Stat(spoolPath)
	assert.NoError(t, err, "a still-failing spooled run must be kept for next time")
}

func TestRetrySpooled_DropsOnPermanentRejection(t *testing.T) {
	run := validRun(t)
	spoolDir := t.TempDir()
	body, err := json.Marshal(run)
	require.NoError(t, err)

	server := newRecordingServer(t, http.StatusConflict, `{"error":"already exists"}`)
	defer server.Close()
	spoolPath := filepath.Join(serverSpoolDir(spoolDir, server.URL), run.ID.String()+".json")
	require.NoError(t, os.MkdirAll(filepath.Dir(spoolPath), 0o700))
	require.NoError(t, os.WriteFile(spoolPath, body, 0o600))

	r, err := New(server.URL, "tok", spoolDir, slog.DiscardHandler)
	require.NoError(t, err)

	r.RetrySpooled(context.Background())

	_, err = os.Stat(spoolPath)
	assert.True(t, os.IsNotExist(err), "a permanently-rejected spooled run must be dropped, not retried forever")
}

func TestRetrySpooled_NoSpoolDirIsNotAnError(t *testing.T) {
	r, err := New("http://127.0.0.1:1", "tok", filepath.Join(t.TempDir(), "does-not-exist"), slog.DiscardHandler)
	require.NoError(t, err)
	assert.NotPanics(t, func() { r.RetrySpooled(context.Background()) })
}

// TestSend_TLSVerificationIsNotSkippableByDefault proves the https-only
// rule has teeth: an httptest TLS server uses a self-signed certificate,
// and the default client (the only one New ever builds) must reject it
// exactly like any real TLS client would reject an unrecognised cert --
// otherwise "https required" would be theatre.
func TestSend_TLSVerificationIsNotSkippableByDefault(t *testing.T) {
	run := validRun(t)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("the request must never reach the server: the client must refuse the certificate first")
	}))
	defer server.Close()

	spoolDir := t.TempDir()
	r, err := New(server.URL, "tok", spoolDir, slog.DiscardHandler)
	require.NoError(t, err)

	result := r.Send(context.Background(), run)
	assert.Contains(t, result.Warning, "not reported")
	_, err = os.Stat(filepath.Join(serverSpoolDir(spoolDir, server.URL), run.ID.String()+".json"))
	assert.NoError(t, err, "a TLS failure is transient from the caller's point of view and must be spooled")
}

// TestSend_TestOnlyClientInjectionCanTrustTheTLSServer is the other half of
// the proof above: the *only* way to make the self-signed server usable in
// a test is the unexported withHTTPClient option, which is not reachable
// from cmd/slop-review's flags -- there is no production path to this.
func TestSend_TestOnlyClientInjectionCanTrustTheTLSServer(t *testing.T) {
	run := validRun(t)
	accepted := report.Accepted{RunID: run.ID, Created: true, URL: "https://example/runs/" + run.ID.String()}
	acceptedJSON, err := json.Marshal(accepted)
	require.NoError(t, err)

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(acceptedJSON))
	}))
	defer server.Close()

	r, err := New(server.URL, "tok", t.TempDir(), slog.DiscardHandler, withHTTPClient(server.Client()))
	require.NoError(t, err)

	result := r.Send(context.Background(), run)
	require.NotNil(t, result.Accepted)
	assert.Empty(t, result.Warning)
}

// TestSend_TokenNeverAppearsInLogs sends both a successful and a failing
// run through a Reporter whose logger writes to a buffer, then checks the
// token string never shows up in it -- only the Authorization header (see
// TestSend_SuccessReturnsAcceptedAndSendsBearerToken) is where it belongs.
func TestSend_TokenNeverAppearsInLogs(t *testing.T) {
	const token = "should-never-be-logged-1234"
	var logs bytes.Buffer
	handler := slog.NewTextHandler(&logs, nil)

	okServer := newRecordingServer(t, http.StatusOK, `{"run_id":"`+uuid.New().String()+`","created":true,"url":"https://x"}`)
	defer okServer.Close()
	failServer := newRecordingServer(t, http.StatusInternalServerError, `{}`)
	defer failServer.Close()

	spoolDir := t.TempDir()
	ok, err := New(okServer.URL, token, spoolDir, handler)
	require.NoError(t, err)
	ok.Send(context.Background(), validRun(t))

	fail, err := New(failServer.URL, token, spoolDir, handler)
	require.NoError(t, err)
	fail.Send(context.Background(), validRun(t))
	fail.RetrySpooled(context.Background())

	assert.NotContains(t, logs.String(), token)
}

// A redirect -- here back to the same host, the case Go's client would
// forward the Authorization header on -- is never followed, so the token
// can't be steered anywhere but the checked report URL.
func TestSend_NeverFollowsARedirect(t *testing.T) {
	var targetHits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetHits.Add(1)
		w.WriteHeader(http.StatusCreated)
	}))
	defer target.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	defer origin.Close()

	run := validRun(t)
	spoolDir := t.TempDir()
	r, err := New(origin.URL, "tok", spoolDir, slog.DiscardHandler)
	require.NoError(t, err)

	result := r.Send(context.Background(), run)
	assert.Nil(t, result.Accepted)
	assert.Contains(t, result.Warning, "redirect")
	assert.Zero(t, targetHits.Load(), "the redirect target must never be contacted")
}

// A rejected token says nothing about the run: once the token is fixed, the
// run should still get through, so it is kept.
func TestSend_UnauthorizedIsSpooledForWhenTheTokenIsFixed(t *testing.T) {
	run := validRun(t)
	server := newRecordingServer(t, http.StatusUnauthorized, `{"error":"the ingest token is not valid"}`)
	defer server.Close()

	spoolDir := t.TempDir()
	r, err := New(server.URL, "tok", spoolDir, slog.DiscardHandler)
	require.NoError(t, err)

	result := r.Send(context.Background(), run)
	assert.Contains(t, result.Warning, "SLOP_REVIEW_REPORT_TOKEN")
	_, err = os.Stat(filepath.Join(serverSpoolDir(spoolDir, server.URL), run.ID.String()+".json"))
	assert.NoError(t, err, "a 401 must be spooled")
}

// A server that echoes the Authorization header into its error must not get
// the token printed into the review's output.
func TestSend_TokenIsRedactedFromServerMessages(t *testing.T) {
	run := validRun(t)
	server := newRecordingServer(t, http.StatusInternalServerError, `bad auth header: Bearer super-secret-token`)
	defer server.Close()

	r, err := New(server.URL, "super-secret-token", t.TempDir(), slog.DiscardHandler)
	require.NoError(t, err)
	result := r.Send(context.Background(), run)
	assert.NotContains(t, result.Warning, "super-secret-token")
	assert.Contains(t, result.Warning, "[redacted]")
}

// An acknowledgement naming some other run is not an acknowledgement of this
// one: the run is kept for retry rather than reported as delivered.
func TestSend_AcknowledgementOfAnotherRunIsAFailure(t *testing.T) {
	run := validRun(t)
	other, err := json.Marshal(report.Accepted{RunID: uuid.New(), Created: true})
	require.NoError(t, err)
	server := newRecordingServer(t, http.StatusCreated, string(other))
	defer server.Close()

	spoolDir := t.TempDir()
	r, err := New(server.URL, "tok", spoolDir, slog.DiscardHandler)
	require.NoError(t, err)
	result := r.Send(context.Background(), run)
	assert.Nil(t, result.Accepted)
	_, err = os.Stat(filepath.Join(serverSpoolDir(spoolDir, server.URL), run.ID.String()+".json"))
	assert.NoError(t, err, "the run must be kept")
}

// MkdirAll leaves an existing directory as it was; a spool that already
// existed with loose permissions must be tightened, since it holds diffs.
func TestSend_TightensAnExistingSpoolDir(t *testing.T) {
	run := validRun(t)
	server := newRecordingServer(t, http.StatusInternalServerError, `{}`)
	defer server.Close()

	spoolDir := t.TempDir()
	dir := serverSpoolDir(spoolDir, server.URL)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.Chmod(dir, 0o755))

	r, err := New(server.URL, "tok", spoolDir, slog.DiscardHandler)
	require.NoError(t, err)
	r.Send(context.Background(), run)

	info, err := os.Stat(dir)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), info.Mode().Perm())
}

// A server that can't be reached at all is the commonest transient failure:
// the run must be kept for next time.
func TestSend_UnreachableServerIsSpooled(t *testing.T) {
	run := validRun(t)
	server := newRecordingServer(t, http.StatusCreated, `{}`)
	url := server.URL
	server.Close()

	spoolDir := t.TempDir()
	r, err := New(url, "tok", spoolDir, slog.DiscardHandler)
	require.NoError(t, err)
	result := r.Send(context.Background(), run)
	assert.Nil(t, result.Accepted)
	assert.Contains(t, result.Warning, "spooled")
	_, err = os.Stat(filepath.Join(serverSpoolDir(spoolDir, url), run.ID.String()+".json"))
	assert.NoError(t, err)
}
