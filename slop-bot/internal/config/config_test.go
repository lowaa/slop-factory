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
	assert.Equal(t, 30*time.Second, c.Agents.AttachLease.D())
	require.Contains(t, c.Harnesses, ClaudeCode)
	assert.Equal(t, "stream-json", c.Harnesses[ClaudeCode].Driver)
	assert.True(t, *c.Harnesses[ClaudeCode].Permissive)
	assert.False(t, c.Harnesses[ClaudeCode].APIKeyOptIn)
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
  codex: {}
  agy: {driver: tui, permissive: false}
  pi:
agents: {nudges: 5, stall_after: 30m, ask_reminder_after: 2h, attach_lease: 1m}
retention: {transcripts: 72h, events: 48h}
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
	assert.Equal(t, "rpc", c.Harnesses[Pi].Driver, "a bare harness key gets its default driver")
	assert.False(t, *c.Harnesses[Agy].Permissive)
	assert.Equal(t, 30*time.Minute, c.Agents.StallAfter.D())
	assert.Equal(t, time.Minute, c.Agents.AttachLease.D())
	assert.Equal(t, 48*time.Hour, c.Retention.Events.D())
}

func TestInvalidConfigsNameTheField(t *testing.T) {
	t.Parallel()
	cases := map[string]struct{ yml, want string }{
		"typo key":             {"workspace_dri: /x\n", "field workspace_dri not found"},
		"unknown harness":      {"harnesses: {cursor: {}}\ndefault_harness: cursor\n", "harnesses.cursor: unknown harness"},
		"bad driver":           {"harnesses: {claude-code: {driver: tui}}\n", `harnesses.claude-code.driver: "tui" isn't a driver`},
		"default not present":  {"default_harness: pi\nharnesses: {codex: {}}\n", `default_harness: "pi" isn't configured`},
		"unknown ticketing":    {"ticketing: {system: jira}\n", `ticketing.system: "jira"`},
		"duplicate repo":       {"repos: [{name: a, path: /a}, {name: a, path: /b}]\n", `repos[1].name: "a" is listed twice`},
		"unnamed repo":         {"repos: [{path: /a}]\n", "repos[0].name: is empty"},
		"repo without path":    {"repos: [{name: a}]\n", "repos[0].path: is empty"},
		"max_live too small":   {"concurrency: {max_active: 4, max_live: 2}\n", "concurrency.max_live: 2 is below"},
		"bad duration":         {"agents: {stall_after: soon}\n", `"soon" isn't a duration`},
		"zero budget":          {"autonomy: {tickets_per_agent: -1}\n", "tickets_per_agent: must be at least 1"},
		"negative concurrency": {"concurrency: {max_active: -1}\n", "max_active and urgent.max_active must be at least 1"},
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
