package gitctx

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestShow(t *testing.T) {
	dir := newTestRepo(t)

	out, err := Show(dir, "HEAD")
	require.NoError(t, err)
	assert.Contains(t, out, "add feature")
	assert.Contains(t, out, "feature.go")
}

func TestShow_RejectsFlagLikeRef(t *testing.T) {
	dir := newTestRepo(t)

	_, err := Show(dir, "--help")
	assert.ErrorContains(t, err, "looks like a flag")
}

func TestLog(t *testing.T) {
	dir := newTestRepo(t)

	out, err := Log(dir, "", 10, "")
	require.NoError(t, err)
	assert.Contains(t, out, "add feature")
	assert.Contains(t, out, "initial commit")
}

func TestLog_MaxCount(t *testing.T) {
	dir := newTestRepo(t)

	out, err := Log(dir, "", 1, "")
	require.NoError(t, err)
	assert.Contains(t, out, "add feature")
	assert.NotContains(t, out, "initial commit")
}

func TestLog_ScopedToPath(t *testing.T) {
	dir := newTestRepo(t)

	out, err := Log(dir, "", 10, "README.md")
	require.NoError(t, err)
	assert.Contains(t, out, "initial commit")
	assert.NotContains(t, out, "add feature")
}

func TestBlame(t *testing.T) {
	dir := newTestRepo(t)

	out, err := Blame(dir, "feature.go", 0, 0)
	require.NoError(t, err)
	assert.Contains(t, out, "func main()")
}

func TestHeadSHA(t *testing.T) {
	dir := newTestRepo(t)

	sha, err := HeadSHA(dir)
	require.NoError(t, err)
	want, err := Show(dir, "HEAD")
	require.NoError(t, err)
	assert.Contains(t, want, sha[:7], "HeadSHA must report the commit `git show HEAD` shows")
	assert.Len(t, sha, 40, "a full, not abbreviated, commit hash")
}

func TestRemoteURL_NoRemoteIsEmptyNotAnError(t *testing.T) {
	dir := newTestRepo(t)

	url, err := RemoteURL(dir, "origin")
	require.NoError(t, err)
	assert.Empty(t, url)
}

func TestRemoteURL_ReturnsConfiguredOrigin(t *testing.T) {
	dir := newTestRepo(t)
	runGitCmd(t, dir, "remote", "add", "origin", "git@github.com:acme/widget.git")

	url, err := RemoteURL(dir, "origin")
	require.NoError(t, err)
	assert.Equal(t, "git@github.com:acme/widget.git", url)
}

func TestRemoteURL_NamedNonOriginRemote(t *testing.T) {
	dir := newTestRepo(t)
	runGitCmd(t, dir, "remote", "add", "upstream", "git@github.com:acme/widget.git")

	url, err := RemoteURL(dir, "upstream")
	require.NoError(t, err)
	assert.Equal(t, "git@github.com:acme/widget.git", url)

	// A remote that isn't the one named must not be picked up instead.
	originURL, err := RemoteURL(dir, "origin")
	require.NoError(t, err)
	assert.Empty(t, originURL)
}

func TestRemoteURL_RejectsFlagLikeName(t *testing.T) {
	dir := newTestRepo(t)

	_, err := RemoteURL(dir, "--force")
	assert.ErrorContains(t, err, "looks like a flag")
}

func TestHeadAuthor(t *testing.T) {
	dir := newTestRepo(t)

	author, err := HeadAuthor(dir)
	require.NoError(t, err)
	assert.Equal(t, "test <test@example.com>", author, "newTestRepo commits as GIT_AUTHOR_NAME/EMAIL test/test@example.com")
}

func TestDirty_CleanCheckout(t *testing.T) {
	dir := newTestRepo(t)

	dirty, err := Dirty(dir, "")
	require.NoError(t, err)
	assert.False(t, dirty)
}

func TestDirty_UncommittedEdit(t *testing.T) {
	dir := newTestRepo(t)
	writeFile(t, dir, "feature.go", "package main\n\nfunc main() { println(\"wip\") }\n")

	dirty, err := Dirty(dir, "")
	require.NoError(t, err)
	assert.True(t, dirty)
}

func TestDirty_UntrackedFile(t *testing.T) {
	dir := newTestRepo(t)
	writeFile(t, dir, "new_file.go", "package main\n")

	dirty, err := Dirty(dir, "")
	require.NoError(t, err)
	assert.True(t, dirty)
}

func TestDirty_ScopedToPathIgnoresChangesElsewhere(t *testing.T) {
	dir := newTestRepo(t)
	require.NoError(t, os.MkdirAll(dir+"/other", 0o755))
	writeFile(t, dir, "other/file.go", "package other\n")

	dirty, err := Dirty(dir, "feature.go")
	require.NoError(t, err, "feature.go is unchanged, so the pathspec must resolve to something git accepts even though it names a file, not a directory")
	assert.False(t, dirty, "an untracked file outside the scoped path must not count as dirty")
}

func TestBlame_LineRange(t *testing.T) {
	dir := newTestRepo(t)

	out, err := Blame(dir, "feature.go", 1, 1)
	require.NoError(t, err)
	assert.Contains(t, out, "package main")
	assert.NotContains(t, out, "func main()")
}
