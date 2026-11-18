package diffcheck

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func fileDiff(path, body string) string {
	return "diff --git a/" + path + " b/" + path + "\n--- a/" + path + "\n+++ b/" + path + "\n@@ -1,3 +1,3 @@\n" + body
}

const deletedTest = `diff --git a/pkg/foo_test.go b/pkg/foo_test.go
deleted file mode 100644
index 111..000
--- a/pkg/foo_test.go
+++ /dev/null
@@ -1,2 +0,0 @@
-func TestFoo(t *testing.T) {
-}
`

func TestTestsDeleted(t *testing.T) {
	r := Check(Input{Diff: deletedTest})
	assert.Equal(t, []string{"pkg/foo_test.go"}, r.TestsDeleted.Files)
	assert.True(t, r.TestsDeleted.Known)

	r = Check(Input{Diff: fileDiff("pkg/foo.go", "-a\n+b\n")})
	assert.Empty(t, r.TestsDeleted.Files, "deleting non-test code is not a test deletion")
	assert.True(t, r.TestsDeleted.Known)
}

func TestSkipsAdded(t *testing.T) {
	for _, tc := range []struct{ path, line string }{
		{"a_test.go", `+	t.Skip("flaky")`},
		{"test_a.py", `+@pytest.mark.skip`},
		{"a.test.ts", `+  it.skip("x", () => {})`},
		{"a.spec.js", `+  xit("x", () => {})`},
		{"x.bats", `+  skip "later"`},
	} {
		r := Check(Input{Diff: fileDiff(tc.path, tc.line+"\n")})
		assert.Equal(t, []string{tc.path}, r.SkipsAdded.Files, tc.path)
	}
	r := Check(Input{Diff: fileDiff("a_test.go", "+	assert.True(t, ok)\n")})
	assert.Empty(t, r.SkipsAdded.Files)
	r = Check(Input{Diff: fileDiff("a.go", "+	t.Skip(\"not a test file\")\n")})
	assert.Empty(t, r.SkipsAdded.Files)
}

func TestAssertionsRemoved(t *testing.T) {
	r := Check(Input{Diff: fileDiff("a_test.go", "-	assert.Equal(t, 1, x)\n+	_ = x\n")})
	assert.Equal(t, []string{"a_test.go"}, r.AssertionsRemoved.Files)

	r = Check(Input{Diff: fileDiff("a_test.go", "-	assert.Equal(t, 1, x)\n+	require.Equal(t, 2, x)\n")})
	assert.Empty(t, r.AssertionsRemoved.Files, "a rewritten assertion is not a removal")
}

func TestOutOfScope(t *testing.T) {
	diff := fileDiff("in/a.go", "+x\n") + fileDiff("out/b.go", "+x\n") + fileDiff("inside/c.go", "+x\n")
	r := Check(Input{Diff: diff, Scopes: []string{"in/"}})
	assert.True(t, r.OutOfScope.Known)
	assert.Equal(t, []string{"inside/c.go", "out/b.go"}, r.OutOfScope.Files, "a sibling sharing a name prefix is outside")

	r = Check(Input{Diff: diff, Scopes: []string{"in", "out", "inside"}})
	assert.Empty(t, r.OutOfScope.Files)

	r = Check(Input{Diff: diff})
	assert.False(t, r.OutOfScope.Known, "no scopes means unknown, not clean")
}

func TestRenameOutOfScopeCountsOldPath(t *testing.T) {
	diff := "diff --git a/out/x.go b/in/x.go\nsimilarity index 100%\nrename from out/x.go\nrename to in/x.go\n"
	r := Check(Input{Diff: diff, Scopes: []string{"in"}})
	assert.Equal(t, []string{"out/x.go"}, r.OutOfScope.Files)
}

func TestSizeExcludesGenerated(t *testing.T) {
	var g Generated
	g.AddGitattributes("api", "# gen\n* linguist-generated=true\n")
	g.AddGitattributes("", "**/gen/*.go linguist-generated\n")
	diff := fileDiff("a.go", "-a\n+b\n+c\n") + fileDiff("api/client.ts", "+1\n+2\n+3\n") + fileDiff("gen/x.go", "+1\n") + fileDiff("p/q/gen/y.go", "+1\n")
	r := Check(Input{Diff: diff, Generated: &g})
	assert.Equal(t, Size{Known: true, LinesAdded: 2, LinesRemoved: 1, FilesTouched: 1, GeneratedSkipped: 3, GeneratedLines: 5, GeneratedRulesApplied: true}, r.Size)
	assert.False(t, g.Matches("apix/a.ts"))

	r = Check(Input{Diff: diff})
	assert.False(t, r.Size.GeneratedRulesApplied, "nil rules must be visible, not read as 'nothing generated'")
	assert.Equal(t, 4, r.Size.FilesTouched)
}

func TestEmptyDiffIsUnknown(t *testing.T) {
	r := Check(Input{Scopes: []string{"a"}})
	assert.Equal(t, &Result{}, r)
	assert.False(t, r.Size.Known)
}

func TestModifiedTestIsNotDeleted(t *testing.T) {
	r := Check(Input{Diff: fileDiff("a_test.go", "-x\n+y\n") + fileDiff("test_b.py", "+z\n")})
	assert.Empty(t, r.TestsDeleted.Files)
}

func TestRemovedSkipIsNotAdded(t *testing.T) {
	r := Check(Input{Diff: fileDiff("a_test.go", "-	t.Skip(\"x\")\n+	assert.True(t, ok)\n")})
	assert.True(t, r.SkipsAdded.Known)
	assert.Empty(t, r.SkipsAdded.Files)
	assert.True(t, r.AssertionsRemoved.Known)
}

func TestBinaryFilesCounted(t *testing.T) {
	diff := "diff --git a/x.bin b/x.bin\nindex 1..2 100644\nBinary files a/x.bin and b/x.bin differ\n"
	r := Check(Input{Diff: diff})
	assert.Equal(t, 1, r.Size.BinaryFiles)
	assert.Equal(t, 1, r.Size.FilesTouched)
}

func TestUnparseableDiffIsUnknown(t *testing.T) {
	r := Check(Input{Diff: "not a diff\n"})
	assert.False(t, r.Size.Known)
	assert.False(t, r.TestsDeleted.Known)
}

func TestParseDiffHeaderEdgeCases(t *testing.T) {
	for _, tc := range []struct {
		name    string
		diff    string
		oldPath string
		path    string
		deleted bool
		binary  bool
		nAdded  int
		nRemove int
	}{
		{
			name:    "header without a/ prefix falls back to the whole remainder for both paths",
			diff:    "diff --git x/foo.go y/foo.go\n",
			oldPath: "x/foo.go y/foo.go",
			path:    "x/foo.go y/foo.go",
		},
		{
			name:    "header without ' b/' falls back to the whole remainder for both paths",
			diff:    "diff --git a/foo.go c/foo.go\n",
			oldPath: "a/foo.go c/foo.go",
			path:    "a/foo.go c/foo.go",
		},
		{
			// Documented ambiguity: the last " b/" wins, so the old path is cut short
			// and the new path is only the tail.
			name:    "path containing ' b/' is mis-split",
			diff:    "diff --git a/dir b/x.go b/dir b/x.go\n",
			oldPath: "dir b/x.go b/dir",
			path:    "x.go",
		},
		{
			name:    "git binary patch marks the file binary",
			diff:    "diff --git a/x.bin b/x.bin\nindex 1..2 100644\nGIT binary patch\nliteral 3\nKcmZQz\n",
			oldPath: "x.bin",
			path:    "x.bin",
			binary:  true,
		},
		{
			name:    "deleted file mode keeps the old path and counts removed lines",
			diff:    deletedTest,
			oldPath: "pkg/foo_test.go",
			path:    "pkg/foo_test.go",
			deleted: true,
			nRemove: 2,
		},
		{
			name:    "bare deleted file mode line after a hunk is not a marker",
			diff:    fileDiff("a.go", "+x\ndeleted file mode 100644\n"),
			oldPath: "a.go",
			path:    "a.go",
			nAdded:  1,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			files, err := parseDiff(tc.diff)
			require.NoError(t, err)
			require.Len(t, files, 1)
			f := files[0]
			assert.Equal(t, tc.oldPath, f.oldPath)
			assert.Equal(t, tc.path, f.path)
			assert.Equal(t, tc.deleted, f.deleted)
			assert.Equal(t, tc.binary, f.binary)
			assert.Equal(t, tc.nAdded, f.nAdded)
			assert.Equal(t, tc.nRemove, f.nRemove)
		})
	}
}

func TestParseDiffLongLines(t *testing.T) {
	const limit = 16 * 1024 * 1024
	hunk := "diff --git a/x b/x\n@@ -0,0 +1 @@\n+"

	// The scanner buffer must hold the line and its newline, so a line of
	// limit bytes in total still fits.
	atLimit := strings.Repeat("a", limit-2)
	files, err := parseDiff(hunk + atLimit + "\n")
	require.NoError(t, err)
	require.Len(t, files, 1)
	assert.Equal(t, 1, files[0].nAdded)
	assert.Len(t, files[0].added[0], limit-2)

	_, err = parseDiff(hunk + atLimit + "a\n")
	assert.Error(t, err, "a line past the buffer limit fails the parse")

	r := Check(Input{Diff: hunk + atLimit + "a\n"})
	assert.Equal(t, &Result{}, r, "an oversized line makes every signal unknown, not clean")
}
