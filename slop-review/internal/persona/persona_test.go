package persona

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "standard.md"), []byte("You are a standard reviewer.\n"), 0o644))

	p, err := Load(dir, "standard")
	require.NoError(t, err)
	assert.Equal(t, "standard", p.Name)
	assert.Equal(t, "You are a standard reviewer.\n", p.Prompt)
	assert.Equal(t, ChunkByPackage, p.Chunking, "package chunking is the default")
}

func TestLoad_FrontMatterSetsChunkingAndIsStripped(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "security.md"), []byte("---\nchunking: whole\n---\n\nYou are a security reviewer.\n"), 0o644))

	p, err := Load(dir, "security")
	require.NoError(t, err)
	assert.Equal(t, ChunkWhole, p.Chunking)
	assert.Equal(t, "You are a security reviewer.\n", p.Prompt)
}

func TestLoad_FrontMatterRejectsUnknownKeysAndValues(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.md"), []byte("---\nchunking: sometimes\n---\nx"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "b.md"), []byte("---\nmodel: gpt\n---\nx"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "c.md"), []byte("---\nchunking: whole\nx"), 0o644))

	_, err := Load(dir, "a")
	assert.ErrorContains(t, err, "chunking")
	_, err = Load(dir, "b")
	assert.ErrorContains(t, err, "unknown front matter key")
	_, err = Load(dir, "c")
	assert.ErrorContains(t, err, "not closed")
}

func TestLoadMany(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"security", "standard", "test-efficacy"} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, n+".md"), []byte(n), 0o644))
	}

	all, err := LoadMany(dir, "all")
	require.NoError(t, err)
	assert.Equal(t, []string{"security", "standard", "test-efficacy"}, names(all))

	some, err := LoadMany(dir, "standard, security,standard")
	require.NoError(t, err)
	assert.Equal(t, []string{"standard", "security"}, names(some), "listed order, duplicates dropped")

	_, err = LoadMany(dir, " , ")
	assert.Error(t, err)
	_, err = LoadMany(dir, "standard,missing")
	assert.Error(t, err)
}

func names(ps []*Persona) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.Name
	}
	return out
}

func TestLoad_UnknownPersona(t *testing.T) {
	dir := t.TempDir()

	_, err := Load(dir, "does-not-exist")
	assert.Error(t, err)
}

func TestLoad_RejectsPathTraversal(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(outside, "secret.md"), []byte("leaked"), 0o644))

	_, err := Load(dir, "../"+filepath.Base(outside)+"/secret")
	assert.ErrorContains(t, err, "invalid persona name")
}

// TestLoadMany_ShippedLibrary loads the actual persona library this repo
// ships (personas/*.md, checked in above internal/persona), not a synthetic
// t.TempDir() fixture -- every other test here only proves the loader works
// on data it invented itself, which says nothing about whether the real
// files parse, or still say what the front-matter/chunking contract expects.
func TestLoadMany_ShippedLibrary(t *testing.T) {
	dir := "../../personas"

	all, err := LoadMany(dir, AllPersonas)
	require.NoError(t, err)
	require.Equal(t, []string{"blast-radius", "concurrency", "security", "standard", "test-efficacy"}, names(all))

	byName := map[string]*Persona{}
	for _, p := range all {
		byName[p.Name] = p
	}

	assert.Equal(t, ChunkWhole, byName["security"].Chunking, "security's cross-cutting question is lost if split per package")
	for _, n := range []string{"concurrency", "standard", "test-efficacy"} {
		assert.Equal(t, ChunkByPackage, byName[n].Chunking, "%s should default to per-package chunking", n)
	}

	assert.Equal(t, StageOverview, byName["blast-radius"].Stage)
	assert.Equal(t, ChunkWhole, byName["blast-radius"].Chunking)
	for _, n := range []string{"concurrency", "security", "standard", "test-efficacy"} {
		assert.Equal(t, StageReview, byName[n].Stage, "%s runs at the review stage", n)
	}

	for _, p := range all {
		assert.False(t, strings.HasPrefix(p.Prompt, "---"), "%s: front matter must be stripped from the prompt", p.Name)
		assert.NotContains(t, p.Prompt, "stage:", "%s: front matter must be stripped from the prompt", p.Name)
		assert.NotContains(t, p.Prompt, "chunking:", "%s: front matter must be stripped from the prompt", p.Name)
		assert.Contains(t, p.Prompt, "get_diff", "%s: prompt should tell the model about get_diff", p.Name)
		assert.Contains(t, p.Prompt, "record_finding", "%s: prompt should tell the model about record_finding", p.Name)
		assert.Contains(t, p.Prompt, "submit_review", "%s: prompt should tell the model about submit_review", p.Name)
	}
}

func TestList(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "standard.md"), []byte("a"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "strict.md"), []byte("b"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("c"), 0o644))

	names, err := List(dir)
	require.NoError(t, err)
	assert.Equal(t, []string{"standard", "strict"}, names)
}

func TestLoad_FrontMatterWithCRLFLineEndings(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "security.md"), []byte("---\r\nchunking: whole\r\n---\r\n\r\nYou are a security reviewer.\r\n"), 0o644))

	p, err := Load(dir, "security")
	require.NoError(t, err)
	assert.Equal(t, ChunkWhole, p.Chunking)
	assert.Equal(t, "You are a security reviewer.\n", p.Prompt)
}

func TestLoad_FrontMatterSetsStage(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "o.md"), []byte("---\nstage: overview\n---\nx"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "d.md"), []byte("x"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "bad.md"), []byte("---\nstage: later\n---\nx"), 0o644))

	o, err := Load(dir, "o")
	require.NoError(t, err)
	assert.Equal(t, StageOverview, o.Stage)
	assert.Equal(t, ChunkByPackage, o.Chunking, "stage and chunking are independent")
	d, err := Load(dir, "d")
	require.NoError(t, err)
	assert.Equal(t, StageReview, d.Stage, "review is the default")
	_, err = Load(dir, "bad")
	assert.ErrorContains(t, err, "stage")
}

func TestLoadOverviews(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.md"), []byte("---\nstage: overview\n---\nx"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "b.md"), []byte("x"), 0o644))

	got, err := LoadOverviews(dir)
	require.NoError(t, err)
	assert.Equal(t, []string{"a"}, names(got))
}
