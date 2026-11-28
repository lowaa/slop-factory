package gosrc_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/dev-tools/quality/internal/gosrc"
	"github.com/lowaa/slop-factory/dev-tools/quality/internal/testmod"
)

func TestLoadModuleReadsTheModulePath(t *testing.T) {
	t.Parallel()
	m, err := gosrc.LoadModule(testmod.Write(t, nil))
	require.NoError(t, err)
	assert.Equal(t, testmod.Path, m.Path)
}

func TestLoadModuleWithoutGoModFails(t *testing.T) {
	t.Parallel()
	_, err := gosrc.LoadModule(t.TempDir())
	assert.Error(t, err)
}

func TestLoadModuleWithoutModuleLineFails(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	testmod.WriteFiles(t, dir, map[string]string{"go.mod": "go 1.24\n"})
	_, err := gosrc.LoadModule(dir)
	assert.ErrorContains(t, err, "no module line")
}

func TestRelFileKeepsOnlyTheModulesOwnFiles(t *testing.T) {
	t.Parallel()
	m, err := gosrc.LoadModule(testmod.Write(t, nil))
	require.NoError(t, err)

	rel, ok := m.RelFile(testmod.Path + "/internal/x/x.go")
	assert.True(t, ok)
	assert.Equal(t, "internal/x/x.go", rel)

	// A sibling whose path merely starts the same is another module.
	_, ok = m.RelFile(testmod.Path + "er/x.go")
	assert.False(t, ok)
}

func TestPackageNamesAreModuleRelative(t *testing.T) {
	t.Parallel()
	assert.Equal(t, ".", gosrc.PackageOf("main.go"))
	assert.Equal(t, "./internal/x", gosrc.PackageOf("internal/x/x.go"))
	assert.Equal(t, "./cmd/a", gosrc.Package("cmd/a/"))

	m, err := gosrc.LoadModule(testmod.Write(t, nil))
	require.NoError(t, err)
	pkg, ok := m.PackageOfImport(testmod.Path)
	assert.True(t, ok)
	assert.Equal(t, ".", pkg)
	pkg, ok = m.PackageOfImport(testmod.Path + "/internal/x")
	assert.True(t, ok)
	assert.Equal(t, "./internal/x", pkg)
	_, ok = m.PackageOfImport("example.com/other")
	assert.False(t, ok)
}

func TestIsGenerated(t *testing.T) {
	t.Parallel()
	dir := testmod.Write(t, map[string]string{
		"gen.go":  testmod.Generated + "package p\n",
		"hand.go": "package p\n",
		// The marker only counts before the package clause.
		"late.go": "package p\n\n" + testmod.Generated,
	})
	m, err := gosrc.LoadModule(dir)
	require.NoError(t, err)

	for file, want := range map[string]bool{"gen.go": true, "hand.go": false, "late.go": false, "gone.go": false} {
		got, err := m.IsGenerated(file)
		require.NoError(t, err, file)
		assert.Equal(t, want, got, file)
	}
}
