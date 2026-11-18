package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-review/internal/config"
	"github.com/lowaa/slop-factory/slop-review/internal/llm"
	"github.com/lowaa/slop-factory/slop-review/internal/providers"
	"github.com/lowaa/slop-factory/slop-review/report"
)

// setUpCentral is a reporting run whose LLM records what it was asked,
// with the local settings choosing openrouter and SLOP_REVIEW_MODEL, for a
// central config to override.
func setUpCentral(t *testing.T) (*reportServer, *requestRecorder) {
	t.Helper()
	_, llmServer := setUpReportingRepoAndLLM(t)
	llmServer.Close()
	recorder := &requestRecorder{}
	llm := recordingOneFindingLLMServer(recorder)
	t.Cleanup(llm.Close)
	t.Setenv("SLOP_REVIEW_BASE_URL", llm.URL)
	t.Setenv("SLOP_REVIEW_MODEL", "env-model")
	rs := setUpFailedRunReporting(t)
	return rs, recorder
}

const centralOpenRouter = `{"revision":4,"primary":{"provider":"openrouter","model":"central-model","reasoning_effort":""}}`

func runJSON(t *testing.T, args []string, factory *providers.Factory) (output, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	require.NoError(t, runWith(args, &stdout, &stderr, factory), "stderr: %s", stderr.String())
	var got output
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &got))
	return got, stderr.String()
}

// The acceptance case: the tenant's config chooses the model, over the
// machine's own .env, and the report says which revision it ran under.
func TestRun_CentralConfigChoosesTheProviderAndIsReported(t *testing.T) {
	rs, recorder := setUpCentral(t)
	rs.setProviderConfig(http.StatusOK, centralOpenRouter)

	got, _ := runJSON(t, nil, &providers.Factory{})
	require.NotEmpty(t, recorder.all())
	assert.Equal(t, "central-model", recorder.all()[0].Model)
	assert.Equal(t, config.SourceCentral, got.Meta.ProviderSource)
	assert.Equal(t, int64(4), got.Meta.ProviderConfigRevision)
	assert.Equal(t, "openrouter", got.Sessions[0].Provider)

	requests := rs.all()
	require.Len(t, requests, 1)
	run := requests[0].run
	require.NoError(t, run.Validate())
	assert.Equal(t, report.SchemaVersion, run.SchemaVersion)
	assert.Equal(t, int64(4), run.ProviderConfigRevision)
	assert.Equal(t, "central-model", run.Model)
	assert.Equal(t, "central-model", run.Sessions[0].Model)
	assert.Nil(t, run.Fallback)
}

func TestRun_NoCentralConfigRunsOnLocalSettingsAsBefore(t *testing.T) {
	rs, recorder := setUpCentral(t)

	got, _ := runJSON(t, nil, &providers.Factory{})
	assert.Equal(t, "env-model", recorder.all()[0].Model)
	assert.Equal(t, config.SourceLocal, got.Meta.ProviderSource)
	run := rs.all()[0].run
	// v6 for its personas' hashes, but nothing v4 added. No --source-dir
	// was given, so no run-level fingerprint.
	assert.Equal(t, report.FingerprintSchemaVersion, run.SchemaVersion)
	assert.Empty(t, run.Fingerprint)
	assert.NotEmpty(t, run.Sessions[0].PersonaHash)
	assert.Zero(t, run.ProviderConfigRevision)
	assert.Nil(t, run.Fallback)
	assert.Empty(t, run.Sessions[0].Provider)
}

func TestRun_ProviderFlagsWinOverTheCentralConfig(t *testing.T) {
	rs, recorder := setUpCentral(t)
	rs.setProviderConfig(http.StatusOK, centralOpenRouter)

	got, stderr := runJSON(t, []string{"--model=flag-model"}, &providers.Factory{})
	assert.Equal(t, "flag-model", recorder.all()[0].Model)
	assert.Equal(t, config.SourceFlags, got.Meta.ProviderSource)
	assert.Contains(t, stderr, "not the central provider config")
	assert.Zero(t, rs.configFetches)
}

// With the server down, runs keep using the config they last fetched.
func TestRun_ServerDownUsesTheSavedConfig(t *testing.T) {
	rs, recorder := setUpCentral(t)
	rs.setProviderConfig(http.StatusOK, centralOpenRouter)
	runJSON(t, nil, &providers.Factory{})

	rs.setProviderConfig(http.StatusServiceUnavailable, `{"title":"down"}`)
	got, stderr := runJSON(t, nil, &providers.Factory{})
	assert.Equal(t, "central-model", recorder.all()[len(recorder.all())-1].Model)
	assert.Equal(t, config.SourceSaved, got.Meta.ProviderSource)
	assert.Equal(t, int64(4), got.Meta.ProviderConfigRevision)
	assert.Contains(t, stderr, "using the copy saved at")
}

// A config the client can't accept is ignored with a warning, never
// applied -- whatever the server says.
func TestRun_AnInvalidCentralConfigIsIgnoredWithAWarning(t *testing.T) {
	for name, body := range map[string]string{
		"unknown provider": `{"revision":5,"primary":{"provider":"evil-proxy","model":"central-model"}}`,
		"malformed model":  `{"revision":5,"primary":{"provider":"openrouter","model":"central model"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			rs, recorder := setUpCentral(t)
			rs.setProviderConfig(http.StatusOK, body)
			got, stderr := runJSON(t, nil, &providers.Factory{})
			assert.Equal(t, "env-model", recorder.all()[0].Model)
			assert.Equal(t, config.SourceLocal, got.Meta.ProviderSource)
			assert.Contains(t, stderr, "not valid")
		})
	}
}

// notLoggedInPi is a pi that says there's no ChatGPT login.
func notLoggedInPi(t *testing.T) *providers.Factory {
	t.Helper()
	path := filepath.Join(t.TempDir(), "pi")
	require.NoError(t, os.WriteFile(path, []byte("#!/bin/sh\necho '{\"status\":\"not_ready\",\"provider\":\"openai\",\"reason\":\"credentials_not_configured\"}'\nexit 1\n"), 0o755))
	return &providers.Factory{PiCommand: []string{path}}
}

func chatgptFallingBackOn(classes string) string {
	return fmt.Sprintf(`{"revision":6,"primary":{"provider":"chatgpt","model":"","reasoning_effort":""},
		"fallback":{"provider":"openrouter","model":"central-model","reasoning_effort":""},"fallback_on":[%s]}`, classes)
}

// A primary whose login is gone, under a config that falls back on auth:
// the whole run goes to the fallback, and says so everywhere.
func TestRun_APrimaryThatCantStartFallsBackWhenConfiguredTo(t *testing.T) {
	rs, recorder := setUpCentral(t)
	rs.setProviderConfig(http.StatusOK, chatgptFallingBackOn(`"usage_limit","auth"`))

	got, _ := runJSON(t, nil, notLoggedInPi(t))
	assert.Equal(t, "central-model", recorder.all()[0].Model)
	require.NotNil(t, got.Meta.FellBack)
	assert.Equal(t, "auth", got.Meta.FellBack.Class)
	assert.Equal(t, "chatgpt", got.Meta.Provider, "the run's provider is still the primary")
	assert.Equal(t, "openrouter", got.Sessions[0].Provider)
	assert.Contains(t, got.Review.Warnings[0], "fallback provider openrouter")
	assert.Equal(t, got.Review.Warnings, rs.all()[0].run.Warnings, "the report says it fell back too")

	run := rs.all()[0].run
	require.NoError(t, run.Validate())
	host, _ := os.Hostname()
	assert.Equal(t, &report.Fallback{From: "chatgpt", To: "openrouter", Class: report.FailureAuth, Host: host}, run.Fallback)
	assert.Equal(t, "openrouter", run.Sessions[0].Provider)
}

func TestRun_APrimaryThatCantStartFailsWhenItsClassIsNotATrigger(t *testing.T) {
	rs, recorder := setUpCentral(t)
	rs.setProviderConfig(http.StatusOK, chatgptFallingBackOn(`"usage_limit"`))

	err := runWith(nil, &bytes.Buffer{}, &bytes.Buffer{}, notLoggedInPi(t))
	require.ErrorIs(t, err, llm.ErrAuth)
	assert.Empty(t, recorder.all(), "the fallback was never used")
	run := rs.all()[0].run
	assert.Equal(t, report.FailureAuth, run.Failure.Class)
	assert.Nil(t, run.Fallback)
}

func TestNewRoutes_TriggerFollowsTheConfiguredClasses(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "k")
	cfg := &config.Config{
		Route:      config.Route{Provider: "openai", ProviderKind: config.ProviderKindOpenAICompatible, Model: "m"},
		Fallback:   &config.Route{Provider: "openrouter", ProviderKind: config.ProviderKindOpenAICompatible, Model: "fm"},
		FallbackOn: []report.FailureClass{report.FailureCredits, report.FailureUsageLimit},
	}
	rs, err := newRoutes(t.Context(), cfg, &providers.Factory{}, slog.DiscardHandler, &bytes.Buffer{})
	require.NoError(t, err)
	require.NotNil(t, rs.fallback)
	assert.Equal(t, "credits", rs.trigger(fmt.Errorf("session: %w", llm.ErrCredits)))
	assert.Equal(t, "usage_limit", rs.trigger(fmt.Errorf("session: %w", llm.ErrUsageLimit)))
	assert.Empty(t, rs.trigger(fmt.Errorf("session: %w", llm.ErrKeyRejected)), "auth isn't configured")
	assert.Empty(t, rs.trigger(errors.New("out of turns")))
}

// A session the primary fails mid-review, with a class the config falls
// back on, is redone on the fallback -- through the command's own wiring.
func TestRun_ASessionThePrimaryFailsIsRedoneOnTheFallback(t *testing.T) {
	rs, recorder := setUpCentral(t)
	outOfCredits := failingLLMServer(http.StatusPaymentRequired, `{"error":{"message":"Insufficient credits","code":402}}`)
	t.Cleanup(outOfCredits.Close)
	t.Setenv("SLOP_REVIEW_BASE_URL", outOfCredits.URL)
	t.Setenv("OPENAI_API_KEY", "openai-key")
	rs.setProviderConfig(http.StatusOK, `{"revision":8,"primary":{"provider":"openrouter","model":"","reasoning_effort":""},
		"fallback":{"provider":"openai","model":"fallback-model","reasoning_effort":""},"fallback_on":["credits"]}`)
	fallbackLLM := recordingOneFindingLLMServer(recorder)
	t.Cleanup(fallbackLLM.Close)

	got, _ := runJSON(t, nil, &providers.Factory{BaseURLs: map[string]string{"openai": fallbackLLM.URL}})
	require.NotEmpty(t, recorder.all())
	assert.Equal(t, "fallback-model", recorder.all()[0].Model)
	require.NotNil(t, got.Meta.FellBack)
	assert.Equal(t, "credits", got.Meta.FellBack.Class)
	assert.Equal(t, "openai", got.Sessions[0].Provider)

	run := rs.all()[0].run
	require.NoError(t, run.Validate())
	assert.Equal(t, report.FailureCredits, run.Fallback.Class)
	assert.Equal(t, "openai", run.Fallback.To)
	assert.Equal(t, "fallback-model", run.Sessions[0].Model)
}

func TestRun_AnUnreadableSourceDirReportsWithoutAFingerprint(t *testing.T) {
	rs, _ := setUpCentral(t)

	_, stderr := runJSON(t, []string{"--source-dir=" + filepath.Join(t.TempDir(), "missing")}, &providers.Factory{})
	assert.Contains(t, stderr, "without a content fingerprint")
	run := rs.all()[0].run
	require.NoError(t, run.Validate())
	assert.Empty(t, run.Fingerprint)
	assert.Empty(t, run.SourceHash)
}

func TestRun_SourceDirMakesTheReportCarryTheRunFingerprint(t *testing.T) {
	rs, _ := setUpCentral(t)
	src := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(src, "main.go"), []byte("package main\n"), 0o600))

	runJSON(t, []string{"--source-dir=" + src}, &providers.Factory{})
	run := rs.all()[0].run
	require.NoError(t, run.Validate())
	assert.True(t, report.IsHash(run.Fingerprint))
	assert.True(t, report.IsHash(run.SourceHash))
	assert.NotEmpty(t, run.PersonaTexts)
}
