package archtest

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// knownHandlerPolicy lists handlers that still refuse a caller themselves.
// Decision 0003 puts authorization policy in services: a handler maps the
// service's refusal (an error table entry) and does not make one, or a
// second transport can skip the check.
var knownHandlerPolicy = map[string]string{}

// refusesInHandler reports each handler file under root that answers 403
// itself.
func refusesInHandler(root string) ([]string, error) {
	seen := map[string]bool{}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		name := info.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") || !strings.Contains(name, "handler") {
			return nil
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(file, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if ok && (sel.Sel.Name == "Error403Forbidden" || sel.Sel.Name == "StatusForbidden") {
				r, _ := filepath.Rel(root, path)
				seen[filepath.ToSlash(r)] = true
			}
			return true
		})
		return nil
	})
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out, err
}

func TestHandlersDoNotDecideWhoMayDoWhat(t *testing.T) {
	found, err := refusesInHandler("..")
	require.NoError(t, err)
	fresh, stale := compare(found, knownHandlerPolicy)
	assert.Empty(t, fresh, "a handler answers 403 itself: have the service take the caller and return a sentinel the handler's error table maps (decision 0003)")
	assert.Empty(t, stale, "fixed: remove from knownHandlerPolicy")
}

func TestTheHandlerPolicyGuardSeesARefusal(t *testing.T) {
	dir := t.TempDir()
	put := func(name, body string) {
		p := filepath.Join(dir, name)
		require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
		require.NoError(t, os.WriteFile(p, []byte(body), 0o644))
	}
	put("a/handler.go", "package a\nfunc f() error { return huma.Error403Forbidden(\"no\") }")
	put("b/role_handler.go", "package b\nvar s = http.StatusForbidden")
	put("c/handler.go", "package c\nfunc f() error { return huma.Error404NotFound(\"no\") }")
	put("d/handler_test.go", "package d\nvar s = http.StatusForbidden")
	put("e/service.go", "package e\nvar s = http.StatusForbidden")
	got, err := refusesInHandler(dir)
	require.NoError(t, err)
	assert.Equal(t, []string{"a/handler.go", "b/role_handler.go"}, got, fmt.Sprint(got))
}
