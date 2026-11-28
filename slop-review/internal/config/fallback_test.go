package config

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-review/report"
	"github.com/lowaa/slop-factory/slop-review/report/providerconfig"
)

// localCodexWithClaudeFallback is the configuration this was built for.
func localCodexWithClaudeFallback(t *testing.T) {
	t.Helper()
	t.Setenv("SLOP_REVIEW_PROVIDER", "codex")
	t.Setenv("SLOP_REVIEW_MODEL", "gpt-5.6-luna")
	t.Setenv("SLOP_REVIEW_REASONING_EFFORT", "xhigh")
	t.Setenv("SLOP_REVIEW_FALLBACK_PROVIDER", "claude")
	t.Setenv("SLOP_REVIEW_FALLBACK_MODEL", "claude-haiku-5-5")
	t.Setenv("SLOP_REVIEW_FALLBACK_REASONING_EFFORT", "xhigh")
}

func TestLoad_LocalFallbackIsResolvedLikeTheCentralOne(t *testing.T) {
	localCodexWithClaudeFallback(t)

	cfg := mustLoad(t, nil)
	assert.Equal(t, SourceLocal, cfg.ProviderSource)
	assert.Equal(t, "codex", cfg.Provider)
	require.NotNil(t, cfg.Fallback)
	assert.Equal(t, Route{
		Provider: "claude", ProviderKind: ProviderKindClaude, Model: "claude-haiku-5-5", ReasoningEffort: "xhigh",
		BillingMode: providerProfiles["claude"].billing,
	}, *cfg.Fallback)
	assert.Equal(t, providerconfig.DefaultTrigger, cfg.FallbackOn, "unset, it falls back on what the central config defaults to")
}

func TestLoad_LocalFallbackFromTheEnvFile(t *testing.T) {
	path := writeEnvFile(t, "SLOP_REVIEW_PROVIDER=codex\nSLOP_REVIEW_FALLBACK_PROVIDER=claude\nSLOP_REVIEW_FALLBACK_ON=usage_limit, auth\n")

	cfg := mustLoad(t, []string{"--env-file=" + path})
	require.NotNil(t, cfg.Fallback)
	assert.Equal(t, "claude", cfg.Fallback.Provider)
	assert.Equal(t, "claude-sonnet-5-5", cfg.Fallback.Model, "the provider's default model")
	assert.Equal(t, "xhigh", cfg.Fallback.ReasoningEffort, "the default effort")
	assert.Equal(t, []report.FailureClass{report.FailureUsageLimit, report.FailureAuth}, cfg.FallbackOn)
}

func TestLoad_FallbackFlags(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "or-key")
	cfg := mustLoad(t, []string{"--fallback-provider=openrouter", "--fallback-model=openai/gpt-5.6-luna-pro", "--fallback-on=credits"})
	assert.Equal(t, SourceFlags, cfg.ProviderSource, "a fallback flag chooses the reviewer too")
	assert.Equal(t, "claude", cfg.Provider)
	require.NotNil(t, cfg.Fallback)
	assert.Equal(t, "openrouter", cfg.Fallback.Provider)
	assert.Equal(t, "openai/gpt-5.6-luna-pro", cfg.Fallback.Model)
	assert.Equal(t, "or-key", cfg.Fallback.APIKey)
	assert.Empty(t, cfg.Fallback.ReasoningEffort, "openrouter takes no effort")
	assert.Equal(t, []report.FailureClass{report.FailureCredits}, cfg.FallbackOn)
}

// A one-off run chose its reviewer on the command line; the configured
// fallback backed up a different choice, so it is left out, and the notes
// say how to have one.
func TestLoad_AOneOffRunDropsTheEnvFallback(t *testing.T) {
	for _, args := range [][]string{{"--provider=claude"}, {"--model=gpt-5.6-luna-pro"}, {"--reasoning-effort=high"}} {
		t.Run(args[0], func(t *testing.T) {
			localCodexWithClaudeFallback(t)
			cfg := mustLoad(t, args)
			assert.Equal(t, SourceFlags, cfg.ProviderSource)
			assert.Nil(t, cfg.Fallback)
			assert.Empty(t, cfg.FallbackOn)
			assert.Contains(t, strings.Join(cfg.Notes, "\n"), "give --fallback-provider for one")
		})
	}
}

func TestLoad_AOneOffRunKeepsAFallbackGivenOnItsCommandLine(t *testing.T) {
	localCodexWithClaudeFallback(t)
	cfg := mustLoad(t, []string{"--provider=chatgpt", "--fallback-provider=codex"})
	assert.Equal(t, "chatgpt", cfg.Provider)
	require.NotNil(t, cfg.Fallback)
	assert.Equal(t, "codex", cfg.Fallback.Provider, "the flag")
	assert.Equal(t, "gpt-5.6-luna", cfg.Fallback.Model, "codex's default, never the environment's claude model")
	assert.Equal(t, providerconfig.DefaultTrigger, cfg.FallbackOn)
}

func TestLoad_AnInvalidLocalFallbackFails(t *testing.T) {
	for name, tc := range map[string]struct {
		env  map[string]string
		want string
	}{
		"same provider": {map[string]string{"SLOP_REVIEW_FALLBACK_PROVIDER": "codex"}, "fallback.provider must differ"},
		"unknown":       {map[string]string{"SLOP_REVIEW_FALLBACK_PROVIDER": "evil-proxy"}, `fallback.provider "evil-proxy" is not one of`},
		"not a trigger": {map[string]string{"SLOP_REVIEW_FALLBACK_PROVIDER": "claude", "SLOP_REVIEW_FALLBACK_ON": "usage_limit,config"}, `fallback_on: "config" is not one of`},
		"repeated":      {map[string]string{"SLOP_REVIEW_FALLBACK_PROVIDER": "claude", "SLOP_REVIEW_FALLBACK_ON": "auth,auth"}, `"auth" is repeated`},
		"bad effort":    {map[string]string{"SLOP_REVIEW_FALLBACK_PROVIDER": "claude", "SLOP_REVIEW_FALLBACK_REASONING_EFFORT": "minimal"}, `"minimal" is not accepted by "claude"`},
		"bad model":     {map[string]string{"SLOP_REVIEW_FALLBACK_PROVIDER": "claude", "SLOP_REVIEW_FALLBACK_MODEL": "a model"}, "is not a model slug"},
		"no provider":   {map[string]string{"SLOP_REVIEW_FALLBACK_MODEL": "claude-haiku-5-5"}, "need SLOP_REVIEW_FALLBACK_PROVIDER"},
		"only classes":  {map[string]string{"SLOP_REVIEW_FALLBACK_ON": "auth"}, "need SLOP_REVIEW_FALLBACK_PROVIDER"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("SLOP_REVIEW_PROVIDER", "codex")
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			_, err := Load(nil)
			require.ErrorContains(t, err, tc.want)
			assert.ErrorContains(t, err, "SLOP_REVIEW_FALLBACK")
		})
	}
}

// As with the central config's: a fallback this machine has no key for is
// left out with a note, and the primary still runs.
func TestLoad_ALocalFallbackUnusableHereIsLeftOutWithANote(t *testing.T) {
	t.Setenv("SLOP_REVIEW_FALLBACK_PROVIDER", "openrouter")
	t.Setenv("OPENROUTER_API_KEY", "")
	cfg := mustLoad(t, nil)
	assert.Nil(t, cfg.Fallback)
	assert.Contains(t, strings.Join(cfg.Notes, "\n"), "OPENROUTER_API_KEY")
}

// A central config wins unchanged: its fallback (here, none) and not the
// local one.
func TestLoadWith_TheCentralConfigBeatsTheLocalFallback(t *testing.T) {
	reporting(t)
	localCodexWithClaudeFallback(t)
	cfg, err := LoadWith(context.Background(), nil, centralConfig(providerconfig.Settings{Primary: providerconfig.Choice{Provider: "chatgpt"}}))
	require.NoError(t, err)
	assert.Equal(t, SourceCentral, cfg.ProviderSource)
	assert.Equal(t, "chatgpt", cfg.Provider)
	assert.Nil(t, cfg.Fallback)
	assert.Contains(t, strings.Join(cfg.Notes, "\n"), "SLOP_REVIEW_FALLBACK_* is not used")
}

// With a report server that has no config to give, the local settings
// apply, fallback included.
func TestLoadWith_NoCentralConfigKeepsTheLocalFallback(t *testing.T) {
	reporting(t)
	localCodexWithClaudeFallback(t)
	cfg, err := LoadWith(context.Background(), nil, &fakeCentral{result: &Central{Notes: []string{"no config"}}})
	require.NoError(t, err)
	assert.Equal(t, SourceLocal, cfg.ProviderSource)
	require.NotNil(t, cfg.Fallback)
	assert.Equal(t, "claude", cfg.Fallback.Provider)
}
