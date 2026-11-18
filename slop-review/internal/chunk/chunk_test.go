package chunk

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/lowaa/slop-factory/slop-review/internal/gitctx"
)

// file builds a minimal FileDiff fixture: Block is `size` bytes, so
// Size() == size exactly, without needing a real diff body.
func file(path string, size int) gitctx.FileDiff {
	return gitctx.FileDiff{Path: path, Block: strings.Repeat("x", size)}
}

func noiseFile(path string, size int) gitctx.FileDiff {
	f := file(path, size)
	f.Noise = true
	return f
}

func redactedFile(path string, size int) gitctx.FileDiff {
	f := file(path, size)
	f.Redacted = true
	return f
}

// ---- Whole ----

func TestWhole_SumsReviewableFilesInOrderAndExcludesNoiseAndRedacted(t *testing.T) {
	files := []gitctx.FileDiff{
		file("a.go", 10),
		noiseFile("go.sum", 999),
		file("b.go", 20),
		redactedFile(".env", 500),
	}

	chunks := Whole(files)

	assert.Len(t, chunks, 1)
	assert.Equal(t, []string{"a.go", "b.go"}, chunks[0].Files)
	assert.Equal(t, 30, chunks[0].Bytes)
}

func TestWhole_KeepsOnlyNoiseWhenThatIsAllThereIs(t *testing.T) {
	// A lockfile-only change (or an all-generated one) must still be
	// reviewed rather than silently waved through.
	files := []gitctx.FileDiff{
		noiseFile("go.sum", 5),
		redactedFile(".env", 7),
	}

	chunks := Whole(files)

	assert.Len(t, chunks, 1)
	assert.ElementsMatch(t, []string{"go.sum", ".env"}, chunks[0].Files)
	assert.Equal(t, 12, chunks[0].Bytes)
}

// ---- ByPackage: grouping and packing ----

func TestByPackage_GroupsBySortedDirectoryWhenEverythingFitsOneChunk(t *testing.T) {
	files := []gitctx.FileDiff{
		file("z/only.go", 10),
		file("a/only.go", 10),
	}

	chunks := ByPackage(files, 1000)

	assert.Len(t, chunks, 1)
	assert.Equal(t, []string{"a/only.go", "z/only.go"}, chunks[0].Files, "groups pack in sorted directory order")
}

func TestByPackage_GroupThatDoesNotFitCurrentChunkStartsNewChunk(t *testing.T) {
	files := []gitctx.FileDiff{
		file("a/x.go", 60),
		file("b/y.go", 60),
	}

	chunks := ByPackage(files, 100)

	if assert.Len(t, chunks, 2) {
		assert.Equal(t, []string{"a/x.go"}, chunks[0].Files)
		assert.Equal(t, 60, chunks[0].Bytes)
		assert.Equal(t, []string{"b/y.go"}, chunks[1].Files)
		assert.Equal(t, 60, chunks[1].Bytes)
	}
}

func TestByPackage_OversizedGroupSplitsOnItsOwnNotMixedWithNeighbours(t *testing.T) {
	files := []gitctx.FileDiff{
		file("a/small.go", 10),
		file("b/x1.go", 60),
		file("b/x2.go", 60), // group "b" totals 120 > budget 100
		file("c/small.go", 10),
	}

	chunks := ByPackage(files, 100)

	if assert.Len(t, chunks, 4) {
		assert.Equal(t, []string{"a/small.go"}, chunks[0].Files, "neighbour before the oversized group keeps its own chunk")
		assert.Equal(t, []string{"b/x1.go"}, chunks[1].Files)
		assert.Equal(t, []string{"b/x2.go"}, chunks[2].Files)
		assert.Equal(t, []string{"c/small.go"}, chunks[3].Files, "neighbour after the oversized group is not folded into its split")
	}
	for _, c := range chunks {
		assert.LessOrEqual(t, c.Bytes, 100)
	}
}

func TestByPackage_ChunkNeverExceedsBudgetExceptForASingleOversizedFile(t *testing.T) {
	files := []gitctx.FileDiff{
		file("a/small.go", 10),
		file("d/huge.go", 150), // bigger than the budget all on its own
		file("e/small.go", 10),
	}

	chunks := ByPackage(files, 100)

	var sawOversizedAlone bool
	for _, c := range chunks {
		if c.Bytes > 100 {
			assert.Len(t, c.Files, 1, "a chunk may only exceed budget when it holds a single file that alone is bigger than budget")
			assert.Equal(t, []string{"d/huge.go"}, c.Files)
			sawOversizedAlone = true
		}
	}
	assert.True(t, sawOversizedAlone, "expected the oversized file to appear in its own over-budget chunk")
}

// ---- ByPackage: test pairing ----

func TestByPackage_GoTestStaysWithItsImplementation(t *testing.T) {
	files := []gitctx.FileDiff{
		file("pkg/foo_test.go", 10),
		file("pkg/foo.go", 10),
	}

	chunks := ByPackage(files, 1000)

	assert.Len(t, chunks, 1)
	assert.Equal(t, []string{"pkg/foo.go", "pkg/foo_test.go"}, chunks[0].Files)
}

func TestByPackage_PythonTestFileJoinsSoleImplementationsGroup(t *testing.T) {
	// "utils" is chosen because it sorts *after* "tests" -- if pairing
	// didn't happen, test_foo.py would fall back to its own "tests" group
	// and, being alphabetically first, would front the chunk instead of
	// trailing foo.py. That makes this a real discriminator, unlike a
	// same-directory example where the fallback grouping would coincide
	// with the paired one.
	files := []gitctx.FileDiff{
		file("tests/test_foo.py", 10),
		file("utils/foo.py", 10),
	}

	chunks := ByPackage(files, 1000)

	assert.Len(t, chunks, 1)
	assert.Equal(t, []string{"utils/foo.py", "tests/test_foo.py"}, chunks[0].Files, "test_foo.py should join foo.py's group (utils), not stay in tests/")
}

func TestByPackage_JSTestSuffixesPairWithImplementation(t *testing.T) {
	files := []gitctx.FileDiff{
		file("src/foo.test.tsx", 10),
		file("src/foo.tsx", 10),
		file("lib/bar.spec.ts", 10),
		file("lib/bar.ts", 10),
	}

	chunks := ByPackage(files, 1000)

	assert.Len(t, chunks, 1)
	assert.Equal(t, []string{
		"lib/bar.ts", "lib/bar.spec.ts",
		"src/foo.tsx", "src/foo.test.tsx",
	}, chunks[0].Files)
}

func TestByPackage_ImplementationIsImmediatelyFollowedByItsTest(t *testing.T) {
	// Deliberately shuffled input order to prove the output order comes
	// from the pairing logic, not from input order.
	files := []gitctx.FileDiff{
		file("pkg/b_test.go", 5),
		file("pkg/a.go", 5),
		file("pkg/b.go", 5),
		file("pkg/a_test.go", 5),
	}

	chunks := ByPackage(files, 1000)

	assert.Len(t, chunks, 1)
	assert.Equal(t, []string{"pkg/a.go", "pkg/a_test.go", "pkg/b.go", "pkg/b_test.go"}, chunks[0].Files)
}

func TestByPackage_AmbiguousBasenameLeavesTestInItsOwnDirectoryGroup(t *testing.T) {
	// Two changed foo.py files in different directories: testedFile can't
	// tell which one tests/test_foo.py belongs to, so it stays put.
	files := []gitctx.FileDiff{
		file("tests/test_foo.py", 10),
		file("pkg2/foo.py", 10),
		file("pkg1/foo.py", 10),
	}

	chunks := ByPackage(files, 1000)

	assert.Len(t, chunks, 1)
	assert.Equal(t, []string{"pkg1/foo.py", "pkg2/foo.py", "tests/test_foo.py"}, chunks[0].Files)
}

func TestByPackage_AmbiguousBasenamePairsWithCandidateInTestsOwnDirectory(t *testing.T) {
	// Still ambiguous by basename alone, but one of the candidates lives
	// in the test's own directory, which resolves the tie.
	files := []gitctx.FileDiff{
		file("tests/test_foo.py", 10),
		file("tests/foo.py", 10),
		file("pkg1/foo.py", 10),
	}

	chunks := ByPackage(files, 1000)

	assert.Len(t, chunks, 1)
	assert.Equal(t, []string{"pkg1/foo.py", "tests/foo.py", "tests/test_foo.py"}, chunks[0].Files)
}

func TestByPackage_SplitKeepsAnImplementationAdjacentToItsTest(t *testing.T) {
	files := []gitctx.FileDiff{
		file("pkg/a.go", 40),
		file("pkg/a_test.go", 40),
		file("pkg/b.go", 40),
		file("pkg/b_test.go", 40), // group totals 160 > budget 100
	}

	chunks := ByPackage(files, 100)

	if assert.Len(t, chunks, 2) {
		assert.Equal(t, []string{"pkg/a.go", "pkg/a_test.go"}, chunks[0].Files)
		assert.Equal(t, 80, chunks[0].Bytes)
		assert.Equal(t, []string{"pkg/b.go", "pkg/b_test.go"}, chunks[1].Files)
		assert.Equal(t, 80, chunks[1].Bytes)
	}
	for _, c := range chunks {
		assert.LessOrEqual(t, c.Bytes, 100)
	}
}

// ---- Every reviewable file appears exactly once ----

func TestByPackage_EveryReviewableFileAppearsExactlyOnce(t *testing.T) {
	files := []gitctx.FileDiff{
		file("a/foo.go", 30),
		file("a/foo_test.go", 20),
		file("b/x1.go", 50),
		file("b/x1_test.go", 50),
		file("b/x2.go", 50),
		file("c/lonely.go", 10),
		noiseFile("d/go.sum", 999),
		redactedFile("e/.env", 500),
	}
	wantReviewable := []string{
		"a/foo.go", "a/foo_test.go",
		"b/x1.go", "b/x1_test.go", "b/x2.go",
		"c/lonely.go",
	}

	chunks := ByPackage(files, 100)

	var got []string
	seen := map[string]int{}
	for _, c := range chunks {
		for _, p := range c.Files {
			got = append(got, p)
			seen[p]++
		}
	}
	assert.ElementsMatch(t, wantReviewable, got)
	for p, n := range seen {
		assert.Equal(t, 1, n, "%s appeared in %d chunks, want exactly 1", p, n)
	}
}

func TestWhole_EveryReviewableFileAppearsExactlyOnce(t *testing.T) {
	files := []gitctx.FileDiff{
		file("a/foo.go", 30),
		file("a/foo_test.go", 20),
		noiseFile("d/go.sum", 999),
		redactedFile("e/.env", 500),
	}
	wantReviewable := []string{"a/foo.go", "a/foo_test.go"}

	chunks := Whole(files)

	var got []string
	for _, c := range chunks {
		got = append(got, c.Files...)
	}
	assert.ElementsMatch(t, wantReviewable, got)
}

// Go tests live in their package's directory, so main_test.go (a TestMain)
// in one package must not be pulled towards the only changed main.go,
// which belongs to a different package entirely.
func TestByPackage_GoTestIsNeverPairedAcrossDirectories(t *testing.T) {
	files := []gitctx.FileDiff{
		file("cmd/api/main.go", 10),
		file("internal/settings/main_test.go", 10),
		file("internal/settings/store.go", 10),
	}

	chunks := ByPackage(files, 20)

	var settingsChunk []string
	for _, c := range chunks {
		for _, f := range c.Files {
			if f == "internal/settings/store.go" {
				settingsChunk = c.Files
			}
		}
	}
	assert.Contains(t, settingsChunk, "internal/settings/main_test.go")
	assert.NotContains(t, settingsChunk, "cmd/api/main.go")
}
