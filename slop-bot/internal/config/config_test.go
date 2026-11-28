package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func parse(t *testing.T, yml string) (*Config, error) {
	t.Helper()
	return Parse([]byte(yml), "/etc/sb", "/home/u")
}

func TestEmptyConfigGetsEveryDefault(t *testing.T) {
	t.Parallel()
	c, err := parse(t, "")
	require.NoError(t, err)
	assert.Equal(t, "/home/u/slop-bot/workspace", c.WorkspaceDir)
	assert.Empty(t, c.BriefingPath, "an absent briefing_path means no custom briefing")
	assert.Equal(t, ClaudeCode, c.DefaultHarness)
	assert.True(t, *c.AgentsMaySetUrgent)
	assert.Equal(t, "local", c.Ticketing.System)
	assert.Equal(t, 4, c.Concurrency.MaxActive)
	assert.Equal(t, 12, c.Concurrency.MaxLive)
	assert.Equal(t, 1, c.Concurrency.Urgent.MaxActive)
	assert.False(t, c.Autonomy.Enabled)
	assert.Equal(t, 50, c.AutonomyBudget())
	assert.Equal(t, 3, c.Agents.Nudges)
	assert.Equal(t, 15*time.Minute, c.Agents.StallAfter.D())
	assert.Equal(t, 24*time.Hour, c.Agents.AskReminderAfter.D())
	require.Contains(t, c.Harnesses, ClaudeCode)
	assert.Equal(t, "stream-json", c.Harnesses[ClaudeCode].Driver)
	assert.True(t, *c.Harnesses[ClaudeCode].Permissive)
	assert.False(t, c.Harnesses[ClaudeCode].APIKeyOptIn)
	assert.InDelta(t, 95.0, c.Harnesses[ClaudeCode].LimitThreshold, 0)
}

func TestFullConfig(t *testing.T) {
	t.Parallel()
	c, err := parse(t, `
workspace_dir: ~/ws
briefing_path: briefing.md
default_harness: codex
agents_may_set_urgent: false
ticketing: {system: local}
repos:
  - {name: slop-factory, path: /src/slop-factory}
  - {name: rel, path: ../rel}
concurrency: {max_active: 2, max_live: 10, urgent: {max_active: 2}}
autonomy: {enabled: true, tickets_per_agent: 3}
harnesses:
  codex: {limit_threshold: 80}
  agy: {driver: tui, permissive: false, model: gemini-x}
  pi:
agents: {nudges: 5, stall_after: 30m, ask_reminder_after: 2h}
retention: {transcripts: 72h, events: 48h}
doctor: {require: [gh, shellcheck]}
`)
	require.NoError(t, err)
	assert.Equal(t, "/home/u/ws", c.WorkspaceDir)
	assert.Equal(t, "/etc/sb/briefing.md", c.BriefingPath, "relative to the config file's directory")
	assert.False(t, *c.AgentsMaySetUrgent)
	r, ok := c.Repo("rel")
	require.True(t, ok)
	assert.Equal(t, "/etc/rel", r.Path)
	assert.Equal(t, 12, c.AutonomyBudget(), "3 × (2 + 2) slots")
	assert.Equal(t, "app-server", c.Harnesses[Codex].Driver)
	assert.InDelta(t, 80.0, c.Harnesses[Codex].LimitThreshold, 0)
	assert.Equal(t, "rpc", c.Harnesses[Pi].Driver, "a bare harness key gets its default driver")
	assert.False(t, *c.Harnesses[Agy].Permissive)
	assert.Equal(t, "gemini-x", c.Harnesses[Agy].Model)
	assert.Empty(t, c.Harnesses[Pi].Model)
	assert.Equal(t, []string{"gh", "shellcheck"}, c.Doctor.Require)
	assert.Equal(t, 30*time.Minute, c.Agents.StallAfter.D())
	assert.Equal(t, 48*time.Hour, c.Retention.Events.D())
}

func TestInvalidConfigsNameTheField(t *testing.T) {
	t.Parallel()
	cases := map[string]struct{ yml, want string }{
		"typo key":              {"workspace_dri: /x\n", "field workspace_dri not found"},
		"unknown harness":       {"harnesses: {cursor: {}}\ndefault_harness: cursor\n", "harnesses.cursor: unknown harness"},
		"bad driver":            {"harnesses: {claude-code: {driver: tui}}\n", `harnesses.claude-code.driver: "tui" isn't a driver`},
		"default not present":   {"default_harness: pi\nharnesses: {codex: {}}\n", `default_harness: "pi" isn't configured`},
		"unknown ticketing":     {"ticketing: {system: jira}\n", `ticketing.system: "jira"`},
		"duplicate repo":        {"repos: [{name: a, path: /a}, {name: a, path: /b}]\n", `repos[1].name: "a" is listed twice`},
		"unnamed repo":          {"repos: [{path: /a}]\n", "repos[0].name: is empty"},
		"repo name with slash":  {"repos: [{name: owner/repo, path: /a}]\n", `repos[0].name: "owner/repo": want a letter or digit first`},
		"repo name dot first":   {"repos: [{name: .github, path: /a}]\n", `repos[0].name: ".github": want a letter or digit first`},
		"repo without path":     {"repos: [{name: a}]\n", "repos[0].path: is empty"},
		"max_live too small":    {"concurrency: {max_active: 4, max_live: 2}\n", "concurrency.max_live: 2 is below"},
		"bad duration":          {"agents: {stall_after: soon}\n", `"soon" isn't a duration`},
		"zero budget":           {"autonomy: {tickets_per_agent: -1}\n", "tickets_per_agent: must be at least 1"},
		"negative concurrency":  {"concurrency: {max_active: -1}\n", "max_active and urgent.max_active must be at least 1"},
		"blank required binary": {"doctor: {require: [gh, \"\"]}\n", "doctor.require[1]: is empty"},
		"limit over 100":        {"harnesses: {claude-code: {limit_threshold: 101}}\n", "harnesses.claude-code.limit_threshold: 101 isn't a percentage"},
		"negative limit":        {"harnesses: {claude-code: {limit_threshold: -5}}\n", "limit_threshold: -5"},
		"negative retention":    {"retention: {transcripts: -1h}\n", "retention: transcripts and events must be positive"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := parse(t, tc.yml)
			assert.ErrorContains(t, err, tc.want)
		})
	}
}

func TestLoadReadsTheFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "slop-bot-config.yaml")
	require.NoError(t, os.WriteFile(path, []byte("briefing_path: b.md\n"), 0o600))
	c, err := Load(path, "/home/u")
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(dir, "b.md"), c.BriefingPath)

	_, err = Load(filepath.Join(dir, "missing.yaml"), "/home/u")
	assert.ErrorContains(t, err, "missing.yaml")
}

// `--config ./cfg.yaml` must still give absolute paths: worktree paths are
// compared with git's, which are absolute.
func TestARelativeConfigPathGivesAbsolutePaths(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "slop-bot-config.yaml")
	require.NoError(t, os.WriteFile(path, []byte("workspace_dir: ws\nrepos: [{name: a, path: repos/a}]\n"), 0o600))
	cwd, err := os.Getwd()
	require.NoError(t, err)
	rel, err := filepath.Rel(cwd, path)
	require.NoError(t, err)
	require.False(t, filepath.IsAbs(rel))

	c, err := Load(rel, "/home/u")
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(dir, "ws"), c.WorkspaceDir)
	assert.Equal(t, filepath.Join(dir, "repos", "a"), c.Repos[0].Path)
}
