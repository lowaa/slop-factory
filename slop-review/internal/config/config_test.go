package config

import (
	"context"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	llmusage "github.com/lowaa/slop-factory/go-llm-usage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-review/report"
	"github.com/lowaa/slop-factory/slop-review/report/providerconfig"
	"github.com/lowaa/slop-factory/slop-review/report/reporter"
)

func TestLoad_Defaults(t *testing.T) {
	// Guard against the host's own choices (e.g. a sourced .env) leaking in.
	t.Setenv("OPENROUTER_API_KEY", "")
	t.Setenv("SLOP_REVIEW_PROVIDER", "")
	t.Setenv("SLOP_REVIEW_BASE_URL", "")
	t.Setenv("SLOP_REVIEW_MODEL", "")
	t.Setenv("SLOP_REVIEW_REASONING_EFFORT", "")

	cfg, err := Load(nil)
	require.NoError(t, err)

	assert.Equal(t, "claude", cfg.Provider)
	assert.Equal(t, ProviderKindClaude, cfg.ProviderKind)
	assert.Empty(t, cfg.BaseURL, "the Claude Code CLI picks its own endpoint")
	assert.Empty(t, cfg.APIKey, "the default provider needs no API key")
	assert.Equal(t, "claude-sonnet-5-5", cfg.Model)
	assert.Equal(t, "xhigh", cfg.ReasoningEffort, "claude takes xhigh, the single pass's effort")
	assert.Equal(t, "standard", cfg.Persona)
	assert.Equal(t, "personas", cfg.PersonaDir)
	assert.Equal(t, ".", cfg.RepoRoot)
	assert.Equal(t, "", cfg.FromCommitHash)
	assert.Equal(t, 40, cfg.MaxTurns)
	assert.Equal(t, 8000, cfg.ToolOutputCapBytes)
	assert.Equal(t, 1000000, cfg.MaxDiffBytes)
	assert.Equal(t, 1000000, cfg.ChunkDiffBytes)
	assert.Equal(t, 2000000, cfg.ContextBudgetBytes)
	assert.Equal(t, 4, cfg.Parallelism)
	assert.True(t, cfg.SeamPass)
	assert.False(t, cfg.AllowSecrets)
}

// The one-setting switch back to OpenRouter the docs promise: the env var
// alone restores the pre-chatgpt configuration.
func TestLoad_ProviderEnvRestoresOpenRouter(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "test-key")
	t.Setenv("SLOP_REVIEW_PROVIDER", "openrouter")
	t.Setenv("SLOP_REVIEW_BASE_URL", "")
	t.Setenv("SLOP_REVIEW_MODEL", "")
	t.Setenv("SLOP_REVIEW_REASONING_EFFORT", "")

	cfg, err := Load(nil)
	require.NoError(t, err)

	assert.Equal(t, "openrouter", cfg.Provider)
	assert.Equal(t, ProviderKindOpenAICompatible, cfg.ProviderKind)
	assert.Equal(t, "https://openrouter.ai/api/v1", cfg.BaseURL)
	assert.Equal(t, "test-key", cfg.APIKey)
	assert.Equal(t, "openai/gpt-5.6-luna", cfg.Model)
	assert.Empty(t, cfg.ReasoningEffort)
}

func TestLoad_FlagsOverrideEnv(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "env-key")
	t.Setenv("SLOP_REVIEW_PROVIDER", "openai")
	t.Setenv("SLOP_REVIEW_MODEL", "env-model")

	cfg, err := Load([]string{"--model=flag-model"})
	require.NoError(t, err)

	assert.Equal(t, "openai", cfg.Provider, "env var applies when no flag given")
	assert.Equal(t, "https://api.openai.com/v1", cfg.BaseURL)
	assert.Equal(t, "env-key", cfg.APIKey)
	assert.Equal(t, "flag-model", cfg.Model, "flag takes precedence over env var")
}

func TestLoad_BaseURLOverride(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "test-key")

	cfg, err := Load([]string{"--provider=openrouter", "--base-url=https://gateway.internal/v1"})
	require.NoError(t, err)

	assert.Equal(t, "https://gateway.internal/v1", cfg.BaseURL)
}

func TestLoad_ModelDefaultsPerProvider(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "test-key")
	t.Setenv("OPENAI_API_KEY", "test-key")
	t.Setenv("SLOP_REVIEW_MODEL", "")

	for provider, model := range map[string]string{
		"openrouter": "openai/gpt-5.6-luna",
		"openai":     "gpt-5.6-luna",
		"chatgpt":    "gpt-5.6-luna",
	} {
		cfg, err := Load([]string{"--provider=" + provider})
		require.NoError(t, err)
		assert.Equal(t, model, cfg.Model, provider)

		cfg, err = Load([]string{"--provider=" + provider, "--model=other"})
		require.NoError(t, err)
		assert.Equal(t, "other", cfg.Model, "--model still overrides %s's default", provider)
	}
}

func TestLoad_ReasoningEffortRejectedWhereEffortIsChosenBySlug(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "test-key")

	_, err := Load([]string{"--provider=openrouter", "--reasoning-effort=high"})
	assert.ErrorContains(t, err, "SLOP_REVIEW_REASONING_EFFORT")
	assert.ErrorContains(t, err, "openai/gpt-5.6-luna-pro", "the error names the slug to use instead")
}

func TestLoad_ChatGPTNeedsNoAPIKeyAndTakesReasoningEffort(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("SLOP_REVIEW_MODEL", "")

	cfg, err := Load([]string{"--provider=chatgpt", "--reasoning-effort=high"})
	require.NoError(t, err)
	assert.Equal(t, ProviderKindChatGPT, cfg.ProviderKind)
	assert.Equal(t, "https://api.openai.com/v1", cfg.BaseURL)
	assert.Empty(t, cfg.APIKey, "the token comes from pi, never from the environment")
	assert.Equal(t, "gpt-5.6-luna", cfg.Model)
	assert.Equal(t, "high", cfg.ReasoningEffort)

	_, err = Load([]string{"--provider=chatgpt", "--reasoning-effort=extreme"})
	assert.ErrorContains(t, err, "SLOP_REVIEW_REASONING_EFFORT")
}

func TestLoad_ChatGPTTokenCannotBeRedirected(t *testing.T) {
	t.Setenv("SLOP_REVIEW_BASE_URL", "https://gateway.internal/v1")
	_, err := Load([]string{"--provider=chatgpt"})
	assert.ErrorContains(t, err, "SLOP_REVIEW_BASE_URL")

	t.Setenv("SLOP_REVIEW_BASE_URL", "")
	_, err = Load([]string{"--provider=chatgpt", "--base-url=https://evil.example/v1"})
	assert.ErrorContains(t, err, "https://api.openai.com/v1")
}

// withProfile registers a profile for one test, for behaviour no shipped
// profile exercises yet.
func withProfile(t *testing.T, name string, p providerProfile, spec providerconfig.Provider) {
	t.Helper()
	providerProfiles[name] = p
	providerconfig.Providers[name] = spec
	t.Cleanup(func() {
		delete(providerProfiles, name)
		delete(providerconfig.Providers, name)
	})
}

func TestLoad_ReasoningEffortForAProviderThatTakesIt(t *testing.T) {
	withProfile(t, "effortful", providerProfile{kind: "test"}, providerconfig.Provider{DefaultModel: "m", ReasoningEffort: true})

	cfg, err := Load([]string{"--provider=effortful", "--reasoning-effort=high"})
	require.NoError(t, err)
	assert.Equal(t, "high", cfg.ReasoningEffort)
	assert.Empty(t, cfg.APIKey, "a profile without an API key env var needs no key")

	t.Setenv("SLOP_REVIEW_REASONING_EFFORT", "low")
	cfg, err = Load([]string{"--provider=effortful"})
	require.NoError(t, err)
	assert.Equal(t, "low", cfg.ReasoningEffort)

	_, err = Load([]string{"--provider=effortful", "--reasoning-effort=extreme"})
	assert.ErrorContains(t, err, "SLOP_REVIEW_REASONING_EFFORT")
}

// The default effort goes only to providers that take it, and is never a
// reason to refuse a provider that doesn't.
func TestLoad_DefaultEffortIsXHighWhereTheProviderTakesIt(t *testing.T) {
	t.Setenv("SLOP_REVIEW_REASONING_EFFORT", "")
	t.Setenv("SLOP_REVIEW_MODEL", "")
	t.Setenv("SLOP_REVIEW_BASE_URL", "")
	t.Setenv("OPENROUTER_API_KEY", "k")
	t.Setenv("OPENAI_API_KEY", "k")
	withProfile(t, "lowonly", providerProfile{kind: "test"}, providerconfig.Provider{DefaultModel: "m", ReasoningEffort: true, Efforts: []string{"low"}})
	want := map[string]string{
		"claude":     "xhigh",
		"codex":      "xhigh",
		"chatgpt":    "xhigh",
		"openrouter": "",
		"openai":     "",
		"lowonly":    "",
	}
	for provider, effort := range want {
		cfg, err := Load([]string{"--provider=" + provider})
		require.NoError(t, err, provider)
		assert.Equal(t, effort, cfg.ReasoningEffort, provider)
	}

	cfg, err := Load([]string{"--provider=claude", "--reasoning-effort=low"})
	require.NoError(t, err)
	assert.Equal(t, "low", cfg.ReasoningEffort, "a chosen effort beats the default")
}

// A central choice that leaves effort empty gets the same default, primary
// and fallback alike.
func TestLoadWith_CentralRoutesWithoutAnEffortGetTheDefault(t *testing.T) {
	reporting(t)
	t.Setenv("OPENROUTER_API_KEY", "k")
	cfg, err := LoadWith(context.Background(), nil, centralConfig(providerconfig.Settings{
		Primary:    providerconfig.Choice{Provider: "claude"},
		Fallback:   &providerconfig.Choice{Provider: "openrouter"},
		FallbackOn: []report.FailureClass{report.FailureUsageLimit},
	}))
	require.NoError(t, err)
	assert.Equal(t, "xhigh", cfg.ReasoningEffort)
	require.NotNil(t, cfg.Fallback)
	assert.Empty(t, cfg.Fallback.ReasoningEffort, "openrouter selects effort by slug")
}

func TestLoad_UnknownProvider(t *testing.T) {
	_, err := Load([]string{"--provider=made-up"})
	assert.ErrorContains(t, err, "SLOP_REVIEW_PROVIDER")
}

func TestLoad_MissingAPIKey(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "") // guard against a real key leaking in from the host env

	_, err := Load([]string{"--provider=openrouter"})
	assert.ErrorContains(t, err, "OPENROUTER_API_KEY")
}

func TestLoad_InvalidMaxTurns(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "test-key")

	_, err := Load([]string{"--max-turns=not-a-number"})
	assert.ErrorContains(t, err, "SLOP_REVIEW_MAX_TURNS")
}

func TestLoad_NonPositiveMaxTurns(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "test-key")

	_, err := Load([]string{"--max-turns=0"})
	assert.ErrorContains(t, err, "SLOP_REVIEW_MAX_TURNS")
}

func TestLoad_InvalidMaxDiffBytes(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "test-key")

	_, err := Load([]string{"--max-diff-bytes=not-a-number"})
	assert.ErrorContains(t, err, "SLOP_REVIEW_MAX_DIFF_BYTES")
}

func TestLoad_NonPositiveMaxDiffBytes(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "test-key")

	_, err := Load([]string{"--max-diff-bytes=0"})
	assert.ErrorContains(t, err, "SLOP_REVIEW_MAX_DIFF_BYTES")
}

func TestLoad_AllowSecretsFlag(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "test-key")

	cfg, err := Load([]string{"--allow-secrets=true"})
	require.NoError(t, err)
	assert.True(t, cfg.AllowSecrets)
}

func TestLoad_OrchestrationFlags(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "test-key")

	cfg, err := Load([]string{"--chunk-diff-bytes=1000", "--max-diff-bytes=1500", "--context-budget-bytes=2000", "--parallelism=2", "--seam-pass=false"})
	require.NoError(t, err)
	assert.Equal(t, 1000, cfg.ChunkDiffBytes)
	assert.Equal(t, 2000, cfg.ContextBudgetBytes)
	assert.Equal(t, 2, cfg.Parallelism)
	assert.False(t, cfg.SeamPass)
}

func TestLoad_RejectsNonPositiveOrchestrationSizes(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "test-key")

	for _, flag := range []string{"chunk-diff-bytes", "context-budget-bytes", "parallelism"} {
		_, err := Load([]string{"--" + flag + "=0"})
		assert.ErrorContains(t, err, "must be positive", flag)
	}
	_, err := Load([]string{"--seam-pass=maybe"})
	assert.ErrorContains(t, err, "SLOP_REVIEW_SEAM_PASS")
}

func TestLoad_OrchestrationSettingsFromEnv(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "test-key")
	t.Setenv("SLOP_REVIEW_CHUNK_DIFF_BYTES", "1111")
	t.Setenv("SLOP_REVIEW_MAX_DIFF_BYTES", "2222")
	t.Setenv("SLOP_REVIEW_CONTEXT_BUDGET_BYTES", "3333")
	t.Setenv("SLOP_REVIEW_PARALLELISM", "7")
	t.Setenv("SLOP_REVIEW_SEAM_PASS", "false")

	cfg, err := Load(nil)
	require.NoError(t, err)
	assert.Equal(t, 1111, cfg.ChunkDiffBytes)
	assert.Equal(t, 2222, cfg.MaxDiffBytes)
	assert.Equal(t, 3333, cfg.ContextBudgetBytes)
	assert.Equal(t, 7, cfg.Parallelism)
	assert.False(t, cfg.SeamPass)

	cfg, err = Load([]string{"--parallelism=3", "--seam-pass=true"})
	require.NoError(t, err)
	assert.Equal(t, 3, cfg.Parallelism, "a flag still beats the env var")
	assert.True(t, cfg.SeamPass)
}

func TestLoad_ContextBudgetMustHoldTheInlineDiff(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "test-key")

	_, err := Load([]string{"--max-diff-bytes=5000", "--context-budget-bytes=5000"})
	assert.ErrorContains(t, err, "SLOP_REVIEW_CONTEXT_BUDGET_BYTES")
}

func TestLoad_ReportingUnsetByDefault(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "test-key")
	// A developer's shell often has these from the repo's .env; without
	// clearing them the test fails, and its failure message prints the token.
	t.Setenv("SLOP_REVIEW_REPORT_URL", "")
	t.Setenv("SLOP_REVIEW_REPORT_TOKEN", "")
	t.Setenv("SLOP_REVIEW_REPORT_TOKEN_COMMAND", "")

	cfg, err := Load(nil)
	require.NoError(t, err)
	assert.Empty(t, cfg.ReportURL)
	assert.Empty(t, cfg.ReportToken)
	assert.Empty(t, cfg.ReportSpoolDir, "no spool dir is resolved when there's nowhere to report to")
}

func TestLoad_ReportRemoteDefaultsToOrigin(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "test-key")

	cfg, err := Load(nil)
	require.NoError(t, err)
	assert.Equal(t, "origin", cfg.ReportRemote)
	assert.Empty(t, cfg.ReportRepo)
}

func TestLoad_ReportRemoteFlag(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "test-key")

	cfg, err := Load([]string{"--report-remote=upstream"})
	require.NoError(t, err)
	assert.Equal(t, "upstream", cfg.ReportRemote)
}

func TestLoad_ReportRemoteFromEnv(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "test-key")
	t.Setenv("SLOP_REVIEW_REPORT_REMOTE", "upstream")

	cfg, err := Load(nil)
	require.NoError(t, err)
	assert.Equal(t, "upstream", cfg.ReportRemote)
}

func TestLoad_ReportRepoFlagOverridesRemoteURL(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "test-key")

	cfg, err := Load([]string{"--report-repo=git@github.com:acme/widget.git"})
	require.NoError(t, err)
	assert.Equal(t, "git@github.com:acme/widget.git", cfg.ReportRepo)
	// --report-repo overrides the URL outright; it doesn't change which
	// remote --report-remote still names for ResolveScope to read.
	assert.Equal(t, "origin", cfg.ReportRemote)
}

func TestLoad_ReportTokenIsEnvOnlyNeverAFlag(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "test-key")
	t.Setenv("SLOP_REVIEW_REPORT_TOKEN", "env-token")

	// pflag rejects an unregistered flag outright, which is the point:
	// there must be no --report-token flag to register in the first
	// place.
	_, err := Load([]string{"--report-token=flag-token"})
	assert.ErrorContains(t, err, "report-token")

	cfg, err := Load(nil)
	require.NoError(t, err)
	assert.Equal(t, "env-token", cfg.ReportToken)
}

func TestLoad_ReportURLResolvesDefaultSpoolDir(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "test-key")

	cfg, err := Load([]string{"--report-to=https://example.test"})
	require.NoError(t, err)
	assert.Equal(t, "https://example.test", cfg.ReportURL)
	assert.Contains(t, cfg.ReportSpoolDir, "slop-review")
	assert.Contains(t, cfg.ReportSpoolDir, "spool")
}

func TestLoad_ReportSpoolDirFlagOverridesDefault(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "test-key")

	cfg, err := Load([]string{"--report-to=https://example.test", "--report-spool-dir=/tmp/custom-spool"})
	require.NoError(t, err)
	assert.Equal(t, "/tmp/custom-spool", cfg.ReportSpoolDir)
}

func TestLoad_ToolVersionFlag(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "test-key")

	cfg, err := Load([]string{"--tool-version=abc1234"})
	require.NoError(t, err)
	assert.Equal(t, "abc1234", cfg.ToolVersion)
	assert.False(t, cfg.ToolVersionDirty, "defaults to clean")

	cfg, err = Load([]string{"--tool-version=abc1234", "--tool-version-dirty=true"})
	require.NoError(t, err)
	assert.True(t, cfg.ToolVersionDirty)
}

func TestLoad_BenchFlags(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "test-key")

	cfg, err := Load([]string{"--bench-case=c1", "--bench-variant=chunked", "--bench-run=3"})
	require.NoError(t, err)
	assert.Equal(t, "c1", cfg.BenchCase)
	assert.Equal(t, "chunked", cfg.BenchVariant)
	assert.Equal(t, 3, cfg.BenchRun)
}

func TestLoad_BenchRunDefaultsToZero(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "test-key")

	cfg, err := Load(nil)
	require.NoError(t, err)
	assert.Equal(t, 0, cfg.BenchRun)
}

func TestLoad_BenchRunRejectsNegative(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "test-key")

	_, err := Load([]string{"--bench-run=-1"})
	assert.ErrorContains(t, err, "SLOP_REVIEW_BENCH_RUN")
}

func TestLoad_BenchRunRejectsNonInteger(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "test-key")

	_, err := Load([]string{"--bench-run=not-a-number"})
	assert.ErrorContains(t, err, "SLOP_REVIEW_BENCH_RUN")
}

// fakeCentral serves a fixed central lookup and counts how often it's asked.
type fakeCentral struct {
	result *Central
	tokens reporter.TokenSource
	calls  int
}

func (f *fakeCentral) Lookup(_ context.Context, _ string, tokens reporter.TokenSource, _ string) *Central {
	f.tokens = tokens
	f.calls++
	return f.result
}

func centralConfig(settings providerconfig.Settings) *fakeCentral {
	return &fakeCentral{result: &Central{
		Config: &providerconfig.Config{Revision: 7, Settings: settings},
		Source: SourceCentral,
	}}
}

// reporting sets up a run that reports, which is what a central config
// is looked up for, with the host's own provider settings cleared.
func reporting(t *testing.T) {
	t.Helper()
	t.Setenv("SLOP_REVIEW_REPORT_URL", "https://slop-review.example")
	t.Setenv("SLOP_REVIEW_REPORT_TOKEN", "tok")
	t.Setenv("SLOP_REVIEW_REPORT_TOKEN_COMMAND", "")
	t.Setenv("SLOP_REVIEW_PROVIDER_CONFIG_CACHE_DIR", t.TempDir())
	for _, k := range []string{"SLOP_REVIEW_PROVIDER", "SLOP_REVIEW_BASE_URL", "SLOP_REVIEW_MODEL", "SLOP_REVIEW_REASONING_EFFORT", "OPENROUTER_API_KEY", "OPENAI_API_KEY"} {
		t.Setenv(k, "")
	}
}

func TestLoadWith_CentralConfigBeatsTheEnvironment(t *testing.T) {
	reporting(t)
	t.Setenv("SLOP_REVIEW_PROVIDER", "openai")
	t.Setenv("OPENAI_API_KEY", "k")
	t.Setenv("OPENROUTER_API_KEY", "or-key")
	t.Setenv("SLOP_REVIEW_MODEL", "env-model")
	central := centralConfig(providerconfig.Settings{Primary: providerconfig.Choice{Provider: "openrouter", Model: "openai/gpt-5.6-luna-pro"}})

	cfg, err := LoadWith(context.Background(), nil, central)
	require.NoError(t, err)
	assert.Equal(t, 1, central.calls)
	assert.Equal(t, SourceCentral, cfg.ProviderSource)
	assert.Equal(t, int64(7), cfg.ProviderConfigRevision)
	assert.Equal(t, "openrouter", cfg.Provider)
	assert.Equal(t, "openai/gpt-5.6-luna-pro", cfg.Model, "the central model, not SLOP_REVIEW_MODEL")
	assert.Equal(t, "https://openrouter.ai/api/v1", cfg.BaseURL)
	assert.Equal(t, "or-key", cfg.APIKey)
	assert.Nil(t, cfg.Fallback)
}

// The saved copy applies like the server's own answer, and says so.
func TestLoadWith_ASavedConfigIsAppliedAndSaysSo(t *testing.T) {
	reporting(t)
	t.Setenv("OPENROUTER_API_KEY", "or-key")
	central := centralConfig(providerconfig.Settings{Primary: providerconfig.Choice{Provider: "openrouter", Model: "saved-model"}})
	central.result.Source = SourceSaved
	central.result.Notes = []string{"using the copy saved at ..."}

	cfg, err := LoadWith(context.Background(), nil, central)
	require.NoError(t, err)
	assert.Equal(t, SourceSaved, cfg.ProviderSource)
	assert.Equal(t, int64(7), cfg.ProviderConfigRevision)
	assert.Equal(t, "saved-model", cfg.Model)
	assert.Equal(t, []string{"using the copy saved at ..."}, cfg.Notes)
}

func TestLoadWith_CentralModelDefaultsToTheProvidersOwn(t *testing.T) {
	reporting(t)
	cfg, err := LoadWith(context.Background(), nil, centralConfig(providerconfig.Settings{
		Primary: providerconfig.Choice{Provider: "chatgpt", ReasoningEffort: "high"},
	}))
	require.NoError(t, err)
	assert.Equal(t, "gpt-5.6-luna", cfg.Model)
	assert.Equal(t, "high", cfg.ReasoningEffort)
}

func TestLoadWith_ProviderFlagsWinAndSkipTheCentralConfig(t *testing.T) {
	for _, args := range [][]string{{"--provider=openai"}, {"--model=m"}, {"--reasoning-effort=low"}} {
		t.Run(args[0], func(t *testing.T) {
			reporting(t)
			t.Setenv("OPENAI_API_KEY", "k")
			central := centralConfig(providerconfig.Settings{Primary: providerconfig.Choice{Provider: "openrouter"}})
			cfg, err := LoadWith(context.Background(), args, central)
			require.NoError(t, err)
			assert.Zero(t, central.calls, "a one-off run never asks the server")
			assert.Equal(t, SourceFlags, cfg.ProviderSource)
			assert.Zero(t, cfg.ProviderConfigRevision)
			assert.NotEqual(t, "openrouter", cfg.Provider)
		})
	}
}

func TestLoadWith_OnlyARunThatReportsLooksTheConfigUp(t *testing.T) {
	reporting(t)
	t.Setenv("SLOP_REVIEW_REPORT_TOKEN", "")
	t.Setenv("SLOP_REVIEW_REPORT_TOKEN_COMMAND", "")
	central := centralConfig(providerconfig.Settings{Primary: providerconfig.Choice{Provider: "openrouter"}})
	cfg, err := LoadWith(context.Background(), nil, central)
	require.NoError(t, err)
	assert.Zero(t, central.calls)
	assert.Equal(t, SourceLocal, cfg.ProviderSource)
	assert.Equal(t, "claude", cfg.Provider)
}

func TestLoadWith_NoCentralConfigMeansLocalSettingsWithTheNotes(t *testing.T) {
	reporting(t)
	central := &fakeCentral{result: &Central{Notes: []string{"server unreachable; using local settings"}}}
	cfg, err := LoadWith(context.Background(), nil, central)
	require.NoError(t, err)
	assert.Equal(t, SourceLocal, cfg.ProviderSource)
	assert.Equal(t, "claude", cfg.Provider)
	assert.Equal(t, []string{"server unreachable; using local settings"}, cfg.Notes)
}

// The central config beats the environment, so a local provider setting
// that can't be used -- a key only the old provider needed -- doesn't stop
// the run.
func TestLoadWith_CentralConfigOverridesAnUnusableLocalProvider(t *testing.T) {
	reporting(t)
	t.Setenv("SLOP_REVIEW_PROVIDER", "openrouter") // with no OPENROUTER_API_KEY
	cfg, err := LoadWith(context.Background(), nil, centralConfig(providerconfig.Settings{Primary: providerconfig.Choice{Provider: "chatgpt"}}))
	require.NoError(t, err)
	assert.Equal(t, "chatgpt", cfg.Provider)
}

func TestLoadWith_CentralPrimaryWithoutAKeyHereFails(t *testing.T) {
	reporting(t)
	_, err := LoadWith(context.Background(), nil, centralConfig(providerconfig.Settings{Primary: providerconfig.Choice{Provider: "openrouter"}}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "OPENROUTER_API_KEY")
	assert.Contains(t, err.Error(), "central provider config (revision 7)")
}

func TestLoadWith_FallbackIsResolved(t *testing.T) {
	reporting(t)
	t.Setenv("OPENROUTER_API_KEY", "or-key")
	cfg, err := LoadWith(context.Background(), nil, centralConfig(providerconfig.Settings{
		Primary:    providerconfig.Choice{Provider: "chatgpt"},
		Fallback:   &providerconfig.Choice{Provider: "openrouter", Model: "openai/gpt-5.6-luna-pro"},
		FallbackOn: []report.FailureClass{report.FailureUsageLimit},
	}))
	require.NoError(t, err)
	require.NotNil(t, cfg.Fallback)
	assert.Equal(t, "openrouter", cfg.Fallback.Provider)
	assert.Equal(t, ProviderKindOpenAICompatible, cfg.Fallback.ProviderKind)
	assert.Equal(t, "https://openrouter.ai/api/v1", cfg.Fallback.BaseURL)
	assert.Equal(t, "or-key", cfg.Fallback.APIKey)
	assert.Equal(t, "openai/gpt-5.6-luna-pro", cfg.Fallback.Model)
	assert.Equal(t, []report.FailureClass{report.FailureUsageLimit}, cfg.FallbackOn)
}

func TestLoadWith_AFallbackUnusableHereIsLeftOutWithANote(t *testing.T) {
	reporting(t)
	cfg, err := LoadWith(context.Background(), nil, centralConfig(providerconfig.Settings{
		Primary:    providerconfig.Choice{Provider: "chatgpt"},
		Fallback:   &providerconfig.Choice{Provider: "openrouter"},
		FallbackOn: []report.FailureClass{report.FailureUsageLimit},
	}))
	require.NoError(t, err)
	assert.Nil(t, cfg.Fallback)
	assert.Empty(t, cfg.FallbackOn)
	require.Len(t, cfg.Notes, 1)
	assert.Contains(t, cfg.Notes[0], "OPENROUTER_API_KEY")
}

// A base URL set for one provider's gateway must never receive another
// provider's key.
func TestLoadWith_LocalBaseURLAppliesOnlyToTheProviderItWasSetFor(t *testing.T) {
	reporting(t)
	t.Setenv("SLOP_REVIEW_PROVIDER", "openai")
	t.Setenv("SLOP_REVIEW_BASE_URL", "https://gateway.internal/v1")
	t.Setenv("OPENAI_API_KEY", "k")
	t.Setenv("OPENROUTER_API_KEY", "or-key")

	cfg, err := LoadWith(context.Background(), nil, centralConfig(providerconfig.Settings{Primary: providerconfig.Choice{Provider: "openrouter"}}))
	require.NoError(t, err)
	assert.Equal(t, "https://openrouter.ai/api/v1", cfg.BaseURL)

	cfg, err = LoadWith(context.Background(), nil, centralConfig(providerconfig.Settings{
		Primary:    providerconfig.Choice{Provider: "openrouter"},
		Fallback:   &providerconfig.Choice{Provider: "openai"},
		FallbackOn: []report.FailureClass{report.FailureCredits},
	}))
	require.NoError(t, err)
	assert.Equal(t, "https://openrouter.ai/api/v1", cfg.BaseURL)
	assert.Equal(t, "https://gateway.internal/v1", cfg.Fallback.BaseURL)
}

// Every provider the server can name is one this build knows how to reach,
// and the other way round.
func TestProviderProfilesMatchTheSharedProviderList(t *testing.T) {
	assert.ElementsMatch(t, providerconfig.ProviderNames(), slices.Collect(maps.Keys(providerProfiles)))
}

// How each provider bills is reported with every session's usage: a
// subscription's cost is an estimate, never money spent. Only OpenRouter
// reports what a request cost.
func TestProviderProfiles_BillingAndReportedCost(t *testing.T) {
	want := map[string]struct {
		billing     llmusage.BillingMode
		reportsCost bool
	}{
		"chatgpt":    {llmusage.BillingSubscription, false},
		"openrouter": {llmusage.BillingMetered, true},
		"openai":     {llmusage.BillingMetered, false},
		"codex":      {llmusage.BillingSubscription, false},
		"claude":     {llmusage.BillingSubscription, false},
	}
	require.Len(t, providerProfiles, len(want))
	for name, w := range want {
		p := providerProfiles[name]
		assert.Equal(t, w.billing, p.billing, name)
		assert.Equal(t, w.reportsCost, p.reportsCost, name)
	}
}

func TestLoad_SourceDirFlag(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "test-key")

	cfg, err := Load(nil)
	require.NoError(t, err)
	assert.Empty(t, cfg.SourceDir)

	cfg, err = Load([]string{"--source-dir=/opt/reviewer"})
	require.NoError(t, err)
	assert.Equal(t, "/opt/reviewer", cfg.SourceDir)
}

func TestLoad_IntentFile(t *testing.T) {
	t.Setenv("SLOP_REVIEW_INTENT", "")
	path := filepath.Join(t.TempDir(), "intent.md")
	require.NoError(t, os.WriteFile(path, []byte("  Add the thing.\n"), 0o644))

	cfg, err := Load([]string{"--intent=" + path})
	require.NoError(t, err)
	assert.Equal(t, "Add the thing.", cfg.Intent)

	t.Setenv("SLOP_REVIEW_INTENT", path)
	cfg, err = Load(nil)
	require.NoError(t, err)
	assert.Equal(t, "Add the thing.", cfg.Intent)
}

func TestLoad_IntentMissingFileFails(t *testing.T) {
	_, err := Load([]string{"--intent=" + filepath.Join(t.TempDir(), "nope.md")})
	require.ErrorContains(t, err, "SLOP_REVIEW_INTENT")
}

func TestLoad_IntentIsCappedOnARuneBoundary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "intent.md")
	// A 3-byte rune straddling the cap.
	body := strings.Repeat("a", MaxIntentBytes-1) + "€€"
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))

	cfg, err := Load([]string{"--intent=" + path})
	require.NoError(t, err)
	assert.True(t, cfg.IntentTruncated)
	assert.True(t, utf8.ValidString(cfg.Intent))
	assert.Equal(t, strings.Repeat("a", MaxIntentBytes-1), cfg.Intent, "the whole rune past the cap is dropped, nothing else")
	assert.NotEmpty(t, cfg.Notes)
}

func TestLoad_CLIProvidersDefaultModelsAndKinds(t *testing.T) {
	for name, want := range map[string]struct {
		kind  ProviderKind
		model string
	}{
		"codex":  {ProviderKindCodex, "gpt-5.6-luna"},
		"claude": {ProviderKindClaude, "claude-sonnet-5-5"},
	} {
		t.Run(name, func(t *testing.T) {
			cfg, err := Load([]string{"--provider=" + name})
			require.NoError(t, err)
			assert.Equal(t, want.kind, cfg.ProviderKind)
			assert.Equal(t, want.model, cfg.Model)
			assert.Equal(t, llmusage.BillingSubscription, cfg.BillingMode)
		})
	}
}

// The CLI providers read files themselves, which the review must say; but
// slop-review keeps withholding secret files from what it sends them, so
// AllowSecrets stays off unless asked for.
func TestLoad_CLIProvidersWarnButKeepTheSecretDenylist(t *testing.T) {
	for _, name := range []string{"codex", "claude"} {
		t.Run(name, func(t *testing.T) {
			cfg, err := Load([]string{"--provider=" + name})
			require.NoError(t, err)
			assert.False(t, cfg.AllowSecrets)
			require.Len(t, cfg.Warnings, 1)
			assert.Contains(t, cfg.Warnings[0], "secret-shaped files")

			cfg, err = Load([]string{"--provider=" + name, "--allow-secrets=false"})
			require.NoError(t, err, "an explicit false is the default, not a contradiction")
			assert.False(t, cfg.AllowSecrets)
		})
	}
}

func TestLoad_AStrayArgumentIsRefused(t *testing.T) {
	// "--run-checks" may be bare, so "false" here is not its value.
	_, err := Load([]string{"--provider=codex", "--run-checks", "false"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `unexpected argument "false"`)

	_, err = Load([]string{"--allow-secrets", "false"})
	require.Error(t, err, "a bare --allow-secrets is true, so a following false must not be dropped")

	cfg, err := Load([]string{"--allow-secrets", "--persona=standard"})
	require.NoError(t, err)
	assert.True(t, cfg.AllowSecrets)
}

func TestLoad_APIProvidersKeepTheSecretDenylistByDefault(t *testing.T) {
	cfg, err := Load([]string{"--provider=chatgpt"})
	require.NoError(t, err)
	assert.False(t, cfg.AllowSecrets)
	assert.Empty(t, cfg.Warnings)
}

func TestLoad_RunChecksIsOffByDefaultAndMayBeBare(t *testing.T) {
	cfg, err := Load([]string{"--provider=codex"})
	require.NoError(t, err)
	assert.False(t, cfg.RunChecks)

	cfg, err = Load([]string{"--provider=codex", "--run-checks"})
	require.NoError(t, err)
	assert.True(t, cfg.RunChecks)

	cfg, err = Load([]string{"--provider=codex", "--run-checks=false"})
	require.NoError(t, err)
	assert.False(t, cfg.RunChecks)
}

func TestLoad_ClaudeTakesItsOwnEfforts(t *testing.T) {
	cfg, err := Load([]string{"--provider=claude", "--reasoning-effort=max"})
	require.NoError(t, err)
	assert.Equal(t, "max", cfg.ReasoningEffort)

	_, err = Load([]string{"--provider=claude", "--reasoning-effort=minimal"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not accepted by provider")
}

func TestLoadWith_ACentralCLIPrimaryKeepsTheSecretDenylist(t *testing.T) {
	reporting(t)
	cfg, err := LoadWith(context.Background(), nil, centralConfig(providerconfig.Settings{
		Primary: providerconfig.Choice{Provider: "claude", ReasoningEffort: "high"},
	}))
	require.NoError(t, err)
	assert.Equal(t, ProviderKindClaude, cfg.ProviderKind)
	assert.Equal(t, "high", cfg.ReasoningEffort)
	assert.False(t, cfg.AllowSecrets)
	assert.Len(t, cfg.Warnings, 1)
}

// A CLI fallback is kept: what slop-review sends it is redacted like any
// other route's. Its warning is the review's to add, only if it ran.
func TestLoadWith_ACLIFallbackIsKeptWithoutAWarningUpFront(t *testing.T) {
	settings := providerconfig.Settings{
		Primary:    providerconfig.Choice{Provider: "chatgpt"},
		Fallback:   &providerconfig.Choice{Provider: "codex"},
		FallbackOn: []report.FailureClass{report.FailureUsageLimit},
	}
	reporting(t)
	cfg, err := LoadWith(context.Background(), nil, centralConfig(settings))
	require.NoError(t, err)
	assert.False(t, cfg.AllowSecrets)
	require.NotNil(t, cfg.Fallback)
	assert.Equal(t, ProviderKindCodex, cfg.Fallback.ProviderKind)
	assert.Equal(t, []report.FailureClass{report.FailureUsageLimit}, cfg.FallbackOn)
	assert.Empty(t, cfg.Warnings)
}

// A run whose findings could quote a secret is reported as one, so the
// server won't store it: --allow-secrets, or codex (on either route),
// which reads the checkout with nothing keeping it off secret files.
// claude is denied them, so its runs are stored.
func TestSecretsMayBeQuoted(t *testing.T) {
	for name, tc := range map[string]struct {
		args []string
		want bool
	}{
		"chatgpt":                {[]string{"--provider=chatgpt"}, false},
		"chatgpt, allow-secrets": {[]string{"--provider=chatgpt", "--allow-secrets"}, true},
		"claude":                 {[]string{"--provider=claude"}, false},
		"codex":                  {[]string{"--provider=codex"}, true},
	} {
		t.Run(name, func(t *testing.T) {
			cfg, err := Load(tc.args)
			require.NoError(t, err)
			assert.Equal(t, tc.want, cfg.SecretsMayBeQuoted())
		})
	}

	reporting(t)
	cfg, err := LoadWith(context.Background(), nil, centralConfig(providerconfig.Settings{
		Primary:    providerconfig.Choice{Provider: "chatgpt"},
		Fallback:   &providerconfig.Choice{Provider: "codex"},
		FallbackOn: []report.FailureClass{report.FailureUsageLimit},
	}))
	require.NoError(t, err)
	assert.True(t, cfg.SecretsMayBeQuoted(), "a codex fallback may run")
}
