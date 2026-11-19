package fingerprint

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-review/report"
)

func write(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, rel)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
}

// checkout makes a reviewer source tree with its personas, in a fresh
// directory: a different one each call, as two checkouts are.
func checkout(t *testing.T) (src, personas string) {
	t.Helper()
	src = t.TempDir()
	write(t, src, "go.mod", "module x\n")
	write(t, src, "cmd/slop-review/main.go", "package main\n")
	write(t, src, "internal/orchestrator/seams.md", "seams\n")
	write(t, src, "internal/orchestrator/prompt.go", "package orchestrator\n")
	personas = filepath.Join(src, "personas")
	write(t, src, "personas/standard.md", "standard\n")
	write(t, src, "personas/security.md", "security\n")
	return src, personas
}

func compute(t *testing.T, src, personas string) *Result {
	t.Helper()
	res, err := Compute(src, os.DirFS(personas))
	require.NoError(t, err)
	return res
}

func TestStableAcrossCheckouts(t *testing.T) {
	srcA, personasA := checkout(t)
	srcB, personasB := checkout(t)
	a := compute(t, srcA, personasA)
	b := compute(t, srcB, personasB)
	assert.NotEmpty(t, a.Run)
	assert.Equal(t, a, b)
}

func TestPersonaChangeChangesOnlyThatPersonaAndRun(t *testing.T) {
	src, personas := checkout(t)
	before := compute(t, src, personas)
	write(t, src, "personas/security.md", "security, reworded\n")
	after := compute(t, src, personas)

	assert.Equal(t, before.Personas["standard"], after.Personas["standard"])
	assert.NotEqual(t, before.Personas["security"].Hash, after.Personas["security"].Hash)
	assert.Equal(t, "security, reworded\n", after.Personas["security"].Text)
	assert.Equal(t, before.Source, after.Source, "personas are not part of the source hash")
	assert.NotEqual(t, before.Run, after.Run)
}

func TestSourceInputsChangeRunAndSource(t *testing.T) {
	for name, edit := range map[string]func(t *testing.T, src string){
		"go source": func(t *testing.T, src string) {
			write(t, src, "internal/orchestrator/prompt.go", "package orchestrator // v2\n")
		},
		"embedded md": func(t *testing.T, src string) { write(t, src, "internal/orchestrator/seams.md", "other seams\n") },
		"go.mod":      func(t *testing.T, src string) { write(t, src, "go.mod", "module y\n") },
		"a new file":  func(t *testing.T, src string) { write(t, src, "internal/new/new.go", "package new\n") },
		"a renamed file": func(t *testing.T, src string) {
			require.NoError(t, os.Rename(filepath.Join(src, "internal/orchestrator/prompt.go"), filepath.Join(src, "internal/orchestrator/prompts.go")))
		},
	} {
		t.Run(name, func(t *testing.T) {
			src, personas := checkout(t)
			before := compute(t, src, personas)
			edit(t, src)
			after := compute(t, src, personas)
			assert.NotEqual(t, before.Source, after.Source)
			assert.NotEqual(t, before.Run, after.Run)
			assert.Equal(t, before.Personas, after.Personas)
		})
	}
}

func TestIgnoredFilesDoNotChangeFingerprint(t *testing.T) {
	src, personas := checkout(t)
	before := compute(t, src, personas)
	write(t, src, "internal/orchestrator/prompt_test.go", "package orchestrator\n")
	write(t, src, "internal/x/testdata/case.go", "package x\n")
	write(t, src, "bench/cases/one.md", "case\n")
	write(t, src, "review.sh", "#!/bin/sh\n")
	write(t, src, ".cache/x.go", "package x\n")
	assert.Equal(t, before, compute(t, src, personas))
}

func TestCRLFIsTheSamePersona(t *testing.T) {
	src, personas := checkout(t)
	before := compute(t, src, personas)
	write(t, src, "personas/standard.md", "standard\r\n")
	assert.Equal(t, before, compute(t, src, personas))
}

func TestFieldBoundariesAreUnambiguous(t *testing.T) {
	encode := func(name, value string) string {
		var b strings.Builder
		writeField(&b, name, value)
		return b.String()
	}
	assert.NotEqual(t, encode("a", "bc"), encode("ab", "c"))
}

func TestNoSourceDirSkipsSourceAndRun(t *testing.T) {
	_, personas := checkout(t)
	res := compute(t, "", personas)
	assert.Empty(t, res.Source)
	assert.Empty(t, res.Run)
	assert.Len(t, res.Personas, 2)
}

func TestEmptySourceIsAnError(t *testing.T) {
	_, personas := checkout(t)
	_, err := Compute(t.TempDir(), os.DirFS(personas))
	assert.Error(t, err)
}

func TestComputeBundledDependsOnVersionAndPersonas(t *testing.T) {
	_, personas := checkout(t)
	fsys := os.DirFS(personas)

	a, err := ComputeBundled("v1", fsys)
	require.NoError(t, err)
	b, err := ComputeBundled("v1", fsys)
	require.NoError(t, err)
	assert.Equal(t, a, b)
	assert.True(t, report.IsHash(a.Run))
	assert.True(t, report.IsHash(a.Source))
	assert.Len(t, a.Personas, 2)

	c, err := ComputeBundled("v2", fsys)
	require.NoError(t, err)
	assert.NotEqual(t, a.Source, c.Source)
	assert.NotEqual(t, a.Run, c.Run)
	assert.Equal(t, a.Personas, c.Personas)
}
