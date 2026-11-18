package codehost_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost"
)

func mod(path, patch string) codehost.FileDiff {
	return codehost.FileDiff{Path: path, Status: codehost.DiffModified, Patch: patch}
}

func fp(t *testing.T, files ...codehost.FileDiff) string {
	t.Helper()
	got, err := strategy.Fingerprint(&codehost.Diff{Files: files})
	require.NoError(t, err)
	return got
}

var strategy codehost.ChangeDetectionStrategy = codehost.LineNumberChangeDetectionStrategy{}

const base = "@@ -10,3 +10,4 @@ func one()\n a\n+b\n c\n d\n"

func TestFingerprint_IgnoresFunctionContextCountSpellingAndFileOrder(t *testing.T) {
	a := fp(t, mod("a.go", base), mod("b.go", "@@ -1 +1 @@\n-x\n+y\n"))
	b := fp(t, mod("b.go", "@@ -1,1 +1,1 @@ other()\n-x\n+y"), mod("a.go", "@@ -10,3 +10,4 @@ renamed_func()\n a\n+b\n c\n d"))
	assert.Equal(t, a, b)
	assert.True(t, strings.HasPrefix(a, strategy.Prefix()))
	assert.Equal(t, "lines-v1:", strategy.Prefix())
}

func TestFingerprint_ChangesWithLineNumbers(t *testing.T) {
	ref := fp(t, mod("a.go", base))
	tests := map[string]string{
		"old start shifted":   "@@ -11,3 +10,4 @@\n a\n+b\n c\n d\n",
		"new start shifted":   "@@ -10,3 +11,4 @@\n a\n+b\n c\n d\n",
		"same text elsewhere": "@@ -210,3 +210,4 @@\n a\n+b\n c\n d\n",
	}
	for name, patch := range tests {
		t.Run(name, func(t *testing.T) {
			assert.NotEqual(t, ref, fp(t, mod("a.go", patch)))
		})
	}
}

func TestFingerprint_ChangesWithAnythingTheHumanSaw(t *testing.T) {
	ref := fp(t, mod("a.go", base))
	tests := map[string][]codehost.FileDiff{
		"added line":        {mod("a.go", "@@ -10,3 +10,5 @@\n a\n+b\n+z\n c\n d\n")},
		"changed line":      {mod("a.go", "@@ -10,3 +10,4 @@\n a\n+B\n c\n d\n")},
		"changed context":   {mod("a.go", "@@ -10,3 +10,4 @@\n a\n+b\n c\n D\n")},
		"changed path":      {mod("b.go", base)},
		"changed status":    {{Path: "a.go", Status: codehost.DiffAdded, Patch: base}},
		"extra file":        {mod("a.go", base), mod("b.go", base)},
		"extra hunk":        {mod("a.go", base+"@@ -40 +41 @@\n-x\n+y\n")},
		"no-newline marker": {mod("a.go", base+"\\ No newline at end of file\n")},
	}
	for name, files := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := strategy.Fingerprint(&codehost.Diff{Files: files})
			require.NoError(t, err)
			assert.NotEqual(t, ref, got)
		})
	}
}

func TestFingerprint_FailsClosed(t *testing.T) {
	tests := map[string]*codehost.Diff{
		"nil":                nil,
		"no files":           {},
		"no patch":           {Files: []codehost.FileDiff{mod("a.go", "")}},
		"renamed":            {Files: []codehost.FileDiff{{Path: "a.go", Status: codehost.DiffOther, Patch: base}}},
		"empty status":       {Files: []codehost.FileDiff{{Path: "a.go", Patch: base}}},
		"empty path":         {Files: []codehost.FileDiff{mod("", base)}},
		"repeated path":      {Files: []codehost.FileDiff{mod("a.go", base), mod("a.go", base)}},
		"text before hunk":   {Files: []codehost.FileDiff{mod("a.go", "diff --git a b\n"+base)}},
		"hunk too short":     {Files: []codehost.FileDiff{mod("a.go", "@@ -10,3 +10,4 @@\n a\n+b\n c\n")}},
		"hunk too long":      {Files: []codehost.FileDiff{mod("a.go", base+" extra\n")}},
		"bad header":         {Files: []codehost.FileDiff{mod("a.go", "@@ -x +1 @@\n+a\n")}},
		"leading zero start": {Files: []codehost.FileDiff{mod("a.go", "@@ -01 +1 @@\n-a\n+b\n")}},
		"signed start":       {Files: []codehost.FileDiff{mod("a.go", "@@ -+1 +1 @@\n-a\n+b\n")}},
		"unterminated range": {Files: []codehost.FileDiff{mod("a.go", "@@ -1,2 +1,2\n a\n b\n")}},
		"no hunks":           {Files: []codehost.FileDiff{mod("a.go", "\n")}},
		"odd marker":         {Files: []codehost.FileDiff{mod("a.go", base+"\\ whatever\n")}},
		"marker first":       {Files: []codehost.FileDiff{mod("a.go", "@@ -1 +1 @@\n\\ No newline at end of file\n-a\n+b\n")}},
		"blank line in hunk": {Files: []codehost.FileDiff{mod("a.go", "@@ -1,2 +1,2 @@\n a\n\n")}},
	}
	for name, d := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := strategy.Fingerprint(d)
			require.ErrorIs(t, err, codehost.ErrDiffUnsupported)
			assert.Empty(t, got)
		})
	}
}

func TestDiff_ContainsTipOnlyWhenTheMergeBaseIsTheTip(t *testing.T) {
	assert.True(t, (&codehost.Diff{BaseSHA: "a", MergeBaseSHA: "a"}).ContainsTip())
	assert.False(t, (&codehost.Diff{BaseSHA: "a", MergeBaseSHA: "b"}).ContainsTip())
	assert.False(t, (&codehost.Diff{}).ContainsTip(), "unknown is not contained")
}

func TestFingerprint_AcceptsRemovedFilesAndTheirPatchMatters(t *testing.T) {
	removed := func(patch string) codehost.FileDiff {
		return codehost.FileDiff{Path: "gone.go", Status: codehost.DiffRemoved, Patch: patch}
	}
	ref := fp(t, removed("@@ -1,2 +0,0 @@\n-a\n-b\n"))
	assert.NotEqual(t, ref, fp(t, removed("@@ -1,2 +0,0 @@\n-a\n-B\n")))
	assert.NotEqual(t, ref, fp(t, mod("gone.go", "@@ -1,2 +0,0 @@\n-a\n-b\n")))
}

func TestChangedLines_AreTheAddedAndRemovedLinesWithoutMarkers(t *testing.T) {
	got, err := codehost.ChangedLines("@@ -3,3 +3,3 @@ images:\n   - name: api\n-    newTag: old\n+    newTag: new\n   - name: web\n@@ -9 +9,2 @@\n x\n+\\ not a marker\n\\ No newline at end of file")

	require.NoError(t, err)
	assert.Equal(t, []string{"    newTag: old", "    newTag: new", "\\ not a marker"}, got)
}

// A patch the host did not give, or one that does not parse, is never read
// as one that changes no lines.
func TestChangedLines_FailClosed(t *testing.T) {
	for name, patch := range map[string]string{
		"no patch":        "",
		"no hunk header":  "-a\n+b",
		"short of counts": "@@ -1,2 +1,2 @@\n-a\n+b",
	} {
		t.Run(name, func(t *testing.T) {
			got, err := codehost.ChangedLines(patch)
			require.ErrorIs(t, err, codehost.ErrDiffUnsupported)
			assert.Nil(t, got)
		})
	}
}
