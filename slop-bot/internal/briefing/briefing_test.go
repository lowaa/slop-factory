package briefing

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func data() *Data {
	return &Data{
		Ticket: Ticket{ID: "42", Title: "Add export"},
		Worktrees: []Worktree{
			{Repo: "app", Path: "/ws/t42-app", Branch: "slop-bot/t42", Primary: true},
		},
		Repos:              []Repo{{Name: "app", Path: "/src/app"}},
		CLI:                "/usr/local/bin/slop-bot",
		Nudges:             3,
		AgentsMaySetUrgent: true,
		CLIReference:       "- `slop-bot ask`",
	}
}

func TestBuiltinNamesTheTicketAndTheCLI(t *testing.T) {
	t.Parallel()
	out, err := Render(data(), "")
	require.NoError(t, err)
	assert.Contains(t, out, `ticket **42**: "Add export"`)
	assert.Contains(t, out, "`/usr/local/bin/slop-bot ask")
	assert.Contains(t, out, "`/ws/t42-app`: repo **app**, branch `slop-bot/t42` (primary")
	assert.Contains(t, out, "**app**: `/src/app`")
	assert.Contains(t, out, "after\n3 nudges")
	assert.Contains(t, out, "- `slop-bot ask`", "the generated CLI reference is included")
	assert.NotContains(t, out, "The user's briefing", "no user briefing configured")
}

func TestAutoMergeChangesWhatTheAgentIsTold(t *testing.T) {
	t.Parallel()
	without, err := Render(data(), "")
	require.NoError(t, err)
	assert.Contains(t, without, "does **not** have AutoMerge")
	assert.Contains(t, without, "ready-to-merge")

	d := data()
	d.Ticket.AutoMerge = true
	with, err := Render(d, "")
	require.NoError(t, err)
	assert.Contains(t, with, "This ticket has **AutoMerge**")
	assert.NotContains(t, with, "does **not** have AutoMerge")
}

func TestSecondaryWorktreesComeWithAReadTheirInstructionsNote(t *testing.T) {
	t.Parallel()
	single, err := Render(data(), "")
	require.NoError(t, err)
	assert.Contains(t, single, "Read each repo's own instruction files", "even one repo: a harness may read only AGENTS.md")

	d := data()
	d.Worktrees = append(d.Worktrees, Worktree{Repo: "lib", Path: "/ws/t42-lib", Branch: "slop-bot/t42"})
	multi, err := Render(d, "")
	require.NoError(t, err)
	assert.Contains(t, multi, "Read each repo's own instruction files")
	assert.Contains(t, multi, "`/ws/t42-lib`: repo **lib**")
}

func TestAutonomyAndUrgentRules(t *testing.T) {
	t.Parallel()
	d := data()
	d.Autonomy, d.AgentsMaySetUrgent = true, false
	out, err := Render(d, "")
	require.NoError(t, err)
	assert.Contains(t, out, "Autonomy is **on**")
	assert.Contains(t, out, "only the user marks tickets urgent")

	d.Autonomy, d.AgentsMaySetUrgent = false, true
	out, err = Render(d, "")
	require.NoError(t, err)
	assert.Contains(t, out, "Autonomy is **off**")
	assert.Contains(t, out, "only for\n  something breaking the pipeline")
}

func TestHarnessNotes(t *testing.T) {
	t.Parallel()
	d := data()
	d.HarnessNotes = []string{"Codex refuses `rm -rf`; use `rm -r`."}
	out, err := Render(d, "")
	require.NoError(t, err)
	assert.Contains(t, out, "## Notes for this harness\n\n- Codex refuses `rm -rf`")
}

func TestTheUsersBriefingFollowsTheBuiltinOne(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "briefing.md")
	require.NoError(t, os.WriteFile(path, []byte("Use slop-review before every PR.\n"), 0o600))
	out, err := Render(data(), path)
	require.NoError(t, err)
	builtinEnd := strings.Index(out, "# The user's briefing")
	require.Positive(t, builtinEnd)
	assert.Less(t, strings.Index(out, "Working under slop-bot"), builtinEnd)
	assert.Contains(t, out[builtinEnd:], "Use slop-review before every PR.")
}

func TestAnUnreadableUserBriefingIsAnErrorNotSkipped(t *testing.T) {
	t.Parallel()
	_, err := Render(data(), filepath.Join(t.TempDir(), "missing.md"))
	assert.ErrorIs(t, err, ErrUserBriefing)
	assert.ErrorContains(t, err, "missing.md")
}

func TestEditsApplyToTheNextRender(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "briefing.md")
	require.NoError(t, os.WriteFile(path, []byte("v1"), 0o600))
	first, err := Render(data(), path)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, []byte("v2"), 0o600))
	second, err := Render(data(), path)
	require.NoError(t, err)
	assert.Contains(t, first, "v1")
	assert.Contains(t, second, "v2")
}

func TestWritePutsTheFileOutsideTheWorktree(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "agents", "a1")
	path, text, err := Write(dir, data(), "")
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(dir, "briefing.md"), path)
	got, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, text, string(got))
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func TestARepolessAgentIsToldToAddTheRepoNotEditTheMainCheckout(t *testing.T) {
	t.Parallel()
	d := data()
	d.Worktrees = nil
	out, err := Render(d, "")
	require.NoError(t, err)
	assert.Contains(t, out, "Don't change any repo's main\ncheckout")
	assert.Contains(t, out, "ticket set 42 --repo <name>")

	with, err := Render(data(), "")
	require.NoError(t, err)
	assert.NotContains(t, with, "names no repos yet")
}

func TestTheBriefingNamesTheBranchPrefixCleanupKnows(t *testing.T) {
	t.Parallel()
	out, err := Render(data(), "")
	require.NoError(t, err)
	assert.Contains(t, out, "`slop-bot/t42/<name>`")
	assert.Contains(t, out, "ticket set 42 --status open", "refinement ends by moving the ticket on")
}
