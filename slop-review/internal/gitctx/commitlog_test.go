package gitctx

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCommitLog_OldestFirstMergesDropped(t *testing.T) {
	dir := newTestRepo(t) // feature: "add feature"
	runGitCmd(t, dir, "checkout", "-b", "side", "main")
	writeFile(t, dir, "side.txt", "x\n")
	runGitCmd(t, dir, "add", "side.txt")
	runGitCmd(t, dir, "commit", "-m", "side work")
	runGitCmd(t, dir, "checkout", "feature")
	writeFile(t, dir, "second.txt", "y\n")
	runGitCmd(t, dir, "add", "second.txt")
	runGitCmd(t, dir, "commit", "-m", "second change", "-m", "body of the second")
	runGitCmd(t, dir, "merge", "--no-ff", "-m", "Merge side into feature", "side")

	log, warning := CommitLog(dir, "main", CommitLogMaxBytes)

	assert.Empty(t, warning)
	assert.NotContains(t, log, "Merge side into feature")
	first, second := strings.Index(log, "add feature"), strings.Index(log, "second change")
	require.True(t, first >= 0 && second > first, "oldest first: %q", log)
	assert.Contains(t, log, "body of the second")
	// The merged-in commit is on the branch's history too; only the merge
	// commit itself is dropped.
	assert.Contains(t, log, "side work")
}

func TestCommitLog_CapKeepsNewest(t *testing.T) {
	dir := newTestRepo(t)
	for _, name := range []string{"older one", "newest one"} {
		writeFile(t, dir, name+".txt", "z\n")
		runGitCmd(t, dir, "add", ".")
		runGitCmd(t, dir, "commit", "-m", name)
	}

	log, warning := CommitLog(dir, "main", 55)

	assert.Empty(t, warning)
	assert.LessOrEqual(t, len(log), 55, "the cap holds for the rendered text, note included")
	assert.True(t, strings.HasPrefix(log, "[2 older commit(s) omitted]\n\n"), log)
	assert.True(t, strings.HasSuffix(log, " newest one"), "the newest commit is kept whole: %q", log)
	assert.NotContains(t, log, "older one")
}

func TestCommitLog_OversizedSingleMessageIsTruncated(t *testing.T) {
	dir := newTestRepo(t)
	writeFile(t, dir, "big.txt", "z\n")
	runGitCmd(t, dir, "add", ".")
	runGitCmd(t, dir, "commit", "-m", "big", "-m", strings.Repeat("é", 500))

	log, warning := CommitLog(dir, "main", 100)

	assert.Empty(t, warning)
	assert.LessOrEqual(t, len(log), 100)
	assert.Contains(t, log, "…")
	assert.True(t, utf8Valid(log))
}

func TestCommitLog_UnreadableLogIsAWarning(t *testing.T) {
	dir := newTestRepo(t)

	log, warning := CommitLog(dir, "no-such-revision", CommitLogMaxBytes)

	assert.Empty(t, log)
	assert.Contains(t, warning, "commit log unavailable")
}

func utf8Valid(s string) bool { return utf8.ValidString(s) }
