package codex

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-bot/internal/harness"
)

// userConfig is a config.toml as a user and Codex leave it: settings,
// comments, the user's own trusted checkout, and two worktrees an attach
// trusted (Codex's own spelling, from the TUI spike).
const userConfig = `# my settings
model = "gpt-6.2-sol"

[tui]
screen_reader_detection_done = true

[projects."/home/u/src/mine"]
trust_level = "trusted"

[projects."/home/u/.local/share/slop-bot/worktrees/t1"]
trust_level = "trusted"

# MCP servers I use
[mcp_servers.docs]
command = "docs-mcp"

[projects."/home/u/.local/share/slop-bot/worktrees/t2"]
trust_level = "trusted"
`

func writeConfig(t *testing.T, content string) *TrustFile {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	return &TrustFile{Path: path}
}

func readConfig(t *testing.T, f *TrustFile) string {
	t.Helper()
	b, err := os.ReadFile(f.Path)
	require.NoError(t, err)
	return string(b)
}

func TestForgetRemovesOnlyTheNamedTrustEntries(t *testing.T) {
	t.Parallel()
	f := writeConfig(t, userConfig)
	removed, err := f.Forget("/home/u/.local/share/slop-bot/worktrees/t1", "/home/u/.local/share/slop-bot/worktrees/t2", "/never/trusted")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"/home/u/.local/share/slop-bot/worktrees/t1", "/home/u/.local/share/slop-bot/worktrees/t2"}, removed)
	// Everything else byte for byte, the comment above the next table
	// included.
	assert.Equal(t, `# my settings
model = "gpt-6.2-sol"

[tui]
screen_reader_detection_done = true

[projects."/home/u/src/mine"]
trust_level = "trusted"

# MCP servers I use
[mcp_servers.docs]
command = "docs-mcp"

`, readConfig(t, f))
}

func TestForgetKeepsCommentsAfterARemovedTable(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct{ in, want string }{
		"notes at the end": {
			"[projects.\"/ws\"]\ntrust_level = \"trusted\"\n\n# my notes\n",
			"# my notes\n",
		},
		"a commented-out table": {
			"[projects.\"/ws\"]\ntrust_level = \"trusted\"\n\n# [profiles.old]\n# model = \"o3\"\n\n[tui]\nx = 1\n",
			"# [profiles.old]\n# model = \"o3\"\n\n[tui]\nx = 1\n",
		},
		"a comment inside the table": {
			"[projects.\"/ws\"]\n# trusted by attach\ntrust_level = \"trusted\"\n[tui]\n",
			"[tui]\n",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := writeConfig(t, tc.in)
			_, err := f.Forget("/ws")
			require.NoError(t, err)
			assert.Equal(t, tc.want, readConfig(t, f))
		})
	}
}

func TestForgetEditsASymlinkedConfigInPlace(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	target := filepath.Join(dir, "dotfiles-config.toml")
	require.NoError(t, os.WriteFile(target, []byte(userConfig), 0o600))
	link := filepath.Join(dir, "config.toml")
	require.NoError(t, os.Symlink(target, link))
	_, err := (&TrustFile{Path: link}).Forget("/home/u/.local/share/slop-bot/worktrees/t1")
	require.NoError(t, err)
	info, err := os.Lstat(link)
	require.NoError(t, err)
	assert.NotZero(t, info.Mode()&os.ModeSymlink, "still a link")
	b, err := os.ReadFile(target) // #nosec G304 -- the test's own temp file
	require.NoError(t, err)
	assert.NotContains(t, string(b), "worktrees/t1", "the link's target was edited")
}

func TestForgetKeepsTheFileMode(t *testing.T) {
	t.Parallel()
	f := writeConfig(t, userConfig)
	require.NoError(t, os.Chmod(f.Path, 0o640))
	_, err := f.Forget("/home/u/.local/share/slop-bot/worktrees/t1")
	require.NoError(t, err)
	info, err := os.Stat(f.Path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o640), info.Mode().Perm())
}

func TestForgetUnderstandsQuotedKeys(t *testing.T) {
	t.Parallel()
	f := writeConfig(t, "[projects.'/ws/lit']\ntrust_level = \"trusted\"\n[projects.\"/ws/with \\\"quote\\\"\"]\ntrust_level = \"trusted\"\n[projects.bare]\ntrust_level = \"trusted\"\n")
	removed, err := f.Forget("/ws/lit", `/ws/with "quote"`, "bare")
	require.NoError(t, err)
	assert.Len(t, removed, 3)
	assert.Empty(t, readConfig(t, f))
}

func TestForgetWithNothingToForgetLeavesTheFileAlone(t *testing.T) {
	t.Parallel()
	f := writeConfig(t, userConfig)
	before, err := os.Stat(f.Path)
	require.NoError(t, err)
	removed, err := f.Forget("/not/there")
	require.NoError(t, err)
	assert.Empty(t, removed)
	after, err := os.Stat(f.Path)
	require.NoError(t, err)
	assert.Equal(t, before.ModTime(), after.ModTime())
	assert.Equal(t, userConfig, readConfig(t, f))
}

func TestForgetWithoutAConfigIsNothing(t *testing.T) {
	t.Parallel()
	removed, err := (&TrustFile{Path: filepath.Join(t.TempDir(), "config.toml")}).Forget("/ws")
	require.NoError(t, err)
	assert.Empty(t, removed)
}

func TestForgetRefusesAnEditThatWouldChangeMore(t *testing.T) {
	t.Parallel()
	// A nested array line starts with "[", which ends the table early, so
	// the cut would leave half of it behind. The parse check catches that
	// and the file is left as it was.
	content := "[projects.\"/ws\"]\ntrust_level = \"trusted\"\nnested = [\n[1, 2],\n]\n"
	f := writeConfig(t, content)
	_, err := f.Forget("/ws")
	assert.ErrorContains(t, err, "leaving")
	assert.Equal(t, content, readConfig(t, f))
}

func TestForgetRetriesWhenCodexWritesMeanwhile(t *testing.T) {
	t.Parallel()
	f := writeConfig(t, userConfig)
	writes := 0
	f.beforeCheck = func() {
		if writes == 0 {
			// Codex saves a setting between Forget's read and its rename.
			writes++
			require.NoError(t, os.WriteFile(f.Path, []byte(userConfig+"\n[notice]\nseen = true\n"), 0o600))
		}
	}
	removed, err := f.Forget("/home/u/.local/share/slop-bot/worktrees/t1")
	require.NoError(t, err)
	assert.Len(t, removed, 1)
	got := readConfig(t, f)
	assert.Contains(t, got, "[notice]", "Codex's write survives")
	assert.NotContains(t, got, "worktrees/t1")
}

func TestForgetGivesUpWhenTheFileKeepsChanging(t *testing.T) {
	t.Parallel()
	f := writeConfig(t, userConfig)
	n := 0
	f.beforeCheck = func() {
		n++
		require.NoError(t, os.WriteFile(f.Path, []byte(userConfig+"\n# "+string(rune('a'+n))+"\n"), 0o600))
	}
	_, err := f.Forget("/home/u/.local/share/slop-bot/worktrees/t1")
	assert.ErrorContains(t, err, "kept changing")
	assert.Contains(t, readConfig(t, f), "worktrees/t1", "never half-applied")
}

func TestConfigPath(t *testing.T) {
	t.Parallel()
	p, err := ConfigPath([]string{"PATH=/bin", "CODEX_HOME=/srv/codex"})
	require.NoError(t, err)
	assert.Equal(t, "/srv/codex/config.toml", p)
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	p, err = ConfigPath([]string{"PATH=/bin"})
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(home, ".codex", "config.toml"), p)
	p, err = ConfigPath([]string{"HOME=/home/agent"})
	require.NoError(t, err)
	assert.Equal(t, "/home/agent/.codex/config.toml", p, "the environment's HOME, not the daemon's")
}

func TestForgetWorkspaceUsesTheAgentsCodexHome(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	path := filepath.Join(home, "config.toml")
	require.NoError(t, os.WriteFile(path, []byte(userConfig), 0o600))
	// The agent's env names the home, over the daemon's.
	h := &Harness{Environ: func() []string { return []string{"CODEX_HOME=/elsewhere"} }}
	require.NoError(t, h.ForgetWorkspace(&harness.Workspace{
		Env: map[string]string{"CODEX_HOME": home},
		Dir: "/home/u/.local/share/slop-bot/worktrees/t1", ExtraDirs: []string{"/home/u/.local/share/slop-bot/worktrees/t2"},
	}))
	b, err := os.ReadFile(path) // #nosec G304 -- the test's own temp file
	require.NoError(t, err)
	assert.NotContains(t, string(b), "worktrees/")
	assert.Contains(t, string(b), `[projects."/home/u/src/mine"]`)
}
