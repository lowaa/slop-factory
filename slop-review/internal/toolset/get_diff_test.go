package toolset

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-review/internal/gitctx"
)

func newDiffDispatcher(t *testing.T, owned ...string) *Dispatcher {
	t.Helper()
	files := []gitctx.FileDiff{
		{Path: "a.go", Block: "diff --git a/a.go b/a.go\n@@ -1 +1 @@\n-old a\n+new a\n"},
		{Path: "b/renamed.go", OldPath: "b/original.go", Block: "diff --git a/b/original.go b/b/renamed.go\n+renamed body\n"},
		{Path: "go.sum", Noise: true, Block: "diff --git a/go.sum b/go.sum\n+hash\n"},
		{Path: ".env", Redacted: true, Block: "diff --git a/.env b/.env\n[omitted]\n"},
	}
	return New(&Options{RepoRoot: t.TempDir(), OutputCapBytes: 4096, Files: files, Owned: owned})
}

func TestGetDiff_ServesAChangedFile(t *testing.T) {
	d := newDiffDispatcher(t)

	out := d.Dispatch("get_diff", mustJSON(t, map[string]any{"path": "a.go"}))
	assert.Contains(t, out, "+new a")
	assert.Contains(t, out, "-old a")
}

func TestGetDiff_LineRange(t *testing.T) {
	d := newDiffDispatcher(t)

	out := d.Dispatch("get_diff", mustJSON(t, map[string]any{"path": "a.go", "start_line": 4, "end_line": 4}))
	assert.Contains(t, out, "+new a")
	assert.NotContains(t, out, "-old a")
	assert.Contains(t, out, "lines 4-4 of 4")
}

func TestGetDiff_FindsARenameByItsOldPath(t *testing.T) {
	d := newDiffDispatcher(t)

	out := d.Dispatch("get_diff", mustJSON(t, map[string]any{"path": "b/original.go"}))
	assert.Contains(t, out, "renamed body")
}

func TestGetDiff_RefusesAFileOutsideTheChange(t *testing.T) {
	d := newDiffDispatcher(t)

	out := d.Dispatch("get_diff", mustJSON(t, map[string]any{"path": "not-changed.go"}))
	assert.Contains(t, out, "error:")
}

func TestUnseen_ReportsOwnedReviewableFilesNeverShown(t *testing.T) {
	d := newDiffDispatcher(t, "a.go", "b/renamed.go", "go.sum", ".env")

	assert.Equal(t, []string{"a.go", "b/renamed.go"}, d.Unseen(), "noise and redacted files are not owed a review")
	warnings := d.Warnings()
	require.Len(t, warnings, 1)
	assert.Contains(t, warnings[0], "a.go, b/renamed.go")

	d.MarkSeen("a.go")
	d.Dispatch("get_diff", mustJSON(t, map[string]any{"path": "b/original.go"}))
	assert.Empty(t, d.Unseen(), "inlined and fetched files both count as seen")
	assert.Empty(t, d.Warnings())
}

func TestUnseen_IgnoresFilesTheSessionDoesNotOwn(t *testing.T) {
	d := newDiffDispatcher(t, "a.go")
	d.MarkSeen("a.go")

	assert.Empty(t, d.Unseen())
}

func TestUnseen_APartialRangeIsNotTheWholeFile(t *testing.T) {
	d := newDiffDispatcher(t, "a.go")

	d.Dispatch("get_diff", mustJSON(t, map[string]any{"path": "a.go", "start_line": 1, "end_line": 2}))
	assert.Equal(t, []string{"a.go"}, d.Unseen(), "two of four lines is not a review of the file")

	d.Dispatch("get_diff", mustJSON(t, map[string]any{"path": "a.go", "start_line": 3}))
	assert.Empty(t, d.Unseen(), "the two ranges together cover it")
}

func TestGetDiff_PagesALongDiffAndOnlyCountsWhatItDelivered(t *testing.T) {
	var body strings.Builder
	body.WriteString("diff --git a/big.go b/big.go\n@@ -0,0 +1,100 @@\n")
	for i := 1; i <= 100; i++ {
		fmt.Fprintf(&body, "+line %03d of the new file\n", i)
	}
	files := []gitctx.FileDiff{{Path: "big.go", Block: body.String()}}
	d := New(&Options{RepoRoot: t.TempDir(), OutputCapBytes: 1000, Files: files, Owned: []string{"big.go"}})

	first := d.Dispatch("get_diff", mustJSON(t, map[string]any{"path": "big.go"}))
	assert.LessOrEqual(t, len(first), 1000)
	assert.Contains(t, first, "+line 001")
	assert.NotContains(t, first, "+line 100")
	assert.Contains(t, first, "call get_diff again with start_line=")
	assert.Equal(t, []string{"big.go"}, d.Unseen(), "a capped result is not the whole diff")

	for i := 0; i < 10 && len(d.Unseen()) > 0; i++ {
		out := d.Dispatch("get_diff", mustJSON(t, map[string]any{"path": "big.go", "start_line": nextStart(t, first)}))
		require.NotContains(t, out, "error:")
		first = out
	}
	assert.Empty(t, d.Unseen(), "paging through with the suggested start_line covers the whole diff")
}

func TestGetDiff_ALineLongerThanTheCapStillCounts(t *testing.T) {
	files := []gitctx.FileDiff{{Path: "min.js", Block: "+" + strings.Repeat("x", 5000) + "\n"}}
	d := New(&Options{RepoRoot: t.TempDir(), OutputCapBytes: 1000, Files: files, Owned: []string{"min.js"}})

	out := d.Dispatch("get_diff", mustJSON(t, map[string]any{"path": "min.js"}))
	assert.LessOrEqual(t, len(out), 1000)
	assert.Empty(t, d.Unseen(), "otherwise a minified file could never count as seen")
}

func nextStart(t *testing.T, out string) int {
	t.Helper()
	m := regexp.MustCompile(`start_line=(\d+)`).FindStringSubmatch(out)
	require.NotNil(t, m, "expected a paging note in %q", out)
	n, err := strconv.Atoi(m[1])
	require.NoError(t, err)
	return n
}
