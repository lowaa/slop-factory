// Package gitxtest gives each test a git whose config is its own, so tests
// against real git run in parallel and the user's ~/.gitconfig (signing,
// hooks, a default branch of their choosing) can't change what they see.
package gitxtest

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-bot/internal/gitx"
)

const globalConfig = `[init]
	defaultBranch = main
[advice]
	detachedHead = false
`

// Env is an environment with only PATH from the real one, a temp HOME, empty
// system and global git config, and a fixed author.
func Env(t testing.TB) []string {
	t.Helper()
	home := t.TempDir()
	global := filepath.Join(home, "gitconfig")
	system := filepath.Join(home, "gitconfig-system")
	require.NoError(t, os.WriteFile(global, []byte(globalConfig), 0o600))
	require.NoError(t, os.WriteFile(system, nil, 0o600))
	return []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + home,
		"XDG_CONFIG_HOME=" + filepath.Join(home, ".config"),
		"GIT_CONFIG_GLOBAL=" + global,
		"GIT_CONFIG_SYSTEM=" + system,
		"GIT_AUTHOR_NAME=slop-bot test",
		"GIT_AUTHOR_EMAIL=test@example.invalid",
		"GIT_COMMITTER_NAME=slop-bot test",
		"GIT_COMMITTER_EMAIL=test@example.invalid",
	}
}

// New is a *gitx.Git running with Env.
func New(t testing.TB) *gitx.Git {
	t.Helper()
	g, err := gitx.New(&gitx.Options{Bin: "git", Env: Env(t), Logger: slog.New(slog.DiscardHandler)})
	require.NoError(t, err)
	return g
}
