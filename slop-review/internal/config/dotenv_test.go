package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseDotEnv_ReadsOnlySlopReviewAssignments(t *testing.T) {
	d, err := ParseDotEnv(strings.NewReader(`
# a comment
SLOP_REVIEW_PROVIDER=codex
  SLOP_REVIEW_MODEL = gpt-5.6-luna   # trailing comment
export SLOP_REVIEW_REASONING_EFFORT=xhigh
SLOP_REVIEW_FALLBACK_PROVIDER="claude"
SLOP_REVIEW_FALLBACK_MODEL='claude-haiku-5-5' # quoted, then a comment
SLOP_REVIEW_INTENT="a # in quotes"
SLOP_REVIEW_FALLBACK_ON=
SLOP_REVIEW_BASE_URL=https://gw.example/v1#frag
OPENAI_API_KEY=sk-not-for-us
OTHER_TOOL_JSON={"multi":
  "line value that is no assignment"}
-----BEGIN PRIVATE KEY-----
`))
	require.NoError(t, err)
	assert.Equal(t, DotEnv{
		"SLOP_REVIEW_PROVIDER":          "codex",
		"SLOP_REVIEW_MODEL":             "gpt-5.6-luna",
		"SLOP_REVIEW_REASONING_EFFORT":  "xhigh",
		"SLOP_REVIEW_FALLBACK_PROVIDER": "claude",
		"SLOP_REVIEW_FALLBACK_MODEL":    "claude-haiku-5-5",
		"SLOP_REVIEW_INTENT":            "a # in quotes",
		"SLOP_REVIEW_FALLBACK_ON":       "",
		"SLOP_REVIEW_BASE_URL":          "https://gw.example/v1#frag",
	}, d)
}

// Values are taken literally: the file is data, never shell.
func TestParseDotEnv_DoesNotExpandOrExecute(t *testing.T) {
	d, err := ParseDotEnv(strings.NewReader("SLOP_REVIEW_MODEL=$(touch /tmp/x)${HOME}\nSLOP_REVIEW_PERSONA=\"$HOME\"\n"))
	require.NoError(t, err)
	assert.Equal(t, "$(touch /tmp/x)${HOME}", d["SLOP_REVIEW_MODEL"])
	assert.Equal(t, "$HOME", d["SLOP_REVIEW_PERSONA"])
}

// A malformed line of ours fails, naming the line and key but never the
// value, which could be half a secret.
func TestParseDotEnv_AMalformedSlopReviewLineFailsWithoutItsValue(t *testing.T) {
	for name, tc := range map[string]struct{ text, want string }{
		"unterminated quote": {"OK=1\nSLOP_REVIEW_REPORT_TOKEN=\"secret-value\n", "line 2 (SLOP_REVIEW_REPORT_TOKEN): unterminated quote"},
		"text after quote":   {"SLOP_REVIEW_REPORT_TOKEN='secret-value'tail\n", "line 1 (SLOP_REVIEW_REPORT_TOKEN): text after the closing quote"},
		"no assignment":      {"SLOP_REVIEW_REPORT_TOKEN secret-value\n", "line 1: not a KEY=VALUE assignment"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ParseDotEnv(strings.NewReader(tc.text))
			require.EqualError(t, err, tc.want)
			assert.NotContains(t, err.Error(), "secret-value")
		})
	}
}

func TestReadDotEnv_AMissingFileOrNoPathIsNoSettings(t *testing.T) {
	d, err := ReadDotEnv(filepath.Join(t.TempDir(), ".env"))
	require.NoError(t, err)
	assert.Empty(t, d)
	d, err = ReadDotEnv("")
	require.NoError(t, err)
	assert.Empty(t, d)
}

func TestReadDotEnv_AnUnreadableFileFails(t *testing.T) {
	_, err := ReadDotEnv(t.TempDir()) // a directory
	require.Error(t, err)
}

func TestDotEnv_TheEnvironmentWinsUnlessEmpty(t *testing.T) {
	d := DotEnv{"SLOP_REVIEW_MODEL": "file", "SLOP_REVIEW_PERSONA": "file"}
	t.Setenv("SLOP_REVIEW_MODEL", "env")
	t.Setenv("SLOP_REVIEW_PERSONA", "")
	assert.Equal(t, "env", d.Getenv("SLOP_REVIEW_MODEL"))
	assert.Equal(t, "file", d.Getenv("SLOP_REVIEW_PERSONA"))
	assert.Empty(t, d.Getenv("SLOP_REVIEW_PROVIDER"))
	assert.Equal(t, []string{"SLOP_REVIEW_PERSONA"}, d.usedKeys())
}

func writeEnvFile(t *testing.T, text string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), ".env")
	require.NoError(t, os.WriteFile(path, []byte(text), 0o600))
	return path
}

// Flags beat the environment, which beats the .env, which beats the
// defaults.
func TestLoad_EnvFileSitsBelowTheEnvironmentAndTheFlags(t *testing.T) {
	path := writeEnvFile(t, "SLOP_REVIEW_PROVIDER=codex\nSLOP_REVIEW_MODEL=file-model\nSLOP_REVIEW_REASONING_EFFORT=high\nSLOP_REVIEW_MAX_TURNS=12\n")
	t.Setenv("SLOP_REVIEW_MODEL", "env-model")

	cfg, err := Load([]string{"--env-file=" + path, "--max-turns=7"})
	require.NoError(t, err)
	assert.Equal(t, "codex", cfg.Provider, "from the file")
	assert.Equal(t, "env-model", cfg.Model, "the environment beats the file")
	assert.Equal(t, "high", cfg.ReasoningEffort, "from the file")
	assert.Equal(t, 7, cfg.MaxTurns, "a flag beats the file")
	assert.Equal(t, SourceLocal, cfg.ProviderSource, "the file is local settings, not a one-off")
	assert.Equal(t, 40, mustLoad(t, nil).MaxTurns, "without --env-file nothing is read")
}

func mustLoad(t *testing.T, args []string) *Config {
	t.Helper()
	cfg, err := Load(args)
	require.NoError(t, err)
	return cfg
}

// The notes say which keys came from the file, never what they were.
func TestLoad_EnvFileKeysAreNotedWithoutTheirValues(t *testing.T) {
	path := writeEnvFile(t, "SLOP_REVIEW_PERSONA=persona-from-file\nSLOP_REVIEW_REPORT_TOKEN=tok-from-file\nSLOP_REVIEW_MODEL=overridden\n")
	t.Setenv("SLOP_REVIEW_MODEL", "env-model")

	cfg, err := Load([]string{"--env-file=" + path})
	require.NoError(t, err)
	assert.Equal(t, "tok-from-file", cfg.ReportToken, "the env-only token may come from the file too")
	notes := strings.Join(cfg.Notes, "\n")
	assert.Contains(t, notes, "settings from "+path+": SLOP_REVIEW_PERSONA, SLOP_REVIEW_REPORT_TOKEN")
	assert.NotContains(t, notes, "SLOP_REVIEW_MODEL", "the environment's value applied, not the file's")
	assert.NotContains(t, notes, "tok-from-file")
	assert.NotContains(t, notes, "persona-from-file")
}

// The file holds slop-review's settings only: a provider API key in it is
// not taken, as that would make the .env a second place keys come from.
func TestLoad_EnvFileNeverSuppliesAProviderKey(t *testing.T) {
	path := writeEnvFile(t, "SLOP_REVIEW_PROVIDER=openai\nOPENAI_API_KEY=from-file\n")
	t.Setenv("OPENAI_API_KEY", "")
	_, err := Load([]string{"--env-file=" + path})
	require.ErrorContains(t, err, "OPENAI_API_KEY is required")
}

func TestLoad_AnIntentInTheEnvFileIsRefused(t *testing.T) {
	path := writeEnvFile(t, "SLOP_REVIEW_INTENT=pr.md\n")
	_, err := Load([]string{"--env-file=" + path})
	require.ErrorContains(t, err, "SLOP_REVIEW_INTENT can't be set in a .env")
}

func TestLoad_AMalformedEnvFileFails(t *testing.T) {
	path := writeEnvFile(t, "SLOP_REVIEW_MODEL=\"unterminated\n")
	_, err := Load([]string{"--env-file=" + path})
	require.ErrorContains(t, err, path+": line 1 (SLOP_REVIEW_MODEL): unterminated quote")
}
