package config

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-review/report/providerconfig"
)

func TestLoad_ReportTokenCommandFromEnvAndFlag(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "test-key")
	t.Setenv("SLOP_REVIEW_REPORT_TOKEN", "")
	t.Setenv("SLOP_REVIEW_REPORT_TOKEN_COMMAND", "/env/token.sh --env")

	cfg, err := Load(nil)
	require.NoError(t, err)
	assert.Equal(t, "/env/token.sh --env", cfg.ReportTokenCommand)

	cfg, err = Load([]string{"--report-token-command=/flag/token.sh"})
	require.NoError(t, err)
	assert.Equal(t, "/flag/token.sh", cfg.ReportTokenCommand, "the flag beats the environment")
}

func TestReportTokenSource(t *testing.T) {
	t.Run("neither means no credentials", func(t *testing.T) {
		src, err := (&Config{}).ReportTokenSource()
		require.NoError(t, err)
		assert.Nil(t, src)
	})
	t.Run("a fixed token", func(t *testing.T) {
		src, err := (&Config{ReportToken: "tok"}).ReportTokenSource()
		require.NoError(t, err)
		tok, err := src.Token(context.Background())
		require.NoError(t, err)
		assert.Equal(t, "tok", tok)
	})
	t.Run("a command", func(t *testing.T) {
		src, err := (&Config{ReportTokenCommand: "/bin/echo hello"}).ReportTokenSource()
		require.NoError(t, err)
		tok, err := src.Token(context.Background())
		require.NoError(t, err)
		assert.Equal(t, "hello", tok)
	})
	t.Run("both: the fixed token wins", func(t *testing.T) {
		src, err := (&Config{ReportToken: "explicit", ReportTokenCommand: "/bin/echo from-command"}).ReportTokenSource()
		require.NoError(t, err)
		tok, err := src.Token(context.Background())
		require.NoError(t, err)
		assert.Equal(t, "explicit", tok)
	})
	t.Run("a blank command is no command, not a failed run", func(t *testing.T) {
		src, err := (&Config{ReportTokenCommand: "  "}).ReportTokenSource()
		require.NoError(t, err)
		assert.Nil(t, src)
	})
}

func TestLoadWith_AFixedTokenBeatsTheCommandAndSaysSoOnce(t *testing.T) {
	reporting(t) // sets SLOP_REVIEW_REPORT_TOKEN=tok
	t.Setenv("SLOP_REVIEW_REPORT_TOKEN_COMMAND", "/bin/echo from-command")
	t.Setenv("OPENROUTER_API_KEY", "k")
	central := centralConfig(providerconfig.Settings{Primary: providerconfig.Choice{Provider: "openrouter"}})

	cfg, err := LoadWith(context.Background(), nil, central)

	require.NoError(t, err)
	tok, err := central.tokens.Token(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "tok", tok)
	var notes []string
	for _, n := range cfg.Notes {
		if strings.Contains(n, "TOKEN_COMMAND is ignored") {
			notes = append(notes, n)
		}
	}
	require.Len(t, notes, 1)
	assert.Contains(t, notes[0], "SLOP_REVIEW_REPORT_TOKEN is set")
	assert.NotContains(t, notes[0], "tok\n")
}

func TestTokenCommandIgnoredNote_OnlyWhenBothAreSet(t *testing.T) {
	assert.NotEmpty(t, TokenCommandIgnoredNote("t", "/bin/true"))
	assert.Empty(t, TokenCommandIgnoredNote("t", ""))
	assert.Empty(t, TokenCommandIgnoredNote("t", "  "))
	assert.Empty(t, TokenCommandIgnoredNote("", "/bin/true"))
}

func TestLoadWith_ATokenCommandAloneLooksTheConfigUp(t *testing.T) {
	reporting(t)
	t.Setenv("SLOP_REVIEW_REPORT_TOKEN", "")
	t.Setenv("SLOP_REVIEW_REPORT_TOKEN_COMMAND", "/bin/echo tok")
	t.Setenv("OPENROUTER_API_KEY", "k")
	central := centralConfig(providerconfig.Settings{Primary: providerconfig.Choice{Provider: "openrouter"}})

	cfg, err := LoadWith(context.Background(), nil, central)

	require.NoError(t, err)
	assert.Equal(t, 1, central.calls)
	assert.Equal(t, SourceCentral, cfg.ProviderSource)
	require.NotNil(t, central.tokens)
	tok, err := central.tokens.Token(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "tok", tok)
}
