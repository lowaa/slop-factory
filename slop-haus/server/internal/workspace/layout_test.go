package workspace_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The workspace tells the server what is on disk so the Workspace screen
// can show it. These lay a workspace out for real and run the installed
// hook against a server that captures what arrives.

func gitRepoWithOrigin(t *testing.T, dir, origin string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(dir, 0o755))
	for _, args := range [][]string{{"init", "-q"}, {"remote", "add", "origin", origin}} {
		if origin == "" && args[0] == "remote" {
			continue
		}
		out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
		require.NoError(t, err, string(out))
	}
}

func reportedLayout(t *testing.T, dir string) map[string]any {
	t.Helper()
	server, got := captureStop(t)
	run := runHookInDir(t, dir, server.URL, "agent-token", `{"session_id":"s1"}`)
	require.Equal(t, 0, run.exitCode, run.stderr)
	described, _ := (<-got)["slop_haus"].(map[string]any)
	layout, _ := described["layout"].(map[string]any)
	require.NotNil(t, layout, "every payload carries the layout")
	return layout
}

func TestLayout_ReportsTheRootAndItsTopLevelRepos(t *testing.T) {
	dir := t.TempDir()
	gitRepoWithOrigin(t, dir, "git@github.com:acme/root.git")
	gitRepoWithOrigin(t, filepath.Join(dir, "app"), "https://github.com/acme/app")
	gitRepoWithOrigin(t, filepath.Join(dir, "deploy"), "")
	gitRepoWithOrigin(t, filepath.Join(dir, "app", "vendor", "nested"), "https://github.com/acme/nested")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "notes"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "secret-plans.txt"), []byte("x"), 0o600))
	gitRepoWithOrigin(t, filepath.Join(dir, ".hidden"), "https://github.com/acme/hidden")

	layout := reportedLayout(t, dir)

	assert.Equal(t, map[string]any{"path": ".", "git": true, "origin": "github.com:acme/root.git"}, layout["root"])
	assert.Equal(t, []any{
		map[string]any{"path": "app", "git": true, "origin": "github.com/acme/app"},
		map[string]any{"path": "deploy", "git": true, "origin": ""},
	}, layout["repos"], "only top-level git repos: not plain directories, files, hidden or nested ones")
}

func TestLayout_ARootThatIsNotARepoSaysSo(t *testing.T) {
	layout := reportedLayout(t, t.TempDir())
	assert.Equal(t, map[string]any{"path": ".", "git": false, "origin": ""}, layout["root"])
	assert.Equal(t, []any{}, layout["repos"])
}

// A token in a remote URL is common, and must not reach the server's
// database whatever shape it takes.
func TestLayout_NoCredentialInARemoteLeavesTheMachine(t *testing.T) {
	for remote, want := range map[string]string{
		"https://user:ghp_secret@github.com/acme/app.git":   "github.com/acme/app.git",
		"https://ghp_secret@github.com/acme/app":            "github.com/acme/app",
		"https://user:p@ss@github.com/acme/app":             "github.com/acme/app",
		"https://user:pa/ss@github.com/acme/app":            "github.com/acme/app",
		"ssh://git:secret@github.com:22/acme/app":           "github.com:22/acme/app",
		"https://github.com/acme/app?token=ghp_secret#frag": "github.com/acme/app",
		"ghp_secret@github.com:acme/app.git":                "github.com:acme/app.git",
		"https://github.com/acme/app":                       "github.com/acme/app",
	} {
		dir := t.TempDir()
		gitRepoWithOrigin(t, dir, remote)
		gitRepoWithOrigin(t, filepath.Join(dir, "app"), remote)
		layout := reportedLayout(t, dir)
		root, _ := layout["root"].(map[string]any)
		assert.Equal(t, want, root["origin"], remote)
		repos, _ := layout["repos"].([]any)
		require.Len(t, repos, 1)
		repo, _ := repos[0].(map[string]any)
		assert.Equal(t, want, repo["origin"], "a top-level repo's remote too: "+remote)
	}
}

// Following a symlink would report, and run git in, whatever it points at.
func TestLayout_ASymlinkedDirectoryIsNotFollowed(t *testing.T) {
	dir := t.TempDir()
	elsewhere := t.TempDir()
	gitRepoWithOrigin(t, elsewhere, "https://github.com/acme/elsewhere")
	require.NoError(t, os.Symlink(elsewhere, filepath.Join(dir, "linked")))

	assert.Equal(t, []any{}, reportedLayout(t, dir)["repos"])
}

func TestLayout_AtMostFiftyReposAreReported(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 55; i++ {
		gitRepoWithOrigin(t, filepath.Join(dir, "r"+string(rune('a'+i/26))+string(rune('a'+i%26))), "")
	}
	repos, _ := reportedLayout(t, dir)["repos"].([]any)
	assert.Len(t, repos, 50)
}

// No report is better than a wrong one: when git cannot be asked, the
// payload carries no layout at all, so the server keeps what it had rather
// than recording a repo as having no origin.
func TestLayout_WhenGitCannotAnswerNoLayoutIsSent(t *testing.T) {
	bin := t.TempDir()
	for _, tool := range []string{
		"bash", "curl", "grep", "sed", "head", "cat", "mktemp", "rm", "date", "dirname", "mv", "chmod", "mkdir", "python3",
	} {
		real, err := exec.LookPath(tool)
		require.NoError(t, err)
		require.NoError(t, os.Symlink(real, filepath.Join(bin, tool)))
	}
	dir := t.TempDir()
	gitRepoWithOrigin(t, dir, "https://github.com/acme/root")

	server, got := captureStop(t)
	run := runHookInDir(t, dir, server.URL, "agent-token", `{"session_id":"s1"}`, "PATH="+bin, "SLOP_HAUS_HARNESS=claude")
	require.Equal(t, 0, run.exitCode, run.stderr)

	described, _ := (<-got)["slop_haus"].(map[string]any)
	delete(described, "bundle_hash")
	assert.Equal(t, map[string]any{"harness": "claude"}, described)
}

// A remote a global or system git config supplies is not the repo's own.
func TestLayout_OnlyTheReposOwnConfigNamesItsOrigin(t *testing.T) {
	home := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(home, ".gitconfig"),
		[]byte("[remote \"origin\"]\n\turl = https://github.com/acme/from-global\n"), 0o600))
	dir := t.TempDir()
	gitRepoWithOrigin(t, dir, "")

	server, got := captureStop(t)
	run := runHookInDir(t, dir, server.URL, "agent-token", `{"session_id":"s1"}`, "HOME="+home, "XDG_CONFIG_HOME="+home)
	require.Equal(t, 0, run.exitCode, run.stderr)

	described, _ := (<-got)["slop_haus"].(map[string]any)
	layout, _ := described["layout"].(map[string]any)
	assert.Equal(t, map[string]any{"path": ".", "git": true, "origin": ""}, layout["root"])
}
