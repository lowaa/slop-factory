package gitctx

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// newTestRepo builds a throwaway git repo with a main branch (one commit
// with a README) and a feature branch checked out on top with an additional
// commit, mirroring the shape slop-review expects: current branch != main,
// with a merge-base to diff against.
func newTestRepo(t *testing.T) string {
	t.Helper()
	// git reports paths with symlinks resolved, and macOS's temp dir is
	// under /var, a link to /private/var.
	dir, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)

	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com",
		)
		out, err := cmd.CombinedOutput()
		require.NoErrorf(t, err, "git %v: %s", args, out)
		return string(out)
	}

	run("init", "--initial-branch=main")
	writeFile(t, dir, "README.md", "hello\n")
	run("add", "README.md")
	run("commit", "-m", "initial commit")

	run("checkout", "-b", "feature")
	writeFile(t, dir, "feature.go", "package main\n\nfunc main() {}\n")
	run("add", "feature.go")
	run("commit", "-m", "add feature")

	return dir
}

func writeFile(t *testing.T, repoDir, relPath, content string) {
	t.Helper()
	full := filepath.Join(repoDir, relPath)
	require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
	require.NoError(t, os.WriteFile(full, []byte(content), 0o644))
}
