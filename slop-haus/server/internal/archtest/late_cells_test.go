package archtest

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const lateCellsDir = "../../cmd/api"

func render(t *testing.T, fset *token.FileSet, n ast.Node) string {
	t.Helper()
	var b bytes.Buffer
	require.NoError(t, printer.Fprint(&b, fset, n))
	return b.String()
}

// lateCells returns "type | target" for every newLate[T](...) call under dir,
// where target is what the cell is assigned to: a variable or field, or the
// key of a composite literal.
func lateCells(t *testing.T, dir string) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "*.go"))
	require.NoError(t, err)
	seen := map[string]bool{}
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, 0)
		require.NoError(t, err)
		targets := map[*ast.CallExpr]string{}
		note := func(rhs ast.Expr, target string) {
			if call, ok := rhs.(*ast.CallExpr); ok {
				targets[call] = target
			}
		}
		ast.Inspect(file, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.AssignStmt:
				if len(n.Lhs) == len(n.Rhs) {
					for i := range n.Rhs {
						note(n.Rhs[i], render(t, fset, n.Lhs[i]))
					}
				}
			case *ast.KeyValueExpr:
				note(n.Value, render(t, fset, n.Key))
			case *ast.ValueSpec:
				for i, v := range n.Values {
					if i < len(n.Names) {
						note(v, n.Names[i].Name)
					}
				}
			}
			return true
		})
		// A cell made anywhere else (returned, passed as an argument) is named
		// by its function, so it still needs an entry rather than slipping by.
		for _, decl := range file.Decls {
			fn := "package level"
			if f, ok := decl.(*ast.FuncDecl); ok {
				fn = "func " + f.Name.Name
			}
			ast.Inspect(decl, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				idx, ok := call.Fun.(*ast.IndexExpr)
				if !ok {
					return true
				}
				if id, ok := idx.X.(*ast.Ident); !ok || id.Name != "newLate" {
					return true
				}
				target, ok := targets[call]
				if !ok {
					target = "(" + fn + ")"
				}
				seen[render(t, fset, idx.Index)+" | "+target] = true
				return true
			})
		}
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func parseLateAllowlist(t *testing.T, path string) map[string]string {
	t.Helper()
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	known := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "|", 3)
		require.Len(t, parts, 3, "allowlist line needs <type> | <target> | <reason>: %q", line)
		reason := strings.TrimSpace(parts[2])
		require.NotEmpty(t, reason, "allowlist entry needs a reason: %q", line)
		known[strings.TrimSpace(parts[0])+" | "+strings.TrimSpace(parts[1])] = reason
	}
	return known
}

func checkLateCells(found []string, known map[string]string) []string {
	fresh, stale := compare(found, known)
	var problems []string
	for _, f := range fresh {
		problems = append(problems, fmt.Sprintf("new newLate cell %q is not in late_cells_allowlist.txt: each is a runtime dependency cycle the compiler cannot see; remove the cycle, or add a reviewed entry with a one-line reason", f))
	}
	for _, s := range stale {
		problems = append(problems, fmt.Sprintf("allowlisted newLate cell %q no longer exists: remove it from late_cells_allowlist.txt", s))
	}
	return problems
}

func TestLateCellsAreAllowlisted(t *testing.T) {
	problems := checkLateCells(lateCells(t, lateCellsDir), parseLateAllowlist(t, "late_cells_allowlist.txt"))
	assert.Empty(t, problems)
}

func TestTheLateCellGuardSeesChanges(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a.go"), []byte(`package main
func f() {
	c.x = newLate[pkg.T](cells, "x")
	_ = &u{k: newLate[pkg.U](cells, "k")}
	other[int](1)
}
func g() *Late[pkg.R] { return newLate[pkg.R](cells, "r") }
func h() { use(newLate[pkg.A](cells, "a")) }
`), 0o644))
	found := lateCells(t, dir)
	assert.Equal(t, []string{"pkg.A | (func h)", "pkg.R | (func g)", "pkg.T | c.x", "pkg.U | k"}, found)

	known := map[string]string{"pkg.A | (func h)": "r", "pkg.R | (func g)": "r", "pkg.T | c.x": "r", "pkg.U | k": "r"}
	assert.Empty(t, checkLateCells(found, known))

	assert.Len(t, checkLateCells(found, map[string]string{"pkg.T | c.x": "r"}), 3, "unlisted cell")
	known["gone.V | y"] = "r"
	assert.Len(t, checkLateCells(found, known), 1, "stale entry")
}
