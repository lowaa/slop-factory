package workspace_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/workspace"
)

func TestHeadlessRunEnv_RaisesBashTimeouts(t *testing.T) {
	assert.Equal(t, "1800000", workspace.HeadlessRunEnv["BASH_DEFAULT_TIMEOUT_MS"])
	assert.Equal(t, "3600000", workspace.HeadlessRunEnv["BASH_MAX_TIMEOUT_MS"])
}

func TestHeadlessBriefing_LongCommands(t *testing.T) {
	flat := strings.Join(strings.Fields(workspace.HeadlessBriefingText("builder-1", nil, true)), " ")
	for _, want := range []string{
		"in one foreground call, each writing to its own file",
		"then `wait`",
		"before taking the integration lock",
		"Never wait with `until ! pgrep -f",
		"`while kill -0 $pid; do sleep 5; done`",
		"pass an explicit `timeout` in milliseconds",
	} {
		assert.Contains(t, flat, want)
	}
}

// These tools no longer exist, so text that names one sends an agent after a
// call that fails.
var retiredTools = []string{"ticket_wait", "integration_wait", "ticket_review", "ticket_done"}

func TestRenderedText_NamesNoRetiredTool(t *testing.T) {
	files, err := workspace.Files(workspace.Params{AgentName: "a", Server: "https://s"})
	require.NoError(t, err)
	rendered := map[string]string{
		"headless briefing": workspace.HeadlessBriefingText("a", nil, true),
		"steward briefing":  workspace.StewardBriefingText("a", nil, nil),
	}
	for _, f := range files {
		rendered[f.Path] = string(f.Body)
	}

	for name, text := range rendered {
		assert.Empty(t, retiredNamedIn(text), name)
	}
}

func TestRetiredNamedIn_FindsOne(t *testing.T) {
	assert.Equal(t, []string{"ticket_wait"}, retiredNamedIn("end your turn, or call `ticket_wait`"))
}

func retiredNamedIn(text string) []string {
	var found []string
	for _, tool := range retiredTools {
		if strings.Contains(text, tool) {
			found = append(found, tool)
		}
	}
	return found
}

// The supervisor runs slop-review for a headless run: the run asks, waits and
// labels what comes back, whatever its harness.
func TestHeadlessBriefing_ReviewIsRequestedFromTheSupervisor(t *testing.T) {
	for _, h := range []workspace.Harness{workspace.HarnessClaude, workspace.HarnessCodex} {
		flat := strings.Join(strings.Fields(workspace.HeadlessBriefingTextFor(h, "a", nil, true)), " ")
		for _, want := range []string{
			"## Reviewing your change",
			"it runs one slop-review pass over the whole change. You do not run it yourself.",
			"A test-only or small change is not an exemption",
			"call `review_request` with the pushed commit's full sha as `head_sha` and the change's PR description as `intent`",
			"Then end your turn: this is a wait point",
			"read the results with `review_findings`",
			"Validate every finding before acting on it",
			"Label each finding with `review_label`",
			"push and call `review_request` again for the new head",
			"or you have called `review_request`, commit and push, then end your turn",
			"(checks pending, lock queued, review requested)",
			"If `review_request` says no supervisor runs you, review the change as the repo's CLAUDE.md (or AGENTS.md) describes instead",
		} {
			assert.Contains(t, flat, want, h)
		}
		assert.NotContains(t, flat, "self-review personas", h)
	}
}

func TestHeadlessBriefingTextFor_ClaudeIsTheDefault(t *testing.T) {
	assert.Equal(t, workspace.HeadlessBriefingText("a", nil, true),
		workspace.HeadlessBriefingTextFor(workspace.HarnessClaude, "a", nil, true))
}

func TestHeadlessBriefingTextFor_Codex(t *testing.T) {
	codex := strings.Join(strings.Fields(workspace.HeadlessBriefingTextFor(workspace.HarnessCodex, "a", nil, true)), " ")
	for _, want := range []string{
		"`mcp__slop_haus` namespace",
		"`exec_command`",
		"`write_stdin`",
		"Never wait with `until ! pgrep -f",
		"in one call, each writing to its own file",
	} {
		assert.Contains(t, codex, want)
	}
	for _, claudeOnly := range []string{"Background shells are off", "60 minutes", "in milliseconds"} {
		assert.NotContains(t, codex, claudeOnly)
	}
	// The supervisor runs the reviewer; a run never needs to know how it is
	// installed.
	for _, h := range []workspace.Harness{workspace.HarnessClaude, workspace.HarnessCodex} {
		assert.NotContains(t, workspace.HeadlessBriefingTextFor(h, "a", nil, true), "review.sh", h)
	}
	assert.Empty(t, retiredNamedIn(codex))
}
