package workspace_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/workspace"
)

// runScript writes the generated script into a directory and runs it
// there, which is exactly what a human does with it. Nothing here reads
// the script and reasons about it: a shell script that is only inspected
// is a guess, and this one is the entire setup path for anybody without a
// slop-haus checkout.
func runScript(t *testing.T, dir string, p workspace.Params) {
	t.Helper()

	script, err := workspace.Script(p)
	require.NoError(t, err)

	path := filepath.Join(dir, "setup.sh")
	require.NoError(t, os.WriteFile(path, []byte(script), 0o755))

	cmd := exec.Command("bash", path)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "script failed:\n%s", out)
}

// The claim the whole design rests on: the script builds the same
// workspace Files describes. This checks that the heredocs carry it across
// faithfully -- quoting, trailing newlines, modes and all.
func TestScript_ProducesTheSameFilesAsTheCommand(t *testing.T) {
	params := workspace.Params{
		AgentName: "builder-1",
		Server:    "https://slop-haus.example",
	}

	byScript := t.TempDir()
	runScript(t, byScript, params)

	byCommand := t.TempDir()
	install(t, byCommand, params)

	// Every file, not a hand-kept list: a list fell behind when files were
	// added, and a file missing from it is one the script could carry wrong.
	// These two name scripts by absolute path, so each points at its own
	// workspace's copy.
	namesPaths := map[string]bool{workspace.Settings: true, workspace.MCPConfig: true}
	for _, rel := range workspace.Paths() {
		if namesPaths[rel] {
			continue // compared below; they differ by the workspace path
		}
		assert.Equal(t, fileIn(t, byCommand, rel), fileIn(t, byScript, rel),
			"%s differs between the script and the command", rel)
		assert.Equal(t, modeIn(t, byCommand, rel), modeIn(t, byScript, rel),
			"%s has a different mode from the script and the command", rel)
	}

	assert.Contains(t, fileIn(t, byScript, workspace.Settings), workspace.HookPathIn(byScript),
		"the hook path is resolved to where the script actually ran")
	assert.Contains(t, fileIn(t, byScript, workspace.Settings), workspace.UsageHookPathIn(byScript),
		"so is the usage hook's")
	assert.Contains(t, fileIn(t, byScript, workspace.MCPConfig), workspace.TokenPathIn(byScript),
		"the token script path is resolved to where the script actually ran")
	for rel := range namesPaths {
		scripted := fileIn(t, byScript, rel)
		for _, placeholder := range []string{
			workspace.HookCommandPlaceholder, workspace.SessionStartHookCommandPlaceholder,
			workspace.UsageHookCommandPlaceholder, workspace.UserPromptHookCommandPlaceholder, workspace.TokenCommandPlaceholder,
		} {
			assert.NotContains(t, scripted, placeholder, "%s: the placeholder must not survive into the workspace", rel)
		}
		assert.Equal(t,
			strings.ReplaceAll(fileIn(t, byCommand, rel), byCommand, "WORKSPACE"),
			strings.ReplaceAll(scripted, byScript, "WORKSPACE"),
			"%s should differ only by the workspace path", rel)
	}
}

// The script's own substitution, not Files', is what a real workspace
// gets -- so the helper has to work from a script-built one too, with the
// sed-hostile `|` in its path.
func TestScript_MCPHeadersHelperWorksFromAPipeInTheWorkspacePath(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "a|b c")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	runScript(t, dir, workspace.Params{AgentName: "builder-1", Server: "https://slop-haus.example"})
	seedValidCredentials(t, dir, "agent-token")

	assert.Equal(t, map[string]string{"Authorization": "Bearer agent-token"}, runHeadersHelper(t, dir))
}

// Settings and .mcp.json used to carry $SLOP_HAUS_TOKEN and
// ${SLOP_HAUS_TOKEN} for the agent's environment to fill in, and the
// heredoc writing them had to stay quoted so the shell never expanded
// those while writing. There is no such placeholder left to expand -- ­the
// access token comes from token.sh, not the environment -- but the same
// heredoc still has to survive a name or agreement text containing a
// literal `$`, which this pins down by checking nothing this workspace
// writes leaks a stray expansion.
func TestScript_DoesNotExpandTheHeredocsWriting(t *testing.T) {
	dir := t.TempDir()
	// A literal $ followed by something that looks like an expansion --
	// the case that actually exercises the quoted heredoc. An unquoted one,
	// or a broken escape in it, would have the shell running this script
	// expand these to something else (usually empty) while writing the
	// briefing, rather than leaving the agent name as it was given.
	agentName := `builder-$(id)-$SLOP_HAUS_TOKEN-1`
	runScript(t, dir, workspace.Params{
		AgentName: agentName, Server: "https://slop-haus.example",
	})

	assert.Contains(t, fileIn(t, dir, workspace.HeadlessBriefing), agentName,
		"a literal $ in a templated file must survive the heredoc that writes it, unexpanded")
	assert.NotContains(t, fileIn(t, dir, workspace.MCPConfig), "SLOP_HAUS_TOKEN",
		"the old token placeholder is dead; nothing should still mention it")
	assert.NotContains(t, fileIn(t, dir, workspace.Settings), "SLOP_HAUS_TOKEN")
}

func TestScript_InstallsAnExecutableHook(t *testing.T) {
	dir := t.TempDir()
	runScript(t, dir, workspace.Params{
		AgentName: "builder-1", Server: "https://slop-haus.example",
	})

	info, err := os.Stat(workspace.HookPathIn(dir))
	require.NoError(t, err)
	assert.NotZero(t, info.Mode().Perm()&0o100,
		"without +x every Stop fails open, which looks like an idle agent")

	tokenScript, err := os.Stat(filepath.Join(dir, workspace.TokenScript))
	require.NoError(t, err)
	assert.NotZero(t, tokenScript.Mode().Perm()&0o100,
		"token.sh is run directly by the other scripts, and by the human at setup")
}

// settings.json's hook paths are substituted into the heredoc with bash's
// ${var//search/replace}, not sed -- sed's replacement text treats \, &
// and its own delimiter specially, so a workspace path containing any of
// them would have corrupted the substitution (or, worse, silently written
// the wrong command) if this were still sed-based. A directory with a `|`
// in it is the sed delimiter this file uses, so it is the sharpest case.
func TestScript_HookCommandSurvivesAPipeInTheWorkspacePath(t *testing.T) {
	blocked := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"hookSpecificOutput":{"decision":"block","reason":"still yours"}}`)
	}))
	defer blocked.Close()

	dir := filepath.Join(t.TempDir(), "a|b")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.Contains(t, dir, "|", "the point of this test is a path with a pipe in it")

	runScript(t, dir, workspace.Params{AgentName: "builder-1", Server: blocked.URL})
	seedValidCredentials(t, dir, "agent-token")

	var settings map[string]any
	require.NoError(t, json.Unmarshal([]byte(fileIn(t, dir, workspace.Settings)), &settings))
	hooks := settings["hooks"].(map[string]any)
	command := hooks["Stop"].([]any)[0].(map[string]any)["hooks"].([]any)[0].(map[string]any)["command"].(string)
	assert.Contains(t, command, workspace.HookPathIn(dir))
	assert.NotContains(t, command, workspace.HookCommandPlaceholder)

	cmd := exec.Command("sh", "-c", command)
	cmd.Stdin = strings.NewReader("{}")
	cmd.Env = os.Environ()
	var stderr stringWriter
	cmd.Stderr = &stderr
	cmd.Stdout = io.Discard

	assert.Equal(t, 2, exitCodeOf(t, cmd.Run()),
		"the hook has to run, and block, from a path with a pipe in it")
	assert.Contains(t, stderr.text, "still yours")
}

// An agent name is not ours -- the server accepts any non-empty string --
// and it reaches a shell here.
//
// Both payloads, because the first version of this test used only the
// quote one and passed against a generator that ran the second: Go's %q
// escapes quotes and backslashes, but not $, so a command substitution
// walked straight through it.
func TestScript_DoesNotLetAnAgentNameEscapeIntoTheShell(t *testing.T) {
	for _, payload := range []string{
		`'; touch CANARY; echo '`,
		`$(touch CANARY)`,
		"`touch CANARY`",
		`"; touch CANARY; echo "`,
	} {
		t.Run(payload, func(t *testing.T) {
			dir := t.TempDir()
			canary := filepath.Join(t.TempDir(), "canary")

			runScript(t, dir, workspace.Params{
				AgentName: strings.ReplaceAll(payload, "CANARY", canary),
				Server:    "https://slop-haus.example",
			})

			_, err := os.Stat(canary)
			assert.True(t, os.IsNotExist(err), "the name was executed instead of quoted")
		})
	}
}

// A newline cannot be quoted out of trouble: it ends the comment the name
// appears in, and could close a heredoc early. Refused rather than
// mangled.
func TestScript_RefusesANameWithALineBreak(t *testing.T) {
	_, err := workspace.Script(workspace.Params{
		AgentName: "builder-1\nrm -rf /",
		Server:    "https://slop-haus.example",
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "line break")
}

// The script is no longer a credential -- there is nothing in it worth
// stealing -- and it says so, since anyone who read the old version's
// warning should not be left thinking this one still applies.
func TestScript_SaysItCarriesNoCredential(t *testing.T) {
	script, err := workspace.Script(workspace.Params{
		AgentName: "builder-1", Server: "https://slop-haus.example",
	})
	require.NoError(t, err)

	// "Bearer" legitimately appears in the embedded hook scripts' own curl
	// calls; what must not survive is the old claim that this file itself
	// holds one.
	assert.NotContains(t, strings.ToUpper(script), "THIS FILE CONTAINS A BEARER TOKEN")
	assert.Contains(t, script, "no credential",
		"a reader should not have to guess whether this is still secret")
}

// The whole reason this script has to run token.sh login at all: an agent
// has no static credential to embed any more, so signing in has to happen
// somewhere, and the setup script is the one place that already knows the
// agent's name.
func TestScript_SignsTheWorkspaceInOnceForHooksAndMCPAlike(t *testing.T) {
	script, err := workspace.Script(workspace.Params{
		AgentName: "builder-1", Server: "https://slop-haus.example",
	})
	require.NoError(t, err)

	assert.Contains(t, script, workspace.TokenScript+" login")
	assert.NotContains(t, script, "claude mcp login",
		".mcp.json's headersHelper reuses this sign-in; a second, browser-only one is what it replaced")
	assert.Contains(t, script, "Workers run under the supervisor")
	assert.NotContains(t, script, "Start the agent with")
}

// The instruction text alone proves nothing: a setup script that dropped
// the token.sh login call entirely, while keeping the later "run this
// yourself" instructions that mention the same words, would still pass
// TestScript_SignsTheWorkspaceInOnceForHooksAndMCPAlike above. This runs
// the actual generated script against a real (fake) OAuth server and
// checks the one thing that matters -- that it walks away with working
// credentials, not just a mention of how to get them.
func TestScript_ActuallyRunsTokenShLoginAndEndsUpWithCredentials(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /oauth/device_authorization", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"device_code": "dev-code-1", "user_code": "ABCD-EFGH",
			"verification_uri":          "http://example.invalid/device",
			"verification_uri_complete": "http://example.invalid/device?code=ABCD-EFGH",
			"expires_in":                600, "interval": 0,
		})
	})
	mux.HandleFunc("POST /oauth/token", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "AT-real", "refresh_token": "RT-real",
			"token_type": "Bearer", "expires_in": 900,
		})
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	dir := t.TempDir()
	runScriptCapturingOutput := func() string {
		script, err := workspace.Script(workspace.Params{AgentName: "builder-1", Server: server.URL})
		require.NoError(t, err)
		path := filepath.Join(dir, "setup.sh")
		require.NoError(t, os.WriteFile(path, []byte(script), 0o755))
		cmd := exec.Command("bash", path)
		cmd.Dir = dir
		// A shell left pointing at some other slop-haus -- another
		// environment's workspace set up earlier, say. The sign-in must go
		// to the server this script was made for, not follow the shell.
		cmd.Env = append(os.Environ(), "SLOP_HAUS_URL=http://127.0.0.1:1")
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "script failed:\n%s", out)
		return string(out)
	}
	out := runScriptCapturingOutput()

	assert.NotContains(t, out, "Sign-in did not finish",
		"token.sh login must actually have succeeded, not merely be named in the instructions")
	assert.Contains(t, out, "Authorized.")

	creds := filepath.Join(dir, ".slop-haus", "credentials.json")
	body, err := os.ReadFile(creds)
	require.NoError(t, err, "the setup script must run token.sh login and end up with stored credentials")
	assert.Contains(t, string(body), "AT-real")
	info, err := os.Stat(creds)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

// Generated shell has to parse as shell. An apostrophe in a placeholder
// has slipped past substring assertions on this project before.
func TestScript_ParsesAsShell(t *testing.T) {
	script, err := workspace.Script(workspace.Params{
		AgentName: "builder-1", Server: "https://slop-haus.example",
	})
	require.NoError(t, err)

	cmd := exec.Command("bash", "-n")
	cmd.Stdin = strings.NewReader(script)
	out, err := cmd.CombinedOutput()
	assert.NoError(t, err, "bash -n: %s", out)
}

func modeIn(t *testing.T, dir, rel string) os.FileMode {
	t.Helper()
	info, err := os.Stat(filepath.Join(dir, filepath.FromSlash(rel)))
	require.NoError(t, err)
	return info.Mode().Perm()
}
