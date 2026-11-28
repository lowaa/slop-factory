package setup

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-bot/internal/config"
)

func TestDefaultConfigLoadsAsWritten(t *testing.T) {
	t.Parallel()
	raw, err := DefaultConfig(&ConfigOptions{
		BriefingPath: "/home/ada/.config/slop-bot/briefing.md", Home: "/home/ada",
		Repos: []config.Repo{
			{Name: "app", Path: "/home/ada/src/app"},
			{Name: "odd-1", Path: "/srv/odd: dir #1/x"},
		},
	})
	require.NoError(t, err)
	assert.Contains(t, string(raw), "briefing_path: ~/.config/slop-bot/briefing.md\n")
	assert.Contains(t, string(raw), "    path: ~/src/app\n")

	c, err := config.Parse(raw, "/home/ada/.config/slop-bot", "/home/ada")
	require.NoError(t, err)
	assert.Equal(t, "/home/ada/.config/slop-bot/briefing.md", c.BriefingPath)
	assert.Equal(t, config.ClaudeCode, c.DefaultHarness)
	assert.Equal(t, []string{config.ClaudeCode}, keys(c.Harnesses))
	assert.Equal(t, "/home/ada/slop-bot/workspace", c.WorkspaceDir)
	require.Len(t, c.Repos, 2)
	assert.Equal(t, config.Repo{Name: "app", Path: "/home/ada/src/app"}, c.Repos[0])
	assert.Equal(t, config.Repo{Name: "odd-1", Path: "/srv/odd: dir #1/x"}, c.Repos[1], "YAML specials are quoted")
	assert.Empty(t, c.Doctor.Require)
}

func TestDefaultConfigWithoutReposLeavesAnExample(t *testing.T) {
	t.Parallel()
	raw, err := DefaultConfig(&ConfigOptions{BriefingPath: "/b/briefing.md", Harnesses: []string{config.Pi, config.Agy}})
	require.NoError(t, err)
	assert.Contains(t, string(raw), "repos: []\n#  - name: my-project\n")
	c, err := config.Parse(raw, "/b", "/h")
	require.NoError(t, err)
	assert.Equal(t, config.Pi, c.DefaultHarness)
	assert.ElementsMatch(t, []string{config.Pi, config.Agy}, keys(c.Harnesses))
	assert.Contains(t, string(raw), "  # claude-code: {}\n  # codex: {}\n")
}

func TestDefaultConfigRefusesWhatWouldNotLoad(t *testing.T) {
	t.Parallel()
	_, err := DefaultConfig(&ConfigOptions{BriefingPath: "/b/briefing.md", Harnesses: []string{"cursor"}})
	assert.ErrorContains(t, err, "unknown harness")
	_, err = DefaultConfig(&ConfigOptions{BriefingPath: "/b/briefing.md",
		Repos: []config.Repo{{Name: "a", Path: "/a"}, {Name: "a", Path: "/b"}}})
	assert.ErrorContains(t, err, "listed twice")
}

func TestWriteNewNeverOverwrites(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "sub", "f")
	wrote, err := WriteNew(path, []byte("first"))
	require.NoError(t, err)
	assert.True(t, wrote)
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	wrote, err = WriteNew(path, []byte("second"))
	require.NoError(t, err)
	assert.False(t, wrote)
	got, err := os.ReadFile(path) // #nosec G304 -- the test's temp file
	require.NoError(t, err)
	assert.Equal(t, "first", string(got))
}

func TestTilde(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "~/a/b", tilde("/home/u/a/b", "/home/u"))
	assert.Equal(t, "/home/user2/a", tilde("/home/user2/a", "/home/u"), "a sibling isn't under home")
	assert.Equal(t, "/home/u", tilde("/home/u", "/home/u"), "home itself stays absolute")
	assert.Equal(t, "/x", tilde("/x", ""))
}

func keys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
