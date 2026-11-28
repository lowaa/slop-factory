package agytest_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-bot/internal/harness/agy/agytest"
)

func write(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
}

func TestForgetConversationRemovesOnlyThatOne(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	root := filepath.Join(home, ".gemini", "antigravity-cli")
	for _, id := range []string{"c1", "c2"} {
		write(t, filepath.Join(root, "brain", id, "x"), "x")
		write(t, filepath.Join(root, "conversations", id+".db"), "x")
		write(t, filepath.Join(root, "annotations", id+".pbtxt"), "x")
	}
	require.NoError(t, agytest.ForgetConversation(home, "c1"))
	assert.NoDirExists(t, filepath.Join(root, "brain", "c1"))
	assert.NoFileExists(t, filepath.Join(root, "conversations", "c1.db"))
	assert.NoFileExists(t, filepath.Join(root, "annotations", "c1.pbtxt"))
	assert.DirExists(t, filepath.Join(root, "brain", "c2"))
	assert.FileExists(t, filepath.Join(root, "conversations", "c2.db"))
	require.NoError(t, agytest.ForgetConversation(home, "gone"), "nothing to remove is fine")

	for _, id := range []string{"", ".", "..", "../brain", "a/b"} {
		require.Error(t, agytest.ForgetConversation(home, id), id)
	}
	assert.DirExists(t, filepath.Join(root, "brain", "c2"), "a blank id never names every conversation")
}

func TestDropHistoryKeepsOtherWorkspacesLines(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	path := filepath.Join(home, ".gemini", "antigravity-cli", "history.jsonl")
	require.NoError(t, agytest.DropHistory(home, "/w"), "no history yet")
	write(t, path, `{"workspace":"/w","text":"test's"}`+"\n"+`{"workspace":"/mine","text":"keep"}`+"\n"+"not json\n")
	require.NoError(t, agytest.DropHistory(home, "/w"))
	got, err := os.ReadFile(path) // #nosec G304 -- the test's own file
	require.NoError(t, err)
	assert.Equal(t, `{"workspace":"/mine","text":"keep"}`+"\n"+"not json\n", string(got))
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	require.NoError(t, agytest.DropHistory(home, "/other"), "nothing of its own: left as it is")
}
