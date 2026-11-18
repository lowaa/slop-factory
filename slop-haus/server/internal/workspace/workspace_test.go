package workspace_test

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/workspace"
)

// install writes the workspace's files into dir the way the CLI does, and
// returns the hook's path. The tests below drive the real thing rather
// than a re-creation of it.
func install(t *testing.T, dir string, p workspace.Params) string {
	t.Helper()

	if p.HookCommand == "" {
		p.HookCommand = workspace.HookPathIn(dir)
	}
	if p.SessionStartHookCommand == "" {
		p.SessionStartHookCommand = workspace.SessionStartHookPathIn(dir)
	}
	if p.UsageHookCommand == "" {
		p.UsageHookCommand = workspace.UsageHookPathIn(dir)
	}
	if p.UserPromptHookCommand == "" {
		p.UserPromptHookCommand = workspace.UserPromptHookPathIn(dir)
	}
	if p.TokenCommand == "" {
		p.TokenCommand = workspace.TokenPathIn(dir)
	}
	files, err := workspace.Files(p)
	require.NoError(t, err)

	for _, file := range files {
		path := filepath.Join(dir, filepath.FromSlash(file.Path))
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, file.Body, os.FileMode(file.Mode)))
		require.NoError(t, os.Chmod(path, os.FileMode(file.Mode)))
	}
	return workspace.HookPathIn(dir)
}

func fileIn(t *testing.T, dir, rel string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
	require.NoError(t, err)
	return string(body)
}

// Between them settings.json and .mcp.json are the entire integration, so
// their shapes are pinned: a typo in a hook key, or the MCP server in the
// wrong file, is a workspace that silently never joins the fleet.
func TestFiles_SettingsCarryHooksAndNotMCP(t *testing.T) {
	dir := t.TempDir()
	install(t, dir, workspace.Params{AgentName: "builder-1", Server: "https://slop-haus.example/"})

	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(fileIn(t, dir, workspace.Settings)), &got))

	// The bug this caught: Claude Code does not read an mcpServers key
	// from settings.json at all. Put it here and the tools never load,
	// with no error anywhere -- which is exactly what happened, and which
	// no other test could see, because they all drove MCP directly.
	assert.NotContains(t, got, "mcpServers",
		"MCP belongs in .mcp.json; settings.json ignores it silently")

	hooks := got["hooks"].(map[string]any)

	// SessionStart used to be an http hook -- it only ever injects context,
	// which a plain response body can carry -- but an http hook's headers
	// can only ever be a fixed string, and this workspace has no such
	// string any more: the access token comes from token.sh, refreshed on
	// demand. So it is a command hook now too, exactly like Stop.
	start := hooks["SessionStart"].([]any)[0].(map[string]any)["hooks"].([]any)[0].(map[string]any)
	assert.Equal(t, "command", start["type"])
	assert.EqualValues(t, 10, start["timeout"])
	assert.Contains(t, start["command"], "https://slop-haus.example")
	assert.Contains(t, start["command"], workspace.SessionStartHookPathIn(dir),
		"an absolute path, so the hook does not depend on the agent's PATH or cwd")
	assert.NotContains(t, start, "headers", "no fixed credential to put in a header any more")
	assert.NotContains(t, start, "allowedEnvVars", "the script reads its own files, not an environment variable")

	// Stop has to be a command hook. An http hook's response can say
	// decision: "block" and Claude Code will surface the reason and let
	// the agent stop anyway; only exit code 2 sends it back to work. This
	// was measured against a real session, not inferred.
	stop := hooks["Stop"].([]any)[0].(map[string]any)["hooks"].([]any)[0].(map[string]any)
	assert.Equal(t, "command", stop["type"],
		"an http Stop hook cannot block; only a command hook exiting 2 can")
	assert.Contains(t, stop["command"], "https://slop-haus.example",
		"the hook must know which server to ask")
	assert.Contains(t, stop["command"], workspace.HookPathIn(dir),
		"an absolute path, so the hook does not depend on the agent's PATH or cwd")
	assert.NotContains(t, stop["command"], "slop hook-stop",
		"the hook must not require a binary on the agent's machine")
	// Long enough for token.sh's lock wait and for hook-stop.sh to ride out
	// a server restart; killed sooner, the agent just stops.
	assert.EqualValues(t, 90, stop["timeout"])
	assert.NotContains(t, stop, "async", "the decision is the whole point of this one")

	// The usage hook rides beside the decision on Stop, and on every
	// SubagentStop. Async, so Claude Code neither waits for it nor reads
	// its exit code: it can't delay or change the Stop decision.
	stopGroups := hooks["Stop"].([]any)
	require.Len(t, stopGroups, 2)
	subagentGroups := hooks["SubagentStop"].([]any)
	require.Len(t, subagentGroups, 1)
	for event, group := range map[string]any{"Stop": stopGroups[1], "SubagentStop": subagentGroups[0]} {
		assert.NotContains(t, group, "matcher", "%s: every subagent type, nested ones included", event)
		usage := group.(map[string]any)["hooks"].([]any)[0].(map[string]any)
		assert.Equal(t, "command", usage["type"], event)
		assert.Equal(t, true, usage["async"], event)
		assert.EqualValues(t, 150, usage["timeout"], event)
		assert.Contains(t, usage["command"], workspace.UsageHookPathIn(dir), event)
		assert.Contains(t, usage["command"], "https://slop-haus.example", event)
	}
}

// .mcp.json is the file Claude Code actually reads MCP servers from.
func TestFiles_MCPConfigIsTheShapeClaudeCodeReads(t *testing.T) {
	dir := t.TempDir()
	install(t, dir, workspace.Params{AgentName: "builder-1", Server: "https://slop-haus.example/"})

	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(fileIn(t, dir, workspace.MCPConfig)), &got))

	server := got["mcpServers"].(map[string]any)["slop-haus"].(map[string]any)
	assert.Equal(t, "http", server["type"])
	assert.Equal(t, "https://slop-haus.example/mcp", server["url"], "one slash, not two")
	assert.NotContains(t, server, "headers",
		"a fixed header would only ever be a static credential, which this workspace no longer has")
	assert.Contains(t, server["headersHelper"], workspace.TokenPathIn(dir))
	// Claude Code's default for an http server is about a minute, which
	// would cut a tool call that talks to the code host off.
	assert.EqualValues(t, workspace.ToolCallTimeout.Milliseconds(), server["timeout"])
}

// headersHelper is what spares an agent a second, browser-only sign-in:
// Claude Code runs it through a shell, from wherever it likes, and sends
// the Authorization header it prints. A space in the workspace path is the
// common case on a mac and would split the command in two unquoted.
func TestFiles_MCPHeadersHelperPrintsTheWorkspacesToken(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "My Repo")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	install(t, dir, workspace.Params{AgentName: "builder-1", Server: "https://slop-haus.example"})
	seedValidCredentials(t, dir, "agent-token")

	assert.Equal(t, map[string]string{"Authorization": "Bearer agent-token"}, runHeadersHelper(t, dir))
}

// runHeadersHelper runs .mcp.json's headersHelper the way Claude Code does
// -- through a shell, outside the workspace -- and decodes what it prints.
func runHeadersHelper(t *testing.T, dir string) map[string]string {
	t.Helper()
	var mcp map[string]any
	require.NoError(t, json.Unmarshal([]byte(fileIn(t, dir, workspace.MCPConfig)), &mcp))
	command := mcp["mcpServers"].(map[string]any)["slop-haus"].(map[string]any)["headersHelper"].(string)

	cmd := exec.Command("sh", "-c", command)
	cmd.Dir = t.TempDir()
	out, err := cmd.Output()
	require.NoError(t, err)
	var headers map[string]string
	require.NoError(t, json.Unmarshal(out, &headers), "headersHelper must print a JSON object: %s", out)
	return headers
}

// No file this package writes carries a credential any more: an agent's
// only one is .slop-haus/credentials.json, which token.sh writes at run
// time and Files never touches.
func TestFiles_NoWrittenFileCarriesACredential(t *testing.T) {
	dir := t.TempDir()
	install(t, dir, workspace.Params{AgentName: "builder-1", Server: "https://slop-haus.example"})

	env := fileIn(t, dir, workspace.EnvFile)
	assert.Contains(t, env, "SLOP_HAUS_URL=")
	assert.Contains(t, env, "SLOP_HAUS_AGENT=")
	assert.NotContains(t, env, "SLOP_HAUS_TOKEN", "the old static token is dead; nothing should still mention it")

	for _, rel := range []string{workspace.Settings, workspace.LocalSettings, workspace.MCPConfig, workspace.HeadlessBriefing, workspace.EnvFile} {
		body := fileIn(t, dir, rel)
		assert.NotContains(t, body, "Bearer ", "%s must not carry a bearer credential", rel)
	}
}

// Nothing secret lives here any more, so there is nothing left to protect
// with a restrictive mode -- and an update rewriting it (to migrate away an
// older workspace's dead SLOP_HAUS_TOKEN line) should not have to worry
// about preserving one either.
func TestFiles_EnvFileHasNoSecretSoItNeedsNoRestrictiveMode(t *testing.T) {
	dir := t.TempDir()
	install(t, dir, workspace.Params{AgentName: "builder-1", Server: "https://slop-haus.example"})

	info, err := os.Stat(filepath.Join(dir, workspace.EnvFile))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o644), info.Mode().Perm())
}

// Allow rules in the shared settings file wait for the workspace-trust
// step; the local one's apply immediately. An agent that has to approve
// each tool on first use cannot work unattended at all.
func TestFiles_ToolPermissionsGoInTheLocalFile(t *testing.T) {
	dir := t.TempDir()
	install(t, dir, workspace.Params{AgentName: "builder-1", Server: "https://slop-haus.example"})

	assert.Contains(t, fileIn(t, dir, workspace.LocalSettings), "mcp__slop-haus__*")
	assert.NotContains(t, fileIn(t, dir, workspace.Settings), "permissions",
		"permissions in the shared file wait for trust; they belong in the local one")
}

// Claude Code runs a command hook through a shell, and the hook lives at a
// path the human chose. A workspace under "~/My Repo" is the common case
// on a mac, and unquoted it is two words: the hook never runs, fails open,
// and looks exactly like an agent with nothing left to do.
func TestFiles_HookCommandSurvivesASpaceInTheWorkspacePath(t *testing.T) {
	blocked := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"hookSpecificOutput":{"decision":"block","reason":"still yours"}}`)
	}))
	defer blocked.Close()

	dir := filepath.Join(t.TempDir(), "My Repo")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	install(t, dir, workspace.Params{AgentName: "builder-1", Server: blocked.URL})
	seedValidCredentials(t, dir, "agent-token")
	require.Contains(t, dir, " ", "the point of this test is a path with a space in it")

	var settings map[string]any
	require.NoError(t, json.Unmarshal([]byte(fileIn(t, dir, workspace.Settings)), &settings))
	hooks := settings["hooks"].(map[string]any)
	command := hooks["Stop"].([]any)[0].(map[string]any)["hooks"].([]any)[0].(map[string]any)["command"].(string)

	cmd := exec.Command("sh", "-c", command)
	cmd.Stdin = strings.NewReader("{}")
	cmd.Env = os.Environ()
	var stderr stringWriter
	cmd.Stderr = &stderr
	cmd.Stdout = io.Discard

	assert.Equal(t, 2, exitCodeOf(t, cmd.Run()),
		"the hook has to run, and block, from a path with a space in it")
	assert.Contains(t, stderr.text, "still yours")
}

// The briefing names the agent it was written for -- which is why it
// cannot live in a repo's single tracked CLAUDE.md.
func TestFiles_BriefingNamesItsAgent(t *testing.T) {
	dir := t.TempDir()
	install(t, dir, workspace.Params{AgentName: "reviewer-3", Server: "https://slop-haus.example"})

	assert.Contains(t, fileIn(t, dir, workspace.HeadlessBriefing), "reviewer-3")
}

// There is no chat room: nothing a workspace is given -- the briefings, the
// headless briefing, the hooks -- may send an agent to one.
func TestFiles_NothingPointsAnAgentAtAChatRoom(t *testing.T) {
	dir := t.TempDir()
	install(t, dir, workspace.Params{AgentName: "builder-1", Server: "https://slop-haus.example"})

	var seen int
	require.NoError(t, filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		require.NoError(t, err)
		if d.IsDir() {
			return nil
		}
		body, err := os.ReadFile(path)
		require.NoError(t, err)
		seen++
		// A model provider's name is not a room, and the temp dir is named
		// for this test.
		text := strings.ReplaceAll(strings.ReplaceAll(string(body), "ChatGPT", ""), dir, "")
		assert.NotContains(t, strings.ToLower(text), "chat", path)
		return nil
	}))
	assert.Greater(t, seen, 3, "the walk found the installed files")
	assert.NotContains(t, strings.ToLower(workspace.HeadlessBriefingText("builder-1", nil, false)), "chat")
}

func TestFiles_HookIsExecutableAndIsThisPackagesScript(t *testing.T) {
	dir := t.TempDir()
	install(t, dir, workspace.Params{AgentName: "builder-1", Server: "https://slop-haus.example"})

	info, err := os.Stat(workspace.HookPathIn(dir))
	require.NoError(t, err)
	assert.NotZero(t, info.Mode().Perm()&0o100,
		"Claude Code runs this directly; without +x every Stop fails open and looks like an idle agent")
	assert.Equal(t, string(workspace.Hook()), fileIn(t, dir, workspace.HookScript))
}

// fileBody is a file's contents as Files produces them.
func fileBody(t *testing.T, rel string) string {
	t.Helper()
	files, err := workspace.Files(workspace.Params{AgentName: "builder-1", Server: "https://slop-haus.example"})
	require.NoError(t, err)
	for _, f := range files {
		if f.Path == rel {
			return string(f.Body)
		}
	}
	t.Fatalf("no file %s in the workspace", rel)
	return ""
}

// Ownership is what an update may do to a file, so getting one wrong either
// destroys something (a token, a human's permission rules) or leaves a
// workspace stale forever.
func TestFiles_EachFileHasTheRightOwner(t *testing.T) {
	files, err := workspace.Files(workspace.Params{AgentName: "a", Server: "https://s"})
	require.NoError(t, err)

	owners := map[string]workspace.Ownership{}
	for _, f := range files {
		owners[f.Path] = f.Ownership
	}
	assert.Equal(t, workspace.Seeded, owners[workspace.LocalSettings])
	assert.Equal(t, workspace.Seeded, owners[workspace.LauncherEnv])
	for _, managed := range []string{
		workspace.EnvFile, workspace.HookScript, workspace.SessionStartHookScript, workspace.TokenScript,
		workspace.Updater, workspace.RepoSyncer, workspace.ServerURL,
		workspace.Settings, workspace.MCPConfig,
		workspace.SessionDescriber, workspace.PiExtension, workspace.HeadlessBriefing,
		workspace.UsageHookScript, workspace.UserPromptHookScript, workspace.UsageReporter, workspace.TokenModule, workspace.RemoteControlLauncher, workspace.AppInstaller,
		workspace.AgentsMD, workspace.ClaudeMD, workspace.GitignoreAll, workspace.BundleHashFile,
	} {
		assert.Equal(t, workspace.Managed, owners[managed], managed)
	}
	assert.Len(t, owners, 24, "a new file needs an ownership decision here")
}

// The launcher is run by hand, so it has to be executable; the env file it
// reads is where a workspace names its secrets, so nobody else reads it.
func TestFiles_LauncherIsExecutableAndItsEnvIsPrivate(t *testing.T) {
	dir := t.TempDir()
	install(t, dir, workspace.Params{AgentName: "builder-1", Server: "https://slop-haus.example"})

	assert.Equal(t, os.FileMode(0o755), modeIn(t, dir, workspace.RemoteControlLauncher))
	assert.Equal(t, os.FileMode(0o755), modeIn(t, dir, workspace.AppInstaller))
	assert.Equal(t, os.FileMode(0o600), modeIn(t, dir, workspace.LauncherEnv))
}

func TestFiles_UpdaterIsExecutableAndIsThisPackagesScript(t *testing.T) {
	files, err := workspace.Files(workspace.Params{AgentName: "a", Server: "https://s"})
	require.NoError(t, err)

	onDisk, err := os.ReadFile("assets/sh/update.sh")
	require.NoError(t, err)
	for _, f := range files {
		if f.Path == workspace.Updater {
			assert.Equal(t, uint32(0o755), f.Mode)
			assert.Equal(t, string(onDisk), string(f.Body))
			return
		}
	}
	t.Fatal("no updater in the workspace")
}

// The updater appends /api/... to this, so a trailing slash would make
// every request a double-slash path.
func TestFiles_ServerURLIsTheBareBaseAddress(t *testing.T) {
	files, err := workspace.Files(workspace.Params{AgentName: "a", Server: "https://slop-haus.example/"})
	require.NoError(t, err)
	for _, f := range files {
		if f.Path == workspace.ServerURL {
			assert.Equal(t, "https://slop-haus.example\n", string(f.Body))
			return
		}
	}
	t.Fatal("no server-url in the workspace")
}

// What the worker-rules file used to add to a headless run's prompt lives in
// the briefing itself now. Losing any of these means tickets left held or
// handoffs nobody can resume from.
func TestFiles_HeadlessBriefingKeepsTheWorkerRules(t *testing.T) {
	flat := strings.Join(strings.Fields(workspace.HeadlessBriefingText("builder-1", nil, false)), " ")

	for _, rule := range []string{
		"ticket_scope_narrow", "ticket_comment", "ticket_finish", "ticket_release",
		"where the work stands (branch and the pushed commit, what is done and what isn't), what was decided and why, and what you would do next",
		"commit and push your work to the ticket's branch before `ticket_discuss` (and before `ticket_finish`)",
		"re-read the ticket's comments with `ticket_get`",
		"`ticket_release` with `blocked_on`",
		"`[human-only]` in the `ticket_discuss` question",
	} {
		assert.Contains(t, flat, rule)
	}
	assert.Contains(t, workspace.HeadlessBriefingText("builder-1", nil, true), "`[human-only]`", "both briefing variants carry the rules")
	assert.NotContains(t, flat, "main session")
	assert.NotContains(t, flat, "dispatcher")
	assert.Equal(t, 1, strings.Count(flat, "## Splitting large tickets"), "the splitting rules are said once")
}

// A headless run is handed its ticket and branch and is resumed by the
// supervisor, so its briefing must not send it to claim or wait, and must tell
// it to end its turn at a wait point.
func TestFiles_HeadlessBriefingLeavesClaimingAndWaitingToTheSupervisor(t *testing.T) {
	for _, changeTools := range []bool{false, true} {
		briefing := workspace.HeadlessBriefingText("builder-1", nil, changeTools)
		flat := strings.Join(strings.Fields(briefing), " ")

		assert.Contains(t, briefing, "# slop-haus agent: builder-1")
		assert.Contains(t, flat, "headless run")
		// The tools are denied, so the text may name them only to forbid them.
		assert.NotContains(t, flat, "Take work with")
		assert.NotContains(t, flat, "Wait with a command that blocks")
		assert.NotContains(t, flat, "`ticket_next` and `ticket_get` say")
		assert.NotContains(t, flat, "hand the ticket to the")
		assert.Contains(t, flat, "`ticket_next` is denied to you")

		assert.Contains(t, flat, "if it is held you are queued: push, then end your turn")
		assert.Contains(t, flat, "`change_checks` says the checks are pending, or you have called `review_request`, commit and push, then end your turn")
		assert.Contains(t, flat, `"you now hold the integration lock"`)
		assert.Contains(t, flat, `"checks finished"`)
		assert.Contains(t, flat, "Never poll, sleep, loop or retry on a timer")
		assert.Contains(t, flat, "Never wait on a background task or shell")
		assert.Contains(t, flat, "Push before you end any turn at a wait point")
		assert.Contains(t, flat, `resumed with "call `+"`checkin`"+`"`)
		assert.Contains(t, flat, "Always finish with a closing call")
		for _, finish := range []string{"ticket_finish", "ticket_release", "ticket_discuss", "ticket_prepare"} {
			assert.Contains(t, flat, "`"+finish+"`")
		}
		assert.Contains(t, flat, "Work on the branch the supervisor named in your first message")

		// change_checks is named by the waiting rule in both variants.
		for _, tool := range []string{"change_open", "change_merge"} {
			if changeTools {
				assert.Contains(t, flat, "`"+tool+"`")
			} else {
				assert.NotContains(t, flat, "`"+tool+"`")
			}
		}
	}
}

// The file the supervisor reads is the same text, through the workspace.
func TestFiles_HeadlessBriefingIsWrittenIntoTheWorkspace(t *testing.T) {
	files, err := workspace.Files(workspace.Params{AgentName: "builder-1", Server: "https://s"})
	require.NoError(t, err)
	for _, f := range files {
		if f.Path == workspace.HeadlessBriefing {
			assert.Equal(t, workspace.HeadlessBriefingText("builder-1", nil, false), string(f.Body))
			return
		}
	}
	t.Fatal("no headless briefing in the workspace files")
}

func TestHeadlessRunDenyList(t *testing.T) {
	assert.ElementsMatch(t, []string{
		"mcp__slop-haus__ticket_next", "Monitor",
	}, workspace.HeadlessDeniedTools)
	assert.Equal(t, "1", workspace.HeadlessRunEnv["CLAUDE_CODE_DISABLE_BACKGROUND_TASKS"])
}

// Preparing is the one status agents can put a ticket into but never take
// it out of. What happens when a human imposes it mid-ticket is the
// revocation notice's to say, where it is delivered.
func TestFiles_BriefingExplainsPreparing(t *testing.T) {
	for name, body := range map[string]string{
		"headless": workspace.HeadlessBriefingText("builder-1", nil, false),
	} {
		t.Run(name, func(t *testing.T) {
			flat := strings.Join(strings.Fields(body), " ")
			for _, rule := range []string{
				"send it back to preparing with `ticket_prepare` and a reason",
				"Tickets you file with `ticket_create` start in preparing",
			} {
				assert.Contains(t, flat, rule)
			}
		})
	}
}

// Every ticket lands through a reviewable change that records its URL, and
// both roles are told so in the order that keeps main tested: main is merged
// in and tested before the lock, which is then held only for a re-check
// and the merge. The steps are the repo's to map onto a host -- slop-haus
// stays neutral, so no text may name one.
func TestFiles_BriefingLandsWorkThroughAReviewableChange(t *testing.T) {
	for name, c := range map[string]struct{ body, comesBack string }{
		"headless": {
			workspace.HeadlessBriefingText("builder-1", nil, false),
			"`ticket_get` says what to do with it when it comes back",
		},
	} {
		body := c.body
		t.Run(name, func(t *testing.T) {
			flat := strings.Join(strings.Fields(body), " ")
			assert.Contains(t, flat, "reviewable change (a pull request)")
			assert.Contains(t, flat, "repo's CLAUDE.md")
			assert.Contains(t, flat, "as `pr_url`")
			// Whatever the repo's CLAUDE.md says about describing it, the
			// change links back to its ticket, by the link slop-haus hands
			// out rather than one the agent pieces together.
			assert.Contains(t, flat, "`slop-haus #<id>`")
			assert.Contains(t, flat, "`url`")
			assert.Contains(t, flat, "`ticket_get` returns")

			steps := []string{
				"push your branch", "open", "draft", "push as the work goes", "out of draft", "merge the base branch into your branch", "re-run the tests",
				"push", "before the lock", "`integration_lock`", "re-check", "merge the change", "release the lock", "`ticket_finish`",
			}
			// Each step is looked for after the one before, so a step that
			// is missing or out of order fails rather than matching an
			// earlier mention elsewhere in the text.
			rest := flat
			for _, step := range steps {
				at := strings.Index(rest, step)
				require.GreaterOrEqual(t, at, 0, "step %q is missing or out of order", step)
				rest = rest[at+len(step):]
			}

			// A ticket that requires approval goes to review instead of being
			// merged; ticket_next and ticket_get spell out each stage after.
			review := []string{
				"requires human approval", "out of draft", "`ticket_finish` with its URL as `pr_url` and a handoff",
				c.comesBack,
			}
			rest = flat
			for _, step := range review {
				at := strings.Index(rest, step)
				require.GreaterOrEqual(t, at, 0, "review step %q is missing or out of order", step)
				rest = rest[at+len(step):]
			}

			lower := strings.ToLower(body)
			for _, host := range []string{"github", "gitlab", "gh pr", "bitbucket"} {
				assert.NotContains(t, lower, host, "slop-haus's own instructions name no host")
			}
		})
	}
	lower := strings.ToLower(workspace.HeadlessBriefingText("builder-1", nil, true))
	for _, host := range []string{"github", "gitlab", "gh pr", "bitbucket"} {
		assert.NotContains(t, lower, host, "slop-haus's own instructions name no host")
	}
}

// pi's extension is an MCP client and hook runner of its own, so it carries
// its own copy of the timeouts Claude Code is configured with here. Out of
// step, pi would cut a tool call off or kill the Stop hook mid-retry.
func TestPiExtension_TimeoutsMatchClaudeCodes(t *testing.T) {
	src, err := os.ReadFile("assets/ts/pi-extension.ts")
	require.NoError(t, err)

	assert.Contains(t, string(src),
		fmt.Sprintf("const TOOL_CALL_TIMEOUT_MS = %d * 60_000;", int(workspace.ToolCallTimeout/time.Minute)))
	assert.Contains(t, string(src),
		fmt.Sprintf("const SCRIPT_TIMEOUT_MS = %d_000;", workspace.StopHookTimeout))
}

// A repo that defines none of its policy used to leave workers improvising;
// both briefings name the slots and a fallback for each.
func TestFiles_BriefingsStateTheRepoContractWithFallbacks(t *testing.T) {
	cases := map[string]string{
		"headless": workspace.HeadlessBriefingText("builder-1", nil, false),
	}
	for name, text := range cases {
		t.Run(name, func(t *testing.T) {
			flat := strings.Join(strings.Fields(text), " ")
			assert.Contains(t, flat, "**Tests**: the command that runs them. Fallback: find the project's test entrypoint, run it, and say on the ticket what you ran")
			assert.Contains(t, flat, "**Self-review**: how to review a change before it lands, when no supervisor reviews it for you (see \"Reviewing your change\"). Fallback: review the diff yourself")
			assert.Contains(t, flat, "**Exceptions to the PR flow**: changes that may skip it. Fallback: none -- every change goes through a pull request")
			assert.Contains(t, flat, "never lifts a ticket's \"requires human approval\"")
		})
	}
}

// Where a ticket's branch comes from: the one slop-haus made at its pinned
// base commit, and an agent-made one from origin/main only when the ticket
// has none.
func TestFiles_AgentsSwitchToTheBranchSlopHausMade(t *testing.T) {
	flat := func(s string) string { return strings.Join(strings.Fields(s), " ") }
	texts := map[string]string{
		"headless": workspace.HeadlessBriefingText("builder-1", nil, true),
	}
	for name, body := range texts {
		t.Run(name, func(t *testing.T) {
			got := flat(body)
			assert.Contains(t, got, "`git switch <branch>`")
			assert.Contains(t, got, "Don't make a branch of your own")
			assert.Contains(t, got, "from `origin/<base>`")
			assert.NotContains(t, got, "git switch -c <branch> <base_commit>")
		})
	}
}
