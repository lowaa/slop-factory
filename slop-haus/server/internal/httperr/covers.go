package httperr

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// RequireCovered fails unless every exported Err* variable declared in the
// package whose test calls it (the working directory) is both listed in
// sentinels and answered by the table, so a new sentinel cannot fall through
// to a 500 unnoticed. exempt names sentinels that are deliberately not
// answered over HTTP, with the reason.
func RequireCovered(t *testing.T, table *Table, sentinels map[string]error, exempt map[string]string) {
	t.Helper()
	declared := declaredSentinels(t)
	for _, name := range declared {
		err, listed := sentinels[name]
		if !listed {
			if _, ok := exempt[name]; ok {
				continue
			}
			t.Errorf("%s is declared but neither listed in the test's sentinels nor exempt", name)
			continue
		}
		if _, ok := exempt[name]; ok {
			continue
		}
		if !table.Covers(err) {
			t.Errorf("%s has no rule in the error table: it would answer 500", name)
		}
	}
	for name := range sentinels {
		if !slices.Contains(declared, name) {
			t.Errorf("%s is listed but not declared in the package", name)
		}
	}
}

func declaredSentinels(t *testing.T) []string {
	t.Helper()
	notTest := func(fi fs.FileInfo) bool { return !strings.HasSuffix(fi.Name(), "_test.go") }
	pkgs, err := parser.ParseDir(token.NewFileSet(), ".", notTest, 0)
	require.NoError(t, err)
	var names []string
	for _, pkg := range pkgs {
		for _, f := range pkg.Files {
			for _, d := range f.Decls {
				gd, ok := d.(*ast.GenDecl)
				if !ok || gd.Tok != token.VAR {
					continue
				}
				for _, spec := range gd.Specs {
					for _, id := range spec.(*ast.ValueSpec).Names {
						if strings.HasPrefix(id.Name, "Err") && id.IsExported() {
							names = append(names, id.Name)
						}
					}
				}
			}
		}
	}
	return names
}
