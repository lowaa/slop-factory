package bench

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// runGit runs git in dir with a fixed throwaway identity (mirroring
// repoBuilder's own env, and gitctx's test helper), requiring success.
func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.invalid",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.invalid",
	)
	out, err := cmd.CombinedOutput()
	require.NoErrorf(t, err, "git %v: %s", args, out)
	return strings.TrimSpace(string(out))
}

// tryGit runs git in dir and returns its combined output and error, without
// asserting success -- for checks that are expected to fail.
func tryGit(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.invalid",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.invalid",
	)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func writeRepoFile(t *testing.T, dir, relPath, content string) {
	t.Helper()
	full := filepath.Join(dir, relPath)
	require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
	require.NoError(t, os.WriteFile(full, []byte(content), 0o644))
}

// buildSourceRepo builds a small *source* repo with a base commit and a
// head commit on top of it, mirroring what BuildRepo expects to extract
// trees from. It returns the repo's dir and the two commit hashes.
func buildSourceRepo(t *testing.T) (dir, baseHash, headHash string) {
	t.Helper()
	dir = t.TempDir()
	runGit(t, dir, "init", "--quiet", "--initial-branch=main")

	writeRepoFile(t, dir, "a.txt", "a\n")
	writeRepoFile(t, dir, "gone.txt", "gone\n")
	writeRepoFile(t, dir, "dir/b.txt", "b\n")
	runGit(t, dir, "add", "--all")
	runGit(t, dir, "commit", "--quiet", "-m", "base")
	baseHash = runGit(t, dir, "rev-parse", "HEAD")

	writeRepoFile(t, dir, "a.txt", "a modified\n")
	require.NoError(t, os.Remove(filepath.Join(dir, "gone.txt")))
	writeRepoFile(t, dir, "new.txt", "new\n")
	runGit(t, dir, "add", "--all")
	runGit(t, dir, "commit", "--quiet", "-m", "head")
	headHash = runGit(t, dir, "rev-parse", "HEAD")

	return dir, baseHash, headHash
}

// buildPatchForNewTxt generates a unified diff (via a real `git diff`,
// rather than a hand-written patch) that changes new.txt's content in
// sourceDir's current worktree (which must be checked out at head), then
// reverts that change so the source repo is left as it was.
func buildPatchForNewTxt(t *testing.T, sourceDir string) string {
	t.Helper()
	writeRepoFile(t, sourceDir, "new.txt", "new\nsecond line\n")
	patch := runGit(t, sourceDir, "diff", "--", "new.txt")
	require.NotEmpty(t, patch, "expected a non-empty diff for the modified new.txt")
	runGit(t, sourceDir, "checkout", "--quiet", "--", "new.txt")
	return patch + "\n"
}

func TestBuildRepo(t *testing.T) {
	sourceDir, baseHash, headHash := buildSourceRepo(t)
	patch := buildPatchForNewTxt(t, sourceDir)

	caseDir := t.TempDir()
	writeRepoFile(t, caseDir, "bug.patch", patch)

	c := &Case{
		Name:          "the-case",
		Base:          baseHash,
		Head:          headHash,
		Patch:         "bug.patch",
		CommitMessage: "the change",
	}
	c.dir = caseDir

	destDir := t.TempDir()
	repo, err := BuildRepo(sourceDir, c, destDir)
	require.NoError(t, err)

	// Exactly 2 commits reachable from feature: base, then head+patch.
	count := runGit(t, repo, "rev-list", "--count", "feature")
	assert.Equal(t, "2", count)

	// main holds the base tree.
	assert.Equal(t, "a\n", runGit(t, repo, "show", "main:a.txt")+"\n")
	assert.Equal(t, "gone\n", runGit(t, repo, "show", "main:gone.txt")+"\n")
	assert.Equal(t, "b\n", runGit(t, repo, "show", "main:dir/b.txt")+"\n")

	// feature is the checked out branch.
	branch := runGit(t, repo, "rev-parse", "--abbrev-ref", "HEAD")
	assert.Equal(t, "feature", branch)

	// gone.txt is gone, both from the worktree and from feature's tree.
	_, statErr := os.Stat(filepath.Join(repo, "gone.txt"))
	assert.True(t, os.IsNotExist(statErr), "gone.txt should not exist in the worktree")
	_, catErr := tryGit(repo, "cat-file", "-e", "feature:gone.txt")
	assert.Error(t, catErr, "gone.txt should not exist in feature's tree")

	// The patch's content is present and committed -- a clean status
	// means both the patch applied and `git commit` picked it up.
	status := runGit(t, repo, "status", "--porcelain")
	assert.Empty(t, status)
	newContent, err := os.ReadFile(filepath.Join(repo, "new.txt"))
	require.NoError(t, err)
	assert.Equal(t, "new\nsecond line\n", string(newContent))

	// The feature commit carries the case's message.
	subject := runGit(t, repo, "log", "-1", "--format=%s", "feature")
	assert.Equal(t, "the change", subject)

	// The point of building a fresh repo: the source repo's history is
	// not reachable from it.
	_, headErr := tryGit(repo, "cat-file", "-e", headHash)
	assert.Error(t, headErr, "source repo's head commit must not be reachable in the built repo")
	_, baseErr := tryGit(repo, "cat-file", "-e", baseHash)
	assert.Error(t, baseErr, "source repo's base commit must not be reachable in the built repo")
}

func TestBuildRepo_PatchThatDoesNotApplyErrors(t *testing.T) {
	sourceDir, baseHash, headHash := buildSourceRepo(t)

	// A patch whose context can never match new.txt's actual content
	// ("new\n"), so git apply must reject it.
	badPatch := `diff --git a/new.txt b/new.txt
index 0000000..1111111 100644
--- a/new.txt
+++ b/new.txt
@@ -1,1 +1,2 @@
-this line does not exist in new.txt
+replacement line
+second line
`
	caseDir := t.TempDir()
	writeRepoFile(t, caseDir, "bug.patch", badPatch)

	c := &Case{
		Name:          "the-case",
		Base:          baseHash,
		Head:          headHash,
		Patch:         "bug.patch",
		CommitMessage: "the change",
	}
	c.dir = caseDir

	_, err := BuildRepo(sourceDir, c, t.TempDir())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "git apply")
}

// A reused --work-dir must not quietly build on a previous run's repo.
func TestBuildRepo_RefusesAnExistingRepo(t *testing.T) {
	sourceDir, baseHash, headHash := buildSourceRepo(t)
	c := &Case{Name: "the-case", Base: baseHash, Head: headHash}
	destDir := t.TempDir()

	_, err := BuildRepo(sourceDir, c, destDir)
	require.NoError(t, err)

	_, err = BuildRepo(sourceDir, c, destDir)
	assert.ErrorContains(t, err, "already exists")
}
