package workspace_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/workspace"
)

var nestedFleet = &workspace.FleetInstructions{Name: "house rules", Version: 3, Body: "Comment a plan first."}

func TestNestedRoot_AgentsMDIsSelfContainedAndMapsEveryRepo(t *testing.T) {
	f := newRepoFixture(t)
	dir := t.TempDir()

	f.setup(dir, workspace.Params{Repos: bothRepos, Fleet: nestedFleet})

	agents := fileIn(t, dir, "AGENTS.md")
	assert.Contains(t, agents, "Managed by slop-haus")
	assert.Contains(t, agents, "`app/` -- example.test/acme/app")
	assert.Contains(t, agents, "`deploy/` -- example.test/acme/deploy")
	assert.Contains(t, agents, "its paths start with that repo's path")
	assert.Contains(t, agents, "## Working agreement", "the protocol is inline")
	assert.Contains(t, agents, "> Comment a plan first.", "the fleet instructions are inline")
	for _, line := range strings.Split(agents, "\n") {
		assert.False(t, strings.HasPrefix(line, "@"), "pi does not follow @-imports: %q", line)
	}
}

func TestNestedRoot_ClaudeMDIsOnlyTheImportAndTheHeader(t *testing.T) {
	f := newRepoFixture(t)
	dir := t.TempDir()

	f.setup(dir, workspace.Params{Repos: bothRepos})

	assert.Equal(t, workspace.RootClaudeMDText(), fileIn(t, dir, "CLAUDE.md"))
	assert.Contains(t, fileIn(t, dir, "CLAUDE.md"), "\n@AGENTS.md\n")
}

func TestNestedRoot_NothingIsWrittenInsideARepo(t *testing.T) {
	f := newRepoFixture(t)
	dir := t.TempDir()
	f.setup(dir, workspace.Params{Repos: bothRepos})
	before := map[string]map[string]string{
		"app":    snapshot(t, filepath.Join(dir, "app")),
		"deploy": snapshot(t, filepath.Join(dir, "deploy")),
	}

	f.update(dir, workspace.Params{Repos: bothRepos, Fleet: nestedFleet})

	for name, was := range before {
		assert.Equal(t, was, snapshot(t, filepath.Join(dir, name)), name)
		for _, written := range []string{"CLAUDE.md", "AGENTS.md", ".gitignore", ".slop-haus", ".mcp.json", ".claude"} {
			assert.NoFileExists(t, filepath.Join(dir, name, written))
			assert.NoDirExists(t, filepath.Join(dir, name, written))
		}
	}
	assert.NoFileExists(t, filepath.Join(dir, ".gitignore"))
}

func TestNestedRoot_AnUpdateRewritesAHandEdit(t *testing.T) {
	f := newRepoFixture(t)
	dir := t.TempDir()
	f.setup(dir, workspace.Params{Repos: bothRepos})
	require.NoError(t, os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("mine"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte("mine"), 0o644))

	f.update(dir, workspace.Params{Repos: bothRepos, Fleet: nestedFleet})

	assert.Contains(t, fileIn(t, dir, "AGENTS.md"), "> Comment a plan first.")
	assert.Equal(t, workspace.RootClaudeMDText(), fileIn(t, dir, "CLAUDE.md"))
}

func TestNestedRoot_ARootThatIsARepoKeepsItsOwnInstructionFiles(t *testing.T) {
	f := newRepoFixture(t)
	dir := t.TempDir()
	gitInit(t, dir)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte("# theirs\n"), 0o644))

	f.setup(dir, workspace.Params{Repos: bothRepos})

	assert.Equal(t, "# theirs\n", fileIn(t, dir, "CLAUDE.md"), "a repo's own file is never overwritten or appended to")
	assert.NoFileExists(t, filepath.Join(dir, "AGENTS.md"))
	assert.NoFileExists(t, filepath.Join(dir, ".gitignore"))
}

func TestNestedRoot_SlopHausFilesAreIgnoredIfTheRootIsARepoAnyway(t *testing.T) {
	f := newRepoFixture(t)
	dir := t.TempDir()
	gitInit(t, dir)

	f.setup(dir, workspace.Params{Repos: bothRepos})

	out, err := exec.Command("git", "-C", dir, "check-ignore", "-q", "--no-index", ".slop-haus/credentials.json").CombinedOutput()
	require.NoError(t, err, "credentials must not be committable: %s", out)
}

func TestNestedRoot_ProtocolFollowsTheRole(t *testing.T) {
	const headless = "This session is a headless run"
	cases := map[string]struct {
		role     workspace.Role
		contains string
		absent   []string
	}{
		"slot":      {workspace.RoleHeadless, headless, []string{"Remote Control session"}},
		"conductor": {workspace.RoleConductor, "This is a Remote Control session", []string{headless, "## Working agreement", "ticket_next"}},
		"console":   {workspace.RoleConsole, "the person's own interactive session", []string{headless, "## Working agreement", "ticket_next"}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			f := newRepoFixture(t)
			dir := t.TempDir()

			f.setup(dir, workspace.Params{Repos: bothRepos, Fleet: nestedFleet, Role: c.role})

			agents := fileIn(t, dir, "AGENTS.md")
			assert.Contains(t, agents, c.contains)
			for _, a := range c.absent {
				assert.NotContains(t, agents, a)
			}
			assert.Contains(t, agents, "Managed by slop-haus")
			assert.Contains(t, agents, "`app/` -- example.test/acme/app")
			assert.Contains(t, agents, "> Comment a plan first.", "the fleet instructions are in every kind's")
		})
	}
}

func TestNestedRoot_OnlyTheConductorIsToldToPointRoutineDiscussionsAtTheSteward(t *testing.T) {
	const guidance = "Answer with steward"
	for role, want := range map[workspace.Role]bool{workspace.RoleConductor: true, workspace.RoleHeadless: false, workspace.RoleConsole: false} {
		f := newRepoFixture(t)
		dir := t.TempDir()

		f.setup(dir, workspace.Params{Repos: bothRepos, Fleet: nestedFleet, Role: role})

		agents := fileIn(t, dir, "AGENTS.md")
		assert.Equal(t, want, strings.Contains(agents, guidance), "role %v", role)
		if want {
			assert.Contains(t, agents, "If they insist, answer as you")
		}
	}
}
