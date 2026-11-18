package gitctx

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveScope_FeatureBranchAgainstMain(t *testing.T) {
	dir := newTestRepo(t)

	scope, err := ResolveScope(dir, ScopeOptions{})
	require.NoError(t, err)

	assert.Equal(t, "feature", scope.Branch)
	assert.Contains(t, scope.Diff(), "feature.go")
	assert.Contains(t, scope.Diff(), "func main()")
}

func TestResolveScope_IncludesUntrackedFiles(t *testing.T) {
	dir := newTestRepo(t)
	writeFile(t, dir, "new_file.go", "package main\n\n// brand new, never git added\n")

	scope, err := ResolveScope(dir, ScopeOptions{})
	require.NoError(t, err)

	assert.Contains(t, scope.Diff(), "new_file.go")
	assert.Contains(t, scope.Diff(), "brand new, never git added")
	assert.Contains(t, scope.Diff(), "new file mode")
}

func TestResolveScope_IncludesWorkingTreeChanges(t *testing.T) {
	dir := newTestRepo(t)
	writeFile(t, dir, "feature.go", "package main\n\nfunc main() { println(\"wip\") }\n")

	scope, err := ResolveScope(dir, ScopeOptions{})
	require.NoError(t, err)

	assert.Contains(t, scope.Diff(), "wip", "uncommitted working tree edits must be included, not just committed diffs")
}

func TestResolveScope_RedactsSecretShapedFileByDefault(t *testing.T) {
	dir := newTestRepo(t)
	writeFile(t, dir, ".env", "API_KEY=top-secret-value\n")

	scope, err := ResolveScope(dir, ScopeOptions{})
	require.NoError(t, err)

	assert.Contains(t, scope.Diff(), ".env")
	assert.NotContains(t, scope.Diff(), "top-secret-value")
	assert.Contains(t, scope.Diff(), "omitted")
	assert.Empty(t, scope.Warnings)
	// The rest of the diff (an unrelated tracked change) must still come
	// through -- redaction is per-file, not all-or-nothing.
	assert.Contains(t, scope.Diff(), "feature.go")
}

func TestResolveScope_AllowSecretsIncludesContentAndWarns(t *testing.T) {
	dir := newTestRepo(t)
	writeFile(t, dir, ".env", "API_KEY=top-secret-value\n")

	scope, err := ResolveScope(dir, ScopeOptions{AllowSecrets: true})
	require.NoError(t, err)

	assert.Contains(t, scope.Diff(), "top-secret-value")
	require.Len(t, scope.Warnings, 1)
	assert.Contains(t, scope.Warnings[0], ".env")
}

func TestResolveScope_IncludesUntrackedFileWithQuoteWorthyPath(t *testing.T) {
	// A non-secret untracked file whose path needs git's C-quoting (see
	// TestResolveScope_RedactsSecretInGitQuotedPath) must still show up in
	// the diff at all, not silently vanish because `git ls-files` returns
	// it quoted and that quoted string doesn't exist as a literal
	// filesystem path.
	dir := newTestRepo(t)
	weirdDir := filepath.Join(dir, `weird\dir`)
	require.NoError(t, os.MkdirAll(weirdDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(weirdDir, "notes.txt"), []byte("ordinary-content\n"), 0o644))

	scope, err := ResolveScope(dir, ScopeOptions{})
	require.NoError(t, err)

	assert.Contains(t, scope.Diff(), "ordinary-content")
}

func TestResolveScope_RedactsSecretInGitQuotedPath(t *testing.T) {
	dir := newTestRepo(t)
	// A backslash anywhere in the path forces git to C-quote the whole
	// "a/<path>"/"b/<path>" token (not just the path portion after the
	// prefix) in its diff headers, regardless of core.quotepath -- set up
	// so the file itself is still exactly ".env" once unquoted.
	weirdDir := filepath.Join(dir, `weird\dir`)
	require.NoError(t, os.MkdirAll(weirdDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(weirdDir, ".env"), []byte("API_KEY=quoted-path-secret\n"), 0o644))

	// The file is untracked, so it only shows up via the same `git diff
	// --no-index` path untrackedFilesDiff itself uses (see diff.go) --
	// plain `git diff` never includes untracked files at all.
	rawDiff, rawErr := exec.Command("git", "-C", dir, "diff", "--no-index", "--", "/dev/null", `weird\dir/.env`).CombinedOutput()
	_ = rawErr // git diff --no-index exits 1 when it finds differences, which is expected here
	require.Contains(t, string(rawDiff), `"b/weird\\dir/.env"`, "test setup: git must actually quote this path, or it isn't exercising the bug")

	scope, err := ResolveScope(dir, ScopeOptions{})
	require.NoError(t, err)

	assert.NotContains(t, scope.Diff(), "quoted-path-secret")
	assert.Contains(t, scope.Diff(), "omitted")
}

func TestResolveScope_SplitsTheDiffPerFileWithLineCounts(t *testing.T) {
	dir := newTestRepo(t)
	writeFile(t, dir, "feature.go", "package main\n\nfunc main() { println(1) }\n\nfunc extra() {}\n")
	writeFile(t, dir, "pkg/new.go", "package pkg\n")

	scope, err := ResolveScope(dir, ScopeOptions{})
	require.NoError(t, err)

	byPath := filesByPath(scope.Files)
	require.Contains(t, byPath, "feature.go")
	require.Contains(t, byPath, "pkg/new.go")
	assert.Equal(t, 5, byPath["feature.go"].Added, "counted against main, where feature.go does not exist")
	assert.Equal(t, 0, byPath["feature.go"].Deleted)
	assert.Equal(t, 1, byPath["pkg/new.go"].Added)
	assert.True(t, strings.HasPrefix(byPath["pkg/new.go"].Block, "diff --git "))
	assert.Equal(t, scope.Diff(), byPath["feature.go"].Block+byPath["pkg/new.go"].Block)
}

func TestResolveScope_MarksLockfilesAndGeneratedFilesAsNoise(t *testing.T) {
	dir := t.TempDir()
	runGitCmd(t, dir, "init", "--initial-branch=main")
	writeFile(t, dir, "gen/types.go", "// Code generated by stringer; DO NOT EDIT.\n\npackage gen\n")
	writeFile(t, dir, "api/client.ts", "export const x = 1;\n")
	writeFile(t, dir, "real.go", "package main\n")
	writeFile(t, dir, ".gitattributes", "api/client.ts linguist-generated\n")
	runGitCmd(t, dir, "add", "--all")
	runGitCmd(t, dir, "commit", "-m", "base")
	runGitCmd(t, dir, "checkout", "-b", "feature")

	writeFile(t, dir, "go.sum", "example.com/x v1.0.0 h1:abc=\n")
	writeFile(t, dir, "gen/types.go", "// Code generated by stringer; DO NOT EDIT.\n\npackage gen\n\nconst Y = 2\n")
	writeFile(t, dir, "api/client.ts", "export const x = 2;\n")
	writeFile(t, dir, "real.go", "package main\n\nfunc main() {}\n")

	scope, err := ResolveScope(dir, ScopeOptions{})
	require.NoError(t, err)

	byPath := filesByPath(scope.Files)
	assert.True(t, byPath["go.sum"].Noise, "lockfile")
	assert.True(t, byPath["gen/types.go"].Noise, "generated-code marker in the base revision")
	assert.True(t, byPath["api/client.ts"].Noise, "linguist-generated attribute in the base revision")
	assert.False(t, byPath["real.go"].Noise)
	assert.True(t, byPath["real.go"].Reviewable())
	assert.False(t, byPath["go.sum"].Reviewable())
}

// The change under review must not be able to label its own files
// generated and so take them out of the review.
func TestResolveScope_TheChangeCannotMarkItsOwnFilesGenerated(t *testing.T) {
	dir := newTestRepo(t)
	writeFile(t, dir, "auth.go", "// Code generated by hand; DO NOT EDIT.\n\npackage main\n\nfunc allow() bool { return true }\n")
	writeFile(t, dir, "feature.go", "package main\n\nfunc main() { println(\"changed\") }\n")
	writeFile(t, dir, ".gitattributes", "feature.go linguist-generated\n")

	scope, err := ResolveScope(dir, ScopeOptions{})
	require.NoError(t, err)

	byPath := filesByPath(scope.Files)
	assert.False(t, byPath["auth.go"].Noise, "a marker added by the change itself")
	assert.False(t, byPath["feature.go"].Noise, "an attribute added by the change itself")
}

// Code that merely mentions the marker -- a regex for it, a doc comment
// about it -- is not generated.
func TestResolveScope_MentioningTheMarkerIsNotBeingGenerated(t *testing.T) {
	dir := t.TempDir()
	runGitCmd(t, dir, "init", "--initial-branch=main")
	writeFile(t, dir, "detect.go", "package x\n\nvar re = `Code generated .* DO NOT EDIT|@generated`\n")
	runGitCmd(t, dir, "add", "--all")
	runGitCmd(t, dir, "commit", "-m", "base")
	runGitCmd(t, dir, "checkout", "-b", "feature")
	writeFile(t, dir, "detect.go", "package x\n\nvar re = `Code generated .* DO NOT EDIT|@generated`\n\nvar y = 1\n")

	scope, err := ResolveScope(dir, ScopeOptions{})
	require.NoError(t, err)
	assert.False(t, filesByPath(scope.Files)["detect.go"].Noise)
}

func TestResolveScope_RedactedFileIsFlagged(t *testing.T) {
	dir := newTestRepo(t)
	writeFile(t, dir, ".env", "API_KEY=top-secret-value\n")

	scope, err := ResolveScope(dir, ScopeOptions{})
	require.NoError(t, err)

	byPath := filesByPath(scope.Files)
	require.Contains(t, byPath, ".env")
	assert.True(t, byPath[".env"].Redacted)
	assert.False(t, byPath[".env"].Reviewable())
	assert.NotContains(t, byPath[".env"].Block, "top-secret-value")
}

func filesByPath(files []FileDiff) map[string]*FileDiff {
	out := map[string]*FileDiff{}
	for i := range files {
		out[files[i].Path] = &files[i]
	}
	return out
}

func TestResolveScope_RedactsRenameIntoSecretShapedName(t *testing.T) {
	// The file being renamed must already exist in the base commit (not
	// just added within the branch being diffed), or there's nothing in
	// the base tree for git's rename detection to pair the new path
	// against -- it would just render as a plain new-file addition of the
	// final path, incidentally also correctly redacted, but not actually
	// exercising the rename-specific bug this test is for.
	dir := t.TempDir()
	runGitCmd(t, dir, "init", "--initial-branch=main")
	writeFile(t, dir, "notes.txt", "package main\n\nfunc main() {}\n")
	runGitCmd(t, dir, "add", "notes.txt")
	runGitCmd(t, dir, "commit", "-m", "add notes.txt")
	runGitCmd(t, dir, "checkout", "-b", "feature")
	runGitCmd(t, dir, "mv", "notes.txt", ".env")
	// Appending to (rather than replacing) the original content keeps
	// similarity high, so git's rename detection pairs this with the
	// deleted notes.txt as a rename rather than a plain delete+add.
	writeFile(t, dir, ".env", "package main\n\nfunc main() {}\n\n// secret-marker-content\n")
	rawDiff := runGitCmd(t, dir, "diff", "--find-renames", "main")
	require.Contains(t, rawDiff, "rename from notes.txt", "test setup: this must actually render as a rename, or it isn't exercising the bug")

	scope, err := ResolveScope(dir, ScopeOptions{})
	require.NoError(t, err)

	assert.NotContains(t, scope.Diff(), "secret-marker-content")
	assert.Contains(t, scope.Diff(), "omitted")
}

func TestResolveScope_RedactsRenameFromSecretShapedName(t *testing.T) {
	dir := t.TempDir()
	runGitCmd(t, dir, "init", "--initial-branch=main")
	writeFile(t, dir, ".env", "API_KEY=top-secret-value\nUNCHANGED_LINE=1\nANOTHER_UNCHANGED=2\n")
	runGitCmd(t, dir, "add", ".env")
	runGitCmd(t, dir, "commit", "-m", "add secret file")
	runGitCmd(t, dir, "checkout", "-b", "feature")
	runGitCmd(t, dir, "mv", ".env", "notes.txt")
	// A renamed-away secret file can still leak its old content through the
	// hunk even though the resulting filename isn't secret-shaped -- only
	// touch one line so most of the original (including the secret value)
	// still appears verbatim in context lines, not just the diff hunk.
	writeFile(t, dir, "notes.txt", "API_KEY=top-secret-value\nUNCHANGED_LINE=1\nANOTHER_UNCHANGED=2\nEXTRA=3\n")
	rawDiff := runGitCmd(t, dir, "diff", "--find-renames", "main")
	require.Contains(t, rawDiff, "rename from .env", "test setup: this must actually render as a rename, or it isn't exercising the bug")

	scope, err := ResolveScope(dir, ScopeOptions{})
	require.NoError(t, err)

	assert.NotContains(t, scope.Diff(), "top-secret-value")
	assert.Contains(t, scope.Diff(), "omitted")
}

func TestResolveScope_RecordsRemoteURLAndHeadAuthor(t *testing.T) {
	dir := newTestRepo(t)
	runGitCmd(t, dir, "remote", "add", "origin", "git@github.com:acme/widget.git")

	scope, err := ResolveScope(dir, ScopeOptions{})
	require.NoError(t, err)

	assert.Equal(t, "git@github.com:acme/widget.git", scope.RemoteURL)
	assert.Equal(t, "test <test@example.com>", scope.HeadAuthor, "newTestRepo commits as GIT_AUTHOR_NAME/EMAIL test/test@example.com")
}

func TestResolveScope_NoRemoteLeavesRemoteURLEmpty(t *testing.T) {
	dir := newTestRepo(t)

	scope, err := ResolveScope(dir, ScopeOptions{})
	require.NoError(t, err, "a repo with no remote must still be reviewable -- only reporting needs one")
	assert.Empty(t, scope.RemoteURL)
}

func TestResolveScope_UsesNamedRemoteFromOptions(t *testing.T) {
	dir := newTestRepo(t)
	runGitCmd(t, dir, "remote", "add", "origin", "git@github.com:acme/origin-repo.git")
	runGitCmd(t, dir, "remote", "add", "upstream", "git@github.com:acme/upstream-repo.git")

	scope, err := ResolveScope(dir, ScopeOptions{RemoteName: "upstream"})
	require.NoError(t, err)
	assert.Equal(t, "git@github.com:acme/upstream-repo.git", scope.RemoteURL)
}

// A local main left behind origin/main must not become the base: a branch cut
// from origin/main would then be diffed against an old commit, and the review
// would cover everything merged since.
func TestResolveScope_StaleLocalMainUsesTheNearerOriginMain(t *testing.T) {
	dir := newTestRepo(t)
	merged := commitOnNewBranch(t, dir, "main", "upstream", "merged_by_others.go")
	runGitCmd(t, dir, "update-ref", "refs/remotes/origin/main", merged)
	runGitCmd(t, dir, "checkout", "-b", "fresh", "origin/main")
	writeFile(t, dir, "mine.go", "package main\n")
	runGitCmd(t, dir, "add", "mine.go")
	runGitCmd(t, dir, "commit", "-m", "mine")

	scope, err := ResolveScope(dir, ScopeOptions{})
	require.NoError(t, err)

	assert.Equal(t, merged, scope.BaseRef)
	assert.Contains(t, scope.Diff(), "mine.go")
	assert.NotContains(t, scope.Diff(), "merged_by_others.go")
}

func TestResolveScope_LocalMainAheadOfOriginMainIsTheBase(t *testing.T) {
	dir := newTestRepo(t)
	runGitCmd(t, dir, "update-ref", "refs/remotes/origin/main", "main")
	ahead := commitOnNewBranch(t, dir, "main", "local-work", "local_only.go")
	runGitCmd(t, dir, "branch", "-f", "main", ahead)
	runGitCmd(t, dir, "checkout", "-b", "on-local", "main")
	writeFile(t, dir, "mine.go", "package main\n")
	runGitCmd(t, dir, "add", "mine.go")
	runGitCmd(t, dir, "commit", "-m", "mine")

	scope, err := ResolveScope(dir, ScopeOptions{})
	require.NoError(t, err)

	assert.Equal(t, ahead, scope.BaseRef)
	assert.NotContains(t, scope.Diff(), "local_only.go")
}

func TestResolveScope_OnlyOriginMain(t *testing.T) {
	dir := newTestRepo(t)
	runGitCmd(t, dir, "update-ref", "refs/remotes/origin/main", "main")
	runGitCmd(t, dir, "branch", "-D", "main")

	scope, err := ResolveScope(dir, ScopeOptions{})
	require.NoError(t, err)

	assert.Contains(t, scope.Diff(), "feature.go")
}

func TestResolveScope_OnlyMaster(t *testing.T) {
	dir := newTestRepo(t)
	runGitCmd(t, dir, "branch", "-m", "main", "master")

	scope, err := ResolveScope(dir, ScopeOptions{})
	require.NoError(t, err)

	assert.Contains(t, scope.Diff(), "feature.go")
	assert.NotContains(t, scope.Diff(), "README.md")
}

// Bases neither of which is an ancestor of the other, from a branch that
// merged both lines: the one fewer commits behind HEAD wins, whichever
// candidate comes first.
func TestResolveScope_IncomparableBasesTakeTheNearer(t *testing.T) {
	dir := newTestRepo(t)
	local := commitOnNewBranch(t, dir, "main", "local-line", "local_line.go")
	commitOnNewBranch(t, dir, "main", "origin-line", "origin_one.go")
	writeFile(t, dir, "origin_two.go", "package main\n")
	runGitCmd(t, dir, "add", "origin_two.go")
	runGitCmd(t, dir, "commit", "-m", "origin two")
	origin := strings.TrimSpace(runGitCmd(t, dir, "rev-parse", "HEAD"))
	runGitCmd(t, dir, "branch", "-f", "main", local)
	runGitCmd(t, dir, "update-ref", "refs/remotes/origin/main", origin)
	runGitCmd(t, dir, "checkout", "-b", "merged-both", "local-line")
	runGitCmd(t, dir, "merge", "--no-edit", "origin-line")
	writeFile(t, dir, "mine.go", "package main\n")
	runGitCmd(t, dir, "add", "mine.go")
	runGitCmd(t, dir, "commit", "-m", "mine")

	scope, err := ResolveScope(dir, ScopeOptions{})
	require.NoError(t, err)

	assert.Equal(t, origin, scope.BaseRef)
	assert.NotContains(t, scope.Diff(), "origin_two.go")
}

// commitOnNewBranch commits file on a new branch cut from base, returns the
// commit and leaves the branch checked out.
func commitOnNewBranch(t *testing.T, dir, base, branch, file string) string {
	t.Helper()
	runGitCmd(t, dir, "checkout", "-b", branch, base)
	writeFile(t, dir, file, "package main\n")
	runGitCmd(t, dir, "add", file)
	runGitCmd(t, dir, "commit", "-m", "add "+file)
	return strings.TrimSpace(runGitCmd(t, dir, "rev-parse", "HEAD"))
}

func TestResolveScope_OnMainWithoutFromCommitHash(t *testing.T) {
	dir := newTestRepo(t)
	runGitCmd(t, dir, "checkout", "main")

	_, err := ResolveScope(dir, ScopeOptions{})
	assert.ErrorContains(t, err, "--from-commit-hash")
}

func TestResolveScope_FromCommitHash(t *testing.T) {
	dir := newTestRepo(t)
	firstCommit := strings.TrimSpace(runGitCmd(t, dir, "rev-parse", "HEAD~1"))

	scope, err := ResolveScope(dir, ScopeOptions{FromCommitHash: firstCommit})
	require.NoError(t, err)
	assert.Equal(t, firstCommit, scope.BaseRef)
	assert.Contains(t, scope.Diff(), "feature.go")
}

func TestResolveScope_InvalidFromCommitHash(t *testing.T) {
	dir := newTestRepo(t)

	_, err := ResolveScope(dir, ScopeOptions{FromCommitHash: "does-not-exist"})
	assert.Error(t, err)
}

func TestResolveScope_FromCommitHashLooksLikeFlag(t *testing.T) {
	dir := newTestRepo(t)

	_, err := ResolveScope(dir, ScopeOptions{FromCommitHash: "--force"})
	assert.ErrorContains(t, err, "looks like a flag")
}

func TestResolveScope_NoChanges(t *testing.T) {
	dir := newTestRepo(t)
	head := strings.TrimSpace(runGitCmd(t, dir, "rev-parse", "HEAD"))

	_, err := ResolveScope(dir, ScopeOptions{FromCommitHash: head})
	assert.ErrorContains(t, err, "no changes")
}

func runGitCmd(t *testing.T, dir string, args ...string) string {
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

func TestResolveScope_FromSubdirectoryUsesRepoRootRelativePaths(t *testing.T) {
	// The bug this guards: a review invoked from a subdirectory got a diff
	// full of repo-root-relative paths while its toolset was rooted at the
	// subdirectory, so every path the model read out of the diff failed to
	// open.
	dir := newTestRepo(t)
	sub := filepath.Join(dir, "nested", "pkg")
	require.NoError(t, os.MkdirAll(sub, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(sub, "deep.go"), []byte("package pkg\n\n// deep-change\n"), 0o644))

	scope, err := ResolveScope(sub, ScopeOptions{})
	require.NoError(t, err)

	assert.Equal(t, dir, scope.RepoRoot, "RepoRoot must be the repository toplevel, not the invocation directory")
	assert.Equal(t, "nested/pkg/", scope.ScopePath)
	assert.Contains(t, scope.Diff(), "nested/pkg/deep.go", "diff paths must be repo-root-relative")
	assert.NotContains(t, scope.Diff(), "+++ b/deep.go", "diff paths must not be subdirectory-relative")

	// The decisive check: a path taken from the diff must actually resolve
	// through a toolset rooted at scope.RepoRoot.
	_, statErr := os.Stat(filepath.Join(scope.RepoRoot, "nested/pkg/deep.go"))
	assert.NoError(t, statErr, "paths named in the diff must open relative to scope.RepoRoot")
}

func TestResolveScope_FromSubdirectoryScopesToThatSubtree(t *testing.T) {
	dir := newTestRepo(t)
	sub := filepath.Join(dir, "nested")
	require.NoError(t, os.MkdirAll(sub, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(sub, "in_scope.go"), []byte("package nested\n"), 0o644))
	// A change outside the subdirectory must not be pulled into the review.
	writeFile(t, dir, "out_of_scope.go", "package main\n\n// unrelated\n")

	scope, err := ResolveScope(sub, ScopeOptions{})
	require.NoError(t, err)

	assert.Contains(t, scope.Diff(), "nested/in_scope.go")
	assert.NotContains(t, scope.Diff(), "out_of_scope.go")
}

func TestResolveScope_FromRepoRootHasEmptyScopePath(t *testing.T) {
	dir := newTestRepo(t)

	scope, err := ResolveScope(dir, ScopeOptions{})
	require.NoError(t, err)

	assert.Equal(t, dir, scope.RepoRoot)
	assert.Equal(t, "", scope.ScopePath)
	assert.Contains(t, scope.Diff(), "feature.go")
}

func TestResolveScope_FromSubdirectoryWithPathspecMagicCharacters(t *testing.T) {
	// A directory whose name starts with ":" is pathspec magic to git, not
	// a path -- passed through unescaped it silently matches nothing, so a
	// review of that directory would report "no changes".
	dir := newTestRepo(t)
	sub := filepath.Join(dir, ":weird")
	require.NoError(t, os.MkdirAll(sub, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(sub, "c.go"), []byte("package weird\n\n// magic-dir-change\n"), 0o644))

	scope, err := ResolveScope(sub, ScopeOptions{})
	require.NoError(t, err)

	assert.Equal(t, ":weird/", scope.ScopePath)
	assert.Contains(t, scope.Diff(), "magic-dir-change")
}
