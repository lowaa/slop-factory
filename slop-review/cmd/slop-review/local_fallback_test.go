package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-review/internal/config"
	"github.com/lowaa/slop-factory/slop-review/internal/providers"
)

// localFallback is a run that reports nowhere, with a claude primary and an
// openrouter fallback named only in a .env file, the fallback's endpoint a
// recording fake.
type localFallback struct {
	envFile  string // the --env-file argument
	recorder *requestRecorder
	url      string
}

func setUpLocalFallback(t *testing.T, fallbackOn string) *localFallback {
	t.Helper()
	_, llmServer := setUpReportingRepoAndLLM(t)
	llmServer.Close()
	t.Setenv("SLOP_REVIEW_PROVIDER", "")
	t.Setenv("SLOP_REVIEW_BASE_URL", "")
	recorder := &requestRecorder{}
	fallbackLLM := recordingOneFindingLLMServer(recorder)
	t.Cleanup(fallbackLLM.Close)

	text := "SLOP_REVIEW_PROVIDER=claude\nSLOP_REVIEW_FALLBACK_PROVIDER=openrouter\nSLOP_REVIEW_FALLBACK_MODEL=fallback-model\n"
	if fallbackOn != "" {
		text += "SLOP_REVIEW_FALLBACK_ON=" + fallbackOn + "\n"
	}
	path := filepath.Join(t.TempDir(), ".env")
	require.NoError(t, os.WriteFile(path, []byte(text), 0o600))
	return &localFallback{envFile: "--env-file=" + path, recorder: recorder, url: fallbackLLM.URL}
}

// factory points f's openrouter fallback at the recording fake.
func (lf *localFallback) factory(f *providers.Factory) *providers.Factory {
	f.BaseURLs = map[string]string{"openrouter": lf.url}
	return f
}

// A primary that hits a usage limit mid-review, under the default trigger,
// has its session redone on the local fallback; the output says so.
func TestRun_ALocalFallbackRedoesASessionThePrimaryFailsWithATrigger(t *testing.T) {
	lf := setUpLocalFallback(t, "")

	got, stderr := runJSON(t, []string{lf.envFile}, lf.factory(fakeClaudeCLI(t, true, claudeUsageLimitResult)))
	require.NotEmpty(t, lf.recorder.all(), "the fallback reviewed")
	assert.Equal(t, "fallback-model", lf.recorder.all()[0].Model)
	assert.Equal(t, config.SourceLocal, got.Meta.ProviderSource)
	assert.Equal(t, "claude", got.Meta.Provider, "the run's provider is still the primary")
	require.NotNil(t, got.Meta.FellBack)
	assert.Equal(t, "claude", got.Meta.FellBack.From)
	assert.Equal(t, "openrouter", got.Meta.FellBack.To)
	assert.Equal(t, "usage_limit", got.Meta.FellBack.Class)
	require.Len(t, got.Sessions, 1)
	assert.Equal(t, "openrouter", got.Sessions[0].Provider)
	assert.Equal(t, "fallback-model", got.Sessions[0].Model)
	assert.Contains(t, got.Review.Warnings[0], "claude failed (usage_limit)")
	assert.Contains(t, stderr, "retrying on the fallback")
	assert.Contains(t, stderr, "SLOP_REVIEW_FALLBACK_PROVIDER", "the keys taken from the .env are named")
}

// A primary that can't start for a class that is not configured fails the
// run, as with no fallback at all; listed, the same failure takes it.
func TestRun_ALocalFallbackIsTakenOnlyOnItsClasses(t *testing.T) {
	lf := setUpLocalFallback(t, "")
	err := runWith([]string{lf.envFile}, &bytes.Buffer{}, &bytes.Buffer{}, lf.factory(fakeClaudeCLI(t, false, claudeReviewResult)))
	require.ErrorContains(t, err, "claude auth login")
	assert.Empty(t, lf.recorder.all(), "auth is not in the default trigger")

	lf = setUpLocalFallback(t, "usage_limit,auth")
	got, stderr := runJSON(t, []string{lf.envFile}, lf.factory(fakeClaudeCLI(t, false, claudeReviewResult)))
	assert.NotEmpty(t, lf.recorder.all())
	require.NotNil(t, got.Meta.FellBack)
	assert.Equal(t, "auth", got.Meta.FellBack.Class)
	assert.Contains(t, stderr, "every session runs on the fallback openrouter")
}

// A one-off run leaves the .env's fallback out: its usage limit fails the
// run instead of switching provider.
func TestRun_AOneOffRunHasNoLocalFallback(t *testing.T) {
	lf := setUpLocalFallback(t, "")
	var stderr bytes.Buffer
	err := runWith([]string{lf.envFile, "--provider=claude"}, &bytes.Buffer{}, &stderr, lf.factory(fakeClaudeCLI(t, true, claudeUsageLimitResult)))
	require.ErrorContains(t, err, "usage limit")
	assert.Empty(t, lf.recorder.all())
	assert.Contains(t, stderr.String(), "without the SLOP_REVIEW_FALLBACK_* fallback")
}
