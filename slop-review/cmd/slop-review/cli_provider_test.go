package main

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-review/internal/providers"
)

// fakeClaudeCLI is a claude that is logged in (or not) and answers every
// run with result.
func fakeClaudeCLI(t *testing.T, loggedIn bool, result string) *providers.Factory {
	t.Helper()
	path := filepath.Join(t.TempDir(), "claude")
	status := `{"loggedIn":false}`
	if loggedIn {
		status = `{"loggedIn":true}`
	}
	script := "#!/bin/sh\n[ \"$1 $2\" = \"auth status\" ] && { printf '%s' '" + status + "'; exit 0; }\ncat >/dev/null\nprintf '%s' '" + result + "'\n"
	require.NoError(t, os.WriteFile(path, []byte(script), 0o755))
	// A fake claude runs no sandbox, so needs none of its tools (CI's image
	// has neither bubblewrap nor socat).
	return &providers.Factory{ClaudeCommand: path, ClaudeSandboxTools: []string{}}
}

const claudeReviewResult = `{"is_error":false,"subtype":"success","result":"","structured_output":{"verdict":"approve","summary":"fine","findings":[]},"modelUsage":{"claude-sonnet-5-5":{"inputTokens":100,"outputTokens":20}}}`

const claudeUsageLimitResult = `{"is_error":true,"subtype":"success","result":"You have hit your usage limit"}`

const claudeFallingBackOn = `{"revision":9,"primary":{"provider":"claude","model":"","reasoning_effort":""},
	"fallback":{"provider":"openrouter","model":"central-model","reasoning_effort":""},"fallback_on":[%s]}`

func TestRun_AClaudeRouteReviewsThroughItsCLIAndReportsItsTokens(t *testing.T) {
	rs, recorder := setUpCentral(t)
	t.Setenv("SLOP_REVIEW_BASE_URL", "")
	t.Setenv("SLOP_REVIEW_MODEL", "")

	got, _ := runJSON(t, []string{"--provider=claude"}, fakeClaudeCLI(t, true, claudeReviewResult))
	assert.Empty(t, recorder.all(), "no chat-completions request: Claude Code ran the loop")
	require.Len(t, got.Sessions, 1)
	assert.Equal(t, "claude", got.Sessions[0].Provider)
	assert.Equal(t, int64(120), got.Sessions[0].InputTokens)
	assert.Equal(t, "approve", string(got.Review.Verdict))
	assert.Contains(t, strings.Join(got.Review.Warnings, "\n"), "secret-shaped files")

	run := rs.all()[0].run
	require.NoError(t, run.Validate())
	assert.Equal(t, int64(120), run.Sessions[0].InputTokens)
	assert.Equal(t, got.Review.Warnings, run.Warnings, "the report carries the secrets warning too")
}

// claude may read a secret file itself, but nothing slop-review sends is
// unredacted on its account: a fallback session redoing the work never sees
// the file, and the report is not marked as made with --allow-secrets,
// which the report server would refuse to store.
func TestRun_AClaudePrimaryLeavesTheFallbackRedactedAndTheRunStorable(t *testing.T) {
	rs, recorder := setUpCentral(t)
	rs.setProviderConfig(http.StatusOK, sprintfClasses(`"usage_limit"`))
	repo := os.Getenv("SLOP_REVIEW_REPO_ROOT")
	writeFile(t, repo, ".env", "API_KEY=top-secret-value\n")
	runGit(t, repo, "add", ".env")
	runGit(t, repo, "commit", "-m", "add env")

	got, _ := runJSON(t, nil, fakeClaudeCLI(t, true, claudeUsageLimitResult))
	require.NotNil(t, got.Meta.FellBack)
	require.NotEmpty(t, recorder.all())
	for _, req := range recorder.all() {
		for _, m := range req.Messages {
			assert.NotContains(t, m.Content, "top-secret-value", "the fallback was sent the secret file")
		}
	}
	run := rs.all()[0].run
	assert.False(t, run.Config.AllowSecrets)
	assert.Contains(t, strings.Join(got.Review.Warnings, "\n"), "secret-shaped files")
}

// Falling back works the same with a CLI primary: a usage limit hit
// mid-review redoes the session on the fallback.
func TestRun_ACLISessionThatHitsAUsageLimitIsRedoneOnTheFallback(t *testing.T) {
	rs, recorder := setUpCentral(t)
	rs.setProviderConfig(http.StatusOK, sprintfClasses(`"usage_limit"`))

	got, _ := runJSON(t, nil, fakeClaudeCLI(t, true, claudeUsageLimitResult))
	require.NotEmpty(t, recorder.all())
	assert.Equal(t, "central-model", recorder.all()[0].Model)
	require.NotNil(t, got.Meta.FellBack)
	assert.Equal(t, "usage_limit", got.Meta.FellBack.Class)
	assert.Equal(t, "claude", got.Meta.Provider)
	assert.Equal(t, "openrouter", got.Sessions[0].Provider)
	require.NoError(t, rs.all()[0].run.Validate())
}

// A claude that is not logged in can't start: under a config that falls
// back on auth the whole run goes to the fallback, never to another
// provider on its own.
func TestRun_AClaudeThatIsNotLoggedInFallsBackOnlyWhenConfiguredTo(t *testing.T) {
	rs, recorder := setUpCentral(t)
	rs.setProviderConfig(http.StatusOK, sprintfClasses(`"auth"`))
	got, _ := runJSON(t, nil, fakeClaudeCLI(t, false, claudeReviewResult))
	require.NotNil(t, got.Meta.FellBack)
	assert.Equal(t, "auth", got.Meta.FellBack.Class)
	assert.Equal(t, "openrouter", got.Sessions[0].Provider)
	assert.NotEmpty(t, recorder.all())

	rs.setProviderConfig(http.StatusOK, sprintfClasses(`"usage_limit"`))
	err := runWith(nil, os.Stdout, os.Stderr, fakeClaudeCLI(t, false, claudeReviewResult))
	require.ErrorContains(t, err, "claude auth login")
}

func sprintfClasses(classes string) string { return fmt.Sprintf(claudeFallingBackOn, classes) }
