// Package archtest guards the layering of the server packages.
//
// The layers, lowest first; a package may import its own layer or a lower one:
//
//	kernel    db, authn, audit, tenantstate, secretbox, logging, metrics
//	store     SQL behind a Store interface (PostgresStore, pgx)
//	service   rules, declared beside the Store they need
//	transport apiserver, mcpserver, hookserver, githubhook (HTTP, huma, MCP)
//	cmd       wiring; the only importer of apiserver
//
// Packages here are grouped by feature, so one package holds a store, a
// service and a handler together. The rules below are therefore the edges that
// can be checked mechanically: a store file does not reach for HTTP, config
// names no domain package, the kernel names nothing above itself, and only cmd
// imports apiserver. codehost/imports_test.go holds the rule that only the
// wiring imports a concrete code host.
//
// Today's violations are in knownViolations, each with its reason. A new
// violation fails the test, and so does a listed one that no longer exists, so
// the list only shrinks.
package archtest

import (
	"encoding/json"
	"fmt"
	"go/parser"
	"go/token"
	"io"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const modulePath = "github.com/lowaa/slop-factory/slop-haus/server"

var kernel = map[string]bool{}

func init() {
	for _, k := range []string{"db", "authn", "audit", "tenantstate", "secretbox", "logging", "metrics"} {
		kernel[modulePath+"/internal/"+k] = true
	}
}

// forbiddenEdges are imports that run against the layering although they are
// not covered by a general rule: the importer should depend on an interface it
// owns, not on the other package.
var forbiddenEdges = map[string][]string{
	"internal/prune":     {"internal/conductor", "internal/pendingaction"},
	"internal/workspace": {"internal/steward"},
	"internal/agent":     {"internal/oauth"},
}

// knownViolations maps "rule: importer -> target" to why it is tolerated.
var knownViolations = map[string]string{
	"config-no-domain: internal/config -> internal/change":                                   "change.Config is embedded in the server config",
	"config-no-domain: internal/config -> internal/pendingaction":                            "pendingaction.Config is embedded in the server config",
	"config-no-domain: internal/config -> internal/prune":                                    "prune's retention config is embedded in the server config",
	"config-no-domain: internal/config -> internal/ticket":                                   "ticket.DefaultBulkMax and ticket.CooldownPolicy are used as config defaults",
	"forbidden-edge: internal/prune -> internal/pendingaction":                               "prune reuses the pendingaction concrete types",
	"forbidden-edge: internal/workspace -> internal/steward":                                 "workspace names the steward's concrete types",
	"store-no-http: internal/ticketusage/ticketusage.go -> net/http":                         "store and handler share one file",
	"store-no-http: internal/ticketusage/ticketusage.go -> github.com/danielgtaylor/huma/v2": "store and handler share one file",
}

type pkg struct {
	ImportPath string
	Dir        string
	GoFiles    []string
	Imports    []string
}

func rel(path string) string { return strings.TrimPrefix(path, modulePath+"/") }

func isHTTP(imp string) bool {
	return imp == "net/http" || strings.HasPrefix(imp, "github.com/danielgtaylor/huma")
}

func isModule(imp string) bool { return strings.HasPrefix(imp, modulePath+"/") }

// fileImports reports the imports of one source file of the package.
type fileImports func(p *pkg, file string) (imports []string, declaresStore bool)

// check returns every rule violation as "rule: importer -> target".
func check(pkgs []pkg, files fileImports) []string {
	var out []string
	add := func(rule, from, to string) { out = append(out, fmt.Sprintf("%s: %s -> %s", rule, from, to)) }
	for _, p := range pkgs {
		from := rel(p.ImportPath)
		for _, imp := range p.Imports {
			if p.ImportPath == modulePath+"/internal/config" && isModule(imp) && !kernel[imp] {
				add("config-no-domain", from, rel(imp))
			}
			if kernel[p.ImportPath] && isModule(imp) && !kernel[imp] {
				add("kernel-imports-kernel", from, rel(imp))
			}
			if imp == modulePath+"/internal/apiserver" && !strings.HasPrefix(from, "cmd/") {
				add("apiserver-only-from-cmd", from, rel(imp))
			}
			for _, f := range forbiddenEdges[from] {
				if imp == modulePath+"/"+f {
					add("forbidden-edge", from, f)
				}
			}
		}
		if !strings.HasPrefix(from, "internal/") {
			continue
		}
		for _, f := range p.GoFiles {
			imps, declares := files(&p, f)
			isStore := declares
			for _, imp := range imps {
				isStore = isStore || strings.HasPrefix(imp, "github.com/jackc/pgx")
			}
			if !isStore {
				continue
			}
			for _, imp := range imps {
				if isHTTP(imp) {
					add("store-no-http", from+"/"+f, imp)
				}
			}
		}
	}
	sort.Strings(out)
	return out
}

// compare splits violations into new ones and stale allowlist entries.
func compare(found []string, known map[string]string) (fresh, stale []string) {
	seen := map[string]bool{}
	for _, v := range found {
		seen[v] = true
		if _, ok := known[v]; !ok {
			fresh = append(fresh, v)
		}
	}
	for k := range known {
		if !seen[k] {
			stale = append(stale, k)
		}
	}
	sort.Strings(stale)
	return fresh, stale
}

func listModule(t *testing.T) []pkg {
	t.Helper()
	cmd := exec.Command("go", "list", "-json=ImportPath,Dir,GoFiles,Imports", "./...")
	cmd.Dir = "../.."
	stdout, err := cmd.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, cmd.Start())
	var pkgs []pkg
	dec := json.NewDecoder(stdout)
	for {
		var p pkg
		if err := dec.Decode(&p); err == io.EOF {
			break
		}
		require.NoError(t, err)
		pkgs = append(pkgs, p)
	}
	require.NoError(t, cmd.Wait())
	require.NotEmpty(t, pkgs)
	return pkgs
}

func parseFileImports(t *testing.T) fileImports {
	return func(p *pkg, file string) ([]string, bool) {
		f, err := parser.ParseFile(token.NewFileSet(), filepath.Join(p.Dir, file), nil, 0)
		require.NoError(t, err)
		var imps []string
		for _, s := range f.Imports {
			path, err := strconv.Unquote(s.Path.Value)
			require.NoError(t, err)
			imps = append(imps, path)
		}
		declares := false
		for name, obj := range f.Scope.Objects {
			declares = declares || (obj.Kind.String() == "type" && strings.HasSuffix(name, "PostgresStore"))
		}
		return imps, declares
	}
}

func TestServerPackageLayering(t *testing.T) {
	fresh, stale := compare(check(listModule(t), parseFileImports(t)), knownViolations)
	assert.Empty(t, fresh, "new layering violation: fix the import, or (rarely) list it in knownViolations with a reason")
	assert.Empty(t, stale, "fixed: remove from knownViolations")
}

// The rules must see an offender and let the clean through: a list that is
// empty for the wrong reason proves nothing.
func TestTheRulesSeeAnOffender(t *testing.T) {
	m := func(s string) string { return modulePath + "/" + s }
	pkgs := []pkg{
		{ImportPath: m("internal/config"), Imports: []string{m("internal/ticket"), m("internal/secretbox"), "fmt"}},
		{ImportPath: m("internal/audit"), Imports: []string{m("internal/ticket"), m("internal/db")}},
		{ImportPath: m("internal/ticket"), Imports: []string{m("internal/apiserver")}, GoFiles: []string{"store.go", "handler.go", "mixed.go", "pgxhttp.go"}},
		{ImportPath: m("cmd/api"), Imports: []string{m("internal/apiserver")}},
		{ImportPath: m("internal/prune"), Imports: []string{m("internal/conductor"), m("internal/db")}},
	}
	files := map[string]struct {
		imps  []string
		store bool
	}{
		"store.go":   {[]string{"github.com/jackc/pgx/v5"}, false},
		"handler.go": {[]string{"net/http", "github.com/danielgtaylor/huma/v2"}, false},
		"mixed.go":   {[]string{"net/http"}, true},
		"pgxhttp.go": {[]string{"github.com/jackc/pgx/v5", "net/http"}, false},
	}
	got := check(pkgs, func(_ *pkg, f string) ([]string, bool) { return files[f].imps, files[f].store })
	assert.Equal(t, []string{
		"apiserver-only-from-cmd: internal/ticket -> internal/apiserver",
		"config-no-domain: internal/config -> internal/ticket",
		"forbidden-edge: internal/prune -> internal/conductor",
		"kernel-imports-kernel: internal/audit -> internal/ticket",
		"store-no-http: internal/ticket/mixed.go -> net/http",
		"store-no-http: internal/ticket/pgxhttp.go -> net/http",
	}, got)

	fresh, stale := compare([]string{"a", "b"}, map[string]string{"b": "x", "c": "y"})
	assert.Equal(t, []string{"a"}, fresh)
	assert.Equal(t, []string{"c"}, stale)
}
