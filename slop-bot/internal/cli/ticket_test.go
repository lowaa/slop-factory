package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-bot/internal/api"
)

func (h *harness) ok(t *testing.T, args ...string) string {
	t.Helper()
	code := h.run(t, args...)
	require.Equal(t, 0, code, "slop-bot %v: %s", args, h.err.String())
	return h.out.String()
}

func (h *harness) ticket(t *testing.T, args ...string) api.Ticket {
	t.Helper()
	var tk api.Ticket
	require.NoError(t, json.Unmarshal([]byte(h.ok(t, append(args, "--json")...)), &tk))
	return tk
}

func TestARepoNotInTheConfigIsRefused(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	require.NoError(t, os.WriteFile(h.config, []byte("user: ada\nharnesses: {claude-code: {}}\n"+
		"repos: [{name: app, path: "+filepath.Join(h.home, "app")+"}]\n"), 0o600))
	startDaemon(t, h)

	assert.Equal(t, 1, h.run(t, "ticket", "create", "--title", "t", "--repo", "app", "--repo", "typo"))
	assert.Contains(t, h.err.String(), `repo "typo" isn't in this machine's config (repos: app)`)
	tk := h.ticket(t, "ticket", "create", "--title", "t", "--repo", "app")
	assert.Equal(t, 1, h.run(t, "ticket", "set", tk.ID, "--repo", "app", "--repo", "app"))
	assert.Contains(t, h.err.String(), `repo "app" is named twice`)
	assert.Equal(t, 1, h.run(t, "ticket", "set", tk.ID, "--repo", "typo"))
	assert.Contains(t, h.ok(t, "ticket", "show", tk.ID), "app")
}

func TestTicketLifecycleThroughTheCLI(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	require.NoError(t, os.WriteFile(h.config, []byte("user: ada\nharnesses: {claude-code: {}, pi: {}}\n"+
		"repos: [{name: app, path: "+filepath.Join(h.home, "app")+"}]\n"), 0o600))
	startDaemon(t, h)

	first := h.ticket(t, "ticket", "create", "--title", "Add export", "--body", "CSV, please", "--harness", "pi", "--effort", "low", "--repo", "app", "--assign")
	assert.Equal(t, "preparing", first.Status)
	assert.Equal(t, "ada", first.Assignee, "--assign is slop-bot's configured user")
	assert.Equal(t, api.AgentSpec{Harness: "pi", Effort: "low"}, first.Agent)
	assert.Equal(t, api.Principal{Kind: "user"}, first.CreatedBy)

	second := h.ticket(t, "ticket", "create", "--title", "Schema first", "--status", "open")
	out := h.ok(t, "ticket", "block", first.ID, "--on", second.ID)
	assert.Contains(t, out, first.ID+" now waits on "+second.ID)

	shown := h.ticket(t, "ticket", "show", first.ID)
	require.NotNil(t, shown.Blockers)
	require.Len(t, shown.Blockers.BlockedBy, 1)
	assert.Equal(t, second.ID, shown.Blockers.BlockedBy[0].Ticket.ID)

	text := h.ok(t, "ticket", "show", first.ID)
	assert.Contains(t, text, "# "+first.ID+": Add export")
	assert.Contains(t, text, "waits on "+second.ID)
	assert.Contains(t, text, "CSV, please")

	list := h.ok(t, "ticket", "list", "--mine")
	assert.Contains(t, list, "Add export")
	assert.NotContains(t, list, "Schema first", "unassigned")
	assert.Contains(t, list, "blocked")

	set := h.ticket(t, "ticket", "set", first.ID, "--auto-merge", "--urgent", "--reason", "main is red")
	assert.True(t, set.AutoMerge)
	assert.True(t, set.Urgent)

	h.ok(t, "ticket", "comment", first.ID, "Decided: CSV only, no XLSX")
	assert.Contains(t, h.ok(t, "ticket", "show", first.ID), "Decided: CSV only, no XLSX")

	assert.Contains(t, h.ok(t, "ticket", "unblock", first.ID, "--on", second.ID), "no longer waits on")
	assert.Contains(t, h.ok(t, "ticket", "unblock", first.ID, "--on", second.ID), "wasn't waiting on")

	assert.Contains(t, h.ok(t, "ticket", "assign", second.ID), "assignee: ada")
	assert.Contains(t, h.ok(t, "ticket", "cancel", second.ID), "cancelled")
}

func TestABadAgentSpecIsRefusedAtFilingAndEdit(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	startDaemon(t, h)

	assert.Equal(t, 1, h.run(t, "ticket", "create", "--title", "x", "--harness", "agy"))
	assert.Contains(t, h.err.String(), `harness "agy" isn't configured on this machine`)

	assert.Equal(t, 1, h.run(t, "ticket", "create", "--title", "x", "--effort", "minimal"))
	assert.Contains(t, h.err.String(), `effort "minimal" isn't one claude-code accepts`, "blank harness resolves to default_harness")

	tk := h.ticket(t, "ticket", "create", "--title", "x")
	assert.Equal(t, 1, h.run(t, "ticket", "set", tk.ID, "--harness", "cursor"))
	assert.Contains(t, h.err.String(), `harness "cursor"`)
}

func TestSettingOneAgentFieldKeepsTheOthers(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	startDaemon(t, h)

	tk := h.ticket(t, "ticket", "create", "--title", "x", "--harness", "pi", "--model", "m1")
	set := h.ticket(t, "ticket", "set", tk.ID, "--effort", "low")
	assert.Equal(t, api.AgentSpec{Harness: "pi", Model: "m1", Effort: "low"}, set.Agent)
	assert.Equal(t, set.Agent, h.ticket(t, "ticket", "show", tk.ID).Agent, "persisted, not just echoed")

	cleared := h.ticket(t, "ticket", "set", tk.ID, "--model", "")
	assert.Equal(t, api.AgentSpec{Harness: "pi", Effort: "low"}, cleared.Agent, "an explicit empty clears just that field")
}

func TestTicketErrorsCarryTheReason(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	startDaemon(t, h)
	assert.Equal(t, 1, h.run(t, "ticket", "show", "999"))
	assert.Contains(t, h.err.String(), "not found")
	a := h.ticket(t, "ticket", "create", "--title", "a")
	b := h.ticket(t, "ticket", "create", "--title", "b", "--blocked-on", a.ID)
	assert.Equal(t, 1, h.run(t, "ticket", "block", a.ID, "--on", b.ID))
	assert.Contains(t, h.err.String(), "cycle")
}
