// Package gosrc locates a Go module's own source: its import path, its
// packages' names relative to it, and which of its files are generated.
package gosrc

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/mod/modfile"
)

// Module is one Go module on disk.
type Module struct {
	Dir  string
	Path string

	mu        sync.Mutex
	generated map[string]bool
	nested    map[string]bool
}

// LoadModule reads dir/go.mod.
func LoadModule(dir string) (*Module, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(filepath.Join(abs, "go.mod")) // #nosec G304 -- the module the caller names
	if err != nil {
		return nil, err
	}
	modPath := modfile.ModulePath(data)
	if modPath == "" {
		return nil, fmt.Errorf("%s/go.mod: no module line", abs)
	}
	return &Module{Dir: abs, Path: modPath, generated: map[string]bool{}, nested: map[string]bool{}}, nil
}

// RelFile maps an import-path file name, as cover profiles name files, to
// a slash path relative to the module. ok is false for a file of another
// module, whose coverage is not this module's to ratchet: a sibling's, or a
// nested module's, whose import path starts with this one's.
func (m *Module) RelFile(importFile string) (rel string, ok bool) {
	rel, ok = strings.CutPrefix(importFile, m.Path+"/")
	if !ok || m.inNestedModule(path.Dir(rel)) {
		return "", false
	}
	return rel, true
}

// inNestedModule reports whether a module-relative directory is in a
// module of its own below this one: a go.mod in it or in a parent short of
// the root.
func (m *Module) inNestedModule(relDir string) bool {
	if relDir == "." || relDir == "/" || relDir == "" {
		return false
	}
	m.mu.Lock()
	got, ok := m.nested[relDir]
	m.mu.Unlock()
	if ok {
		return got
	}
	_, err := os.Stat(filepath.Join(m.Dir, filepath.FromSlash(relDir), "go.mod"))
	got = err == nil || m.inNestedModule(path.Dir(relDir))
	m.mu.Lock()
	m.nested[relDir] = got
	m.mu.Unlock()
	return got
}

// SourceFiles is the module's own non-test, non-generated Go files,
// module-relative, by directory. testdata, vendor, directories starting "."
// or "_", and nested modules (which measure themselves) are left out.
func (m *Module) SourceFiles() (map[string][]string, error) {
	byDir := map[string][]string{}
	err := filepath.WalkDir(m.Dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return m.skipDir(p, d)
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		rel, err := filepath.Rel(m.Dir, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		gen, err := m.IsGenerated(rel)
		if err != nil || gen {
			return err
		}
		dir := path.Dir(rel)
		byDir[dir] = append(byDir[dir], rel)
		return nil
	})
	return byDir, err
}

func (m *Module) skipDir(p string, d fs.DirEntry) error {
	if p == m.Dir {
		return nil
	}
	name := d.Name()
	if name == "testdata" || name == "vendor" || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") {
		return filepath.SkipDir
	}
	if _, err := os.Stat(filepath.Join(p, "go.mod")); err == nil {
		return filepath.SkipDir
	}
	return nil
}

// Package names a package by its directory relative to the module, as
// `go test` takes it: "." for the root, "./internal/x" otherwise. A
// relative name keeps baselines short and can't be mistaken for a key like
// "total".
func Package(relDir string) string {
	relDir = path.Clean(filepath.ToSlash(relDir))
	if relDir == "." {
		return "."
	}
	return "./" + relDir
}

// PackageOf names the package a module-relative file is in.
func PackageOf(relFile string) string {
	return Package(path.Dir(filepath.ToSlash(relFile)))
}

// PackageOfImport names an import path's package relative to the module,
// or returns ok false for another module's.
func (m *Module) PackageOfImport(importPath string) (string, bool) {
	if importPath == m.Path {
		return ".", true
	}
	rel, ok := strings.CutPrefix(importPath, m.Path+"/")
	if !ok {
		return "", false
	}
	return Package(rel), true
}

// IsGenerated reports whether a module-relative file carries the standard
// "Code generated ... DO NOT EDIT." header. A file that no longer exists
// (a profile older than the tree) counts as hand-written, so its coverage
// stays visible rather than silently dropping out.
func (m *Module) IsGenerated(relFile string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if g, ok := m.generated[relFile]; ok {
		return g, nil
	}
	g, err := IsGeneratedFile(filepath.Join(m.Dir, filepath.FromSlash(relFile)))
	if os.IsNotExist(err) {
		g, err = false, nil
	}
	if err != nil {
		return false, err
	}
	m.generated[relFile] = g
	return g, nil
}

// IsGeneratedFile reports whether the Go file at p is generated. Only its
// package clause is parsed: the header must come before it.
func IsGeneratedFile(p string) (bool, error) {
	f, err := parser.ParseFile(token.NewFileSet(), p, nil, parser.PackageClauseOnly|parser.ParseComments)
	if err != nil {
		if _, statErr := os.Stat(p); statErr != nil {
			return false, statErr
		}
		return false, err
	}
	return ast.IsGenerated(f), nil
}
