package reporter

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-review/report"
)

// writeScript writes an executable shell script and returns its path.
func writeScript(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "token.sh")
	require.NoError(t, os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o700))
	return path
}

// countingTokenScript prints tok-1, tok-2, ... one per run, so a test can
// tell which request got which token, and how often the command ran.
func countingTokenScript(t *testing.T) string {
	t.Helper()
	counter := filepath.Join(t.TempDir(), "n")
	return writeScript(t, fmt.Sprintf(`n=$(cat %[1]s 2>/dev/null || echo 0); n=$((n+1)); echo $n > %[1]s; echo "tok-$n"`, counter))
}

func commandSource(t *testing.T, command string) *CommandToken {
	t.Helper()
	src, err := NewCommandToken(command)
	require.NoError(t, err)
	return src
}

func newCommandReporter(t *testing.T, baseURL, command, spoolDir string) *Reporter {
	t.Helper()
	r, err := New(baseURL, "", spoolDir, slog.DiscardHandler, WithTokenSource(commandSource(t, command)))
	require.NoError(t, err)
	return r
}

func acceptedFor(t *testing.T, run *report.Run) string {
	t.Helper()
	b, err := json.Marshal(report.Accepted{RunID: run.ID, Created: true, URL: "https://example/runs/" + run.ID.String()})
	require.NoError(t, err)
	return string(b)
}

func TestCommandToken_OutputIsTrimmed(t *testing.T) {
	for name, out := range map[string]string{
		"trailing newline":   `printf 'abc\n'`,
		"trailing newlines":  `printf 'abc\n\n\n'`,
		"surrounding spaces": `printf '  abc \r\n'`,
		"no newline":         `printf abc`,
	} {
		t.Run(name, func(t *testing.T) {
			got, err := commandSource(t, writeScript(t, out)).Token(context.Background())
			require.NoError(t, err)
			assert.Equal(t, "abc", got)
		})
	}
}

func TestCommandToken_ArgumentsAreSplitOnWhitespaceAndNotShellInterpreted(t *testing.T) {
	script := writeScript(t, `printf '%s-%s' "$1" "$2"`)

	got, err := commandSource(t, script+"  one   $HOME").Token(context.Background())

	require.NoError(t, err)
	assert.Equal(t, "one-$HOME", got, "extra whitespace separates arguments, and $HOME is not expanded")
}

func TestCommandToken_AnExecutablePathWithSpacesIsNotSplit(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "My Workspace")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	script := filepath.Join(dir, "token.sh")
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\nprintf spaced\n"), 0o700))

	got, err := commandSource(t, script).Token(context.Background())

	require.NoError(t, err)
	assert.Equal(t, "spaced", got)
}

func TestCommandToken_BadOutputIsAnError(t *testing.T) {
	for name, tc := range map[string]struct{ script, want string }{
		"empty":                {`true`, "printed nothing"},
		"only whitespace":      {`printf '\n  \n'`, "printed nothing"},
		"several words":        {`printf 'two words'`, "more than a single token"},
		"several lines":        {`printf 'a\nb\n'`, "more than a single token"},
		"control character":    {`printf 'a\001b'`, "more than a single token"},
		"fails":                {`echo diagnostics >&2; exit 3`, "diagnostics"},
		"fails with no stderr": {`exit 3`, "exit status 3"},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := commandSource(t, writeScript(t, tc.script)).Token(context.Background())
			require.Error(t, err)
			assert.Empty(t, got)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

func TestCommandToken_ErrorsNeverQuoteTheOutputOrTheTokenInStderr(t *testing.T) {
	// A command that prints a token and then fails, with stderr echoing it:
	// neither may reach the error.
	script := writeScript(t, `echo super-secret-token; echo "debug: using super-secret-token" >&2; exit 1`)

	_, err := commandSource(t, script).Token(context.Background())

	require.Error(t, err)
	assert.NotContains(t, err.Error(), "super-secret-token")
	assert.Contains(t, err.Error(), "debug: using [redacted]")
}

func TestCommandToken_MoreThanATokenIsNotQuoted(t *testing.T) {
	_, err := commandSource(t, writeScript(t, `echo "secret-one secret-two"`)).Token(context.Background())

	require.Error(t, err)
	assert.NotContains(t, err.Error(), "secret")
}

func TestCommandToken_RunawayOutputIsRefused(t *testing.T) {
	script := writeScript(t, `head -c 100000 /dev/zero | tr '\0' a`)

	_, err := commandSource(t, script).Token(context.Background())

	assert.ErrorContains(t, err, "not a token")
}

func TestCommandToken_AHungCommandTimesOut(t *testing.T) {
	src := commandSource(t, writeScript(t, `sleep 30`))
	src.timeout = 100 * time.Millisecond

	_, err := src.Token(context.Background())

	assert.ErrorContains(t, err, "timed out")
}

func TestCommandToken_MissingExecutableIsAnError(t *testing.T) {
	_, err := commandSource(t, filepath.Join(t.TempDir(), "nope")).Token(context.Background())
	assert.ErrorContains(t, err, "failed")
}

func TestNewCommandToken_EmptyCommandIsRefused(t *testing.T) {
	for _, c := range []string{"", "   \t "} {
		_, err := NewCommandToken(c)
		assert.Error(t, err, "%q", c)
	}
}

// acceptingServer accepts whichever run it is sent, recording the
// Authorization header of each request.
func acceptingServer(t *testing.T) *recordingServer {
	t.Helper()
	server := newRecordingServer(t, http.StatusOK, "")
	server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var run report.Run
		_ = json.NewDecoder(req.Body).Decode(&run)
		server.requests = append(server.requests, capturedRequest{auth: req.Header.Get("Authorization")})
		_, _ = fmt.Fprint(w, acceptedFor(t, &run))
	})
	t.Cleanup(server.Close)
	return server
}

func TestSend_CommandIsRunForEveryRequestSoARefreshedTokenIsUsed(t *testing.T) {
	server := acceptingServer(t)
	r := newCommandReporter(t, server.URL, countingTokenScript(t), t.TempDir())

	require.NotNil(t, r.Send(context.Background(), validRun(t)).Accepted)
	require.NotNil(t, r.Send(context.Background(), validRun(t)).Accepted)

	require.Len(t, server.requests, 2)
	assert.Equal(t, "Bearer tok-1", server.requests[0].auth)
	assert.Equal(t, "Bearer tok-2", server.requests[1].auth)
}

func TestRetrySpooledAndSend_EachRunTheCommand(t *testing.T) {
	spooled := validRun(t)
	spoolDir := t.TempDir()
	server := acceptingServer(t)
	path := filepath.Join(serverSpoolDir(spoolDir, server.URL), spooled.ID.String()+".json")
	body, err := json.Marshal(spooled)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, body, 0o600))
	r := newCommandReporter(t, server.URL, countingTokenScript(t), spoolDir)

	r.RetrySpooled(context.Background())
	require.NotNil(t, r.Send(context.Background(), validRun(t)).Accepted)

	require.Len(t, server.requests, 2)
	assert.Equal(t, "Bearer tok-1", server.requests[0].auth)
	assert.Equal(t, "Bearer tok-2", server.requests[1].auth)
	assert.NoFileExists(t, path)
}

func TestSend_FailingCommandSpoolsTheRunAndWarnsWithoutTheToken(t *testing.T) {
	run := validRun(t)
	server := newRecordingServer(t, http.StatusCreated, acceptedFor(t, run))
	defer server.Close()
	spoolDir := t.TempDir()
	r := newCommandReporter(t, server.URL, writeScript(t, `echo super-secret-token; echo "refresh failed for super-secret-token" >&2; exit 1`), spoolDir)

	result := r.Send(context.Background(), run)

	assert.Nil(t, result.Accepted)
	assert.Contains(t, result.Warning, "run not reported")
	assert.Contains(t, result.Warning, "refresh failed for [redacted]")
	assert.NotContains(t, result.Warning, "super-secret-token")
	assert.Empty(t, server.requests, "nothing is sent without a token")
	assert.FileExists(t, filepath.Join(serverSpoolDir(spoolDir, server.URL), run.ID.String()+".json"), "kept for when the command works again")
}

func TestSend_EmptyCommandOutputIsAReportingFailureNotAnUnauthenticatedPost(t *testing.T) {
	run := validRun(t)
	server := newRecordingServer(t, http.StatusCreated, acceptedFor(t, run))
	defer server.Close()
	r := newCommandReporter(t, server.URL, writeScript(t, `true`), t.TempDir())

	result := r.Send(context.Background(), run)

	assert.Nil(t, result.Accepted)
	assert.Contains(t, result.Warning, "printed nothing")
	assert.Empty(t, server.requests)
}

func TestRetrySpooled_StopsAtTheFirstFailureToGetAToken(t *testing.T) {
	spoolDir := t.TempDir()
	server := newRecordingServer(t, http.StatusOK, "")
	defer server.Close()
	dir := serverSpoolDir(spoolDir, server.URL)
	require.NoError(t, os.MkdirAll(dir, 0o700))
	for range 3 {
		body, err := json.Marshal(validRun(t))
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(dir, uuid.NewString()+".json"), body, 0o600))
	}
	counter := filepath.Join(t.TempDir(), "n")
	r := newCommandReporter(t, server.URL, writeScript(t, fmt.Sprintf(`echo x >> %s; exit 1`, counter)), spoolDir)

	r.RetrySpooled(context.Background())

	runs, err := os.ReadFile(counter)
	require.NoError(t, err)
	assert.Equal(t, "x\n", string(runs), "one failure is enough: every other run would share it")
	left, err := filepath.Glob(filepath.Join(dir, "*.json"))
	require.NoError(t, err)
	assert.Len(t, left, 3)
}

func TestLabelAndFetchProviderConfig_UseAFreshTokenEach(t *testing.T) {
	var auths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		auths = append(auths, req.Header.Get("Authorization"))
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	r := newCommandReporter(t, srv.URL, countingTokenScript(t), t.TempDir())

	_, _ = r.FetchProviderConfig(context.Background())
	_ = r.Label(context.Background(), uuid.New(), &LabelRequest{Verdict: "confirmed"})

	assert.Equal(t, []string{"Bearer tok-1", "Bearer tok-2"}, auths)
}

func TestLabelAndFetchProviderConfig_FailingCommandIsAnErrorWithoutARequest(t *testing.T) {
	var requests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) { requests++ }))
	defer srv.Close()
	r := newCommandReporter(t, srv.URL, writeScript(t, `exit 1`), t.TempDir())

	_, err := r.FetchProviderConfig(context.Background())
	assert.ErrorContains(t, err, "token command")
	err = r.Label(context.Background(), uuid.New(), &LabelRequest{Verdict: "confirmed"})
	assert.ErrorContains(t, err, "token command")
	assert.Zero(t, requests)
}

// A server may echo back a token sent in an earlier request, which the
// current one no longer is.
func TestSend_AnEarlierTokenIsRedactedFromServerMessages(t *testing.T) {
	run := validRun(t)
	server := newRecordingServer(t, http.StatusOK, "")
	defer server.Close()
	calls := 0
	server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = fmt.Fprint(w, "stale credential tok-1")
	})
	r := newCommandReporter(t, server.URL, countingTokenScript(t), t.TempDir())

	_ = r.Send(context.Background(), run)
	result := r.Send(context.Background(), run)

	assert.NotContains(t, result.Warning, "tok-1")
	assert.Contains(t, result.Warning, "[redacted]")
}
