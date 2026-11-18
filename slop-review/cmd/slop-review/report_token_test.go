package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeTokenScript(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "token.sh")
	require.NoError(t, os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o700))
	return path
}

func TestRun_ReportsWithTheTokenFromTheCommand(t *testing.T) {
	_, llmServer := setUpReportingRepoAndLLM(t)
	defer llmServer.Close()
	rs := newReportServer(t, http.StatusCreated, "")
	defer rs.Close()
	t.Setenv("SLOP_REVIEW_REPORT_URL", rs.URL)
	t.Setenv("SLOP_REVIEW_REPORT_TOKEN", "")
	t.Setenv("SLOP_REVIEW_REPORT_TOKEN_COMMAND", writeTokenScript(t, `printf 'from-command\n'`))

	var stdout bytes.Buffer
	require.NoError(t, run(nil, &stdout, &bytes.Buffer{}))

	requests := rs.all()
	require.Len(t, requests, 1)
	assert.Equal(t, "Bearer from-command", requests[0].auth)
}

// A failing token command is a reporting failure: the review is still
// printed, with a warning, and the run is kept for a later invocation.
func TestRun_AFailingTokenCommandOnlyWarns(t *testing.T) {
	_, llmServer := setUpReportingRepoAndLLM(t)
	defer llmServer.Close()
	rs := newReportServer(t, http.StatusCreated, "")
	defer rs.Close()
	t.Setenv("SLOP_REVIEW_REPORT_URL", rs.URL)
	t.Setenv("SLOP_REVIEW_REPORT_TOKEN", "")
	t.Setenv("SLOP_REVIEW_REPORT_TOKEN_COMMAND", writeTokenScript(t, `echo leaked-token; echo "no refresh token" >&2; exit 1`))
	spoolDir := t.TempDir()
	t.Setenv("SLOP_REVIEW_REPORT_SPOOL_DIR", spoolDir)

	var stdout bytes.Buffer
	require.NoError(t, run(nil, &stdout, &bytes.Buffer{}))

	spooled, err := filepath.Glob(filepath.Join(spoolDir, "*", "*.json"))
	require.NoError(t, err)
	assert.Len(t, spooled, 1, "kept for when the command works again")

	var got output
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &got))
	assert.Nil(t, got.Report)
	require.NotEmpty(t, got.Review.Warnings)
	assert.Contains(t, got.Review.Warnings[len(got.Review.Warnings)-1], "no refresh token")
	assert.NotContains(t, stdout.String(), "leaked-token")
	assert.Empty(t, rs.all())
}

func TestRun_AFixedTokenBeatsTheCommandWithAWarning(t *testing.T) {
	_, llmServer := setUpReportingRepoAndLLM(t)
	defer llmServer.Close()
	rs := newReportServer(t, http.StatusCreated, "")
	defer rs.Close()
	t.Setenv("SLOP_REVIEW_REPORT_URL", rs.URL)
	t.Setenv("SLOP_REVIEW_REPORT_TOKEN", "explicit")
	t.Setenv("SLOP_REVIEW_REPORT_TOKEN_COMMAND", writeTokenScript(t, `printf 'from-command'`))

	var stdout, stderr bytes.Buffer
	require.NoError(t, run(nil, &stdout, &stderr))

	requests := rs.all()
	require.Len(t, requests, 1)
	assert.Equal(t, "Bearer explicit", requests[0].auth)
	assert.Equal(t, 1, strings.Count(stderr.String(), "TOKEN_COMMAND is ignored"), "warned once")
}

func TestLabel_AFixedTokenBeatsTheCommandWithAWarning(t *testing.T) {
	srv, reqs := labelAPI(t, http.StatusCreated)
	t.Setenv("SLOP_REVIEW_REPORT_TOKEN", "explicit")
	t.Setenv("SLOP_REVIEW_REPORT_TOKEN_COMMAND", writeTokenScript(t, `printf 'from-command'`))
	var out, errOut bytes.Buffer

	err := run([]string{"label", uuid.NewString(), "--verdict=confirmed", "--report-to=" + srv.URL}, &out, &errOut)

	require.NoError(t, err)
	require.Len(t, *reqs, 1)
	assert.Equal(t, "Bearer explicit", (*reqs)[0].auth)
	assert.Contains(t, errOut.String(), "TOKEN_COMMAND is ignored")
}

func TestLabel_UsesTheTokenFromTheCommandEachTime(t *testing.T) {
	srv, reqs := labelAPI(t, http.StatusCreated)
	t.Setenv("SLOP_REVIEW_REPORT_TOKEN", "")
	t.Setenv("SLOP_REVIEW_REPORT_TOKEN_COMMAND", writeTokenScript(t, `printf 'cmd-token\n\n'`))
	var out, errOut bytes.Buffer

	err := run([]string{"label", uuid.NewString(), "--verdict=confirmed", "--report-to=" + srv.URL}, &out, &errOut)

	require.NoError(t, err)
	require.Len(t, *reqs, 1)
	assert.Equal(t, "Bearer cmd-token", (*reqs)[0].auth)
}

func TestLabel_AFailingTokenCommandWarnsAndSucceeds(t *testing.T) {
	srv, reqs := labelAPI(t, http.StatusCreated)
	t.Setenv("SLOP_REVIEW_REPORT_TOKEN", "")
	t.Setenv("SLOP_REVIEW_REPORT_TOKEN_COMMAND", writeTokenScript(t, `echo "login expired" >&2; exit 1`))
	var out, errOut bytes.Buffer

	err := run([]string{"label", uuid.NewString(), "--verdict=confirmed", "--report-to=" + srv.URL}, &out, &errOut)

	require.NoError(t, err)
	assert.Contains(t, errOut.String(), "label not recorded")
	assert.Contains(t, errOut.String(), "login expired")
	assert.Empty(t, *reqs)
}

func TestLabel_TokenCommandFlag(t *testing.T) {
	srv, reqs := labelAPI(t, http.StatusCreated)
	t.Setenv("SLOP_REVIEW_REPORT_TOKEN", "")
	t.Setenv("SLOP_REVIEW_REPORT_TOKEN_COMMAND", "")
	script := writeTokenScript(t, `printf 'flag-token'`)
	var out, errOut bytes.Buffer

	err := run([]string{"label", uuid.NewString(), "--verdict=confirmed", "--report-to=" + srv.URL, "--report-token-command=" + script}, &out, &errOut)

	require.NoError(t, err)
	require.Len(t, *reqs, 1)
	assert.Equal(t, "Bearer flag-token", (*reqs)[0].auth)
}
