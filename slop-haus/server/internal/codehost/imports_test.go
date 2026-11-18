package codehost_test

import (
	"encoding/json"
	"io"
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const modulePath = "github.com/lowaa/slop-factory/slop-haus/server"

// The concrete host implementations are named by the wiring alone; every
// other package relies on the codehost interfaces, so a second host needs
// no change outside codehost.
var concrete = []string{modulePath + "/internal/codehost/github", modulePath + "/internal/codehost/githubapp"}

// allowed are the packages that may import a concrete host, with why.
var allowed = map[string]string{
	modulePath + "/cmd/api": "builds the hosts and credentials",
	// Registers the credential routes, and builds the default handlers for
	// the OpenAPI spec.
	modulePath + "/internal/apiserver": "route wiring",
}

// allowedInTests are the packages whose tests may import a concrete host.
var allowedInTests = map[string]string{
	// Runs GitHub's real payloads through the webhook door, which is
	// GitHub's own transport.
	modulePath + "/internal/githubhook": "real payloads through the door",
}

type listed struct {
	ImportPath   string
	Imports      []string
	TestImports  []string
	XTestImports []string
}

func underCodehost(path string) bool {
	root := modulePath + "/internal/codehost"
	return path == root || strings.HasPrefix(path, root+"/")
}

func isConcrete(path string) bool {
	for _, c := range concrete {
		if path == c {
			return true
		}
	}
	return false
}

// violations is what imports a concrete host that may not. Direct imports
// are enough: whatever imports a package that imports one is itself
// checked, and is a direct importer of something it may not name.
func violations(pkgs []listed) []string {
	var out []string
	for _, p := range pkgs {
		if underCodehost(p.ImportPath) {
			continue
		}
		_, wiring := allowed[p.ImportPath]
		_, testing := allowedInTests[p.ImportPath]
		if !wiring {
			for _, imp := range p.Imports {
				if isConcrete(imp) {
					out = append(out, p.ImportPath+" imports "+imp)
				}
			}
		}
		if !wiring && !testing {
			for _, imp := range append(append([]string(nil), p.TestImports...), p.XTestImports...) {
				if isConcrete(imp) {
					out = append(out, p.ImportPath+" tests import "+imp)
				}
			}
		}
	}
	return out
}

func listModule(t *testing.T) []listed {
	t.Helper()
	cmd := exec.Command("go", "list", "-json=ImportPath,Imports,TestImports,XTestImports", "./...")
	cmd.Dir = "../.."
	stdout, err := cmd.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, cmd.Start())
	var pkgs []listed
	dec := json.NewDecoder(stdout)
	for {
		var p listed
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

func TestOnlyTheWiringImportsAConcreteHost(t *testing.T) {
	assert.Empty(t, violations(listModule(t)))
}

// The check must see an offender, in code and in tests, and let the allowed
// through: a list that is empty for the wrong reason proves nothing.
func TestTheImportCheckSeesAnOffender(t *testing.T) {
	gh := modulePath + "/internal/codehost/github"
	pkgs := []listed{
		{ImportPath: modulePath + "/internal/ticket", Imports: []string{gh}},
		{ImportPath: modulePath + "/internal/manifest", XTestImports: []string{modulePath + "/internal/codehost/githubapp"}},
		{ImportPath: modulePath + "/internal/agent", TestImports: []string{gh}},
		{ImportPath: modulePath + "/cmd/api", Imports: []string{gh}, TestImports: []string{gh}},
		{ImportPath: modulePath + "/internal/githubhook", TestImports: []string{gh}},
		{ImportPath: modulePath + "/internal/githubhook", Imports: []string{gh}},
		{ImportPath: modulePath + "/internal/codehost/githubapp", Imports: []string{gh}},
	}
	assert.Equal(t, []string{
		modulePath + "/internal/ticket imports " + gh,
		modulePath + "/internal/manifest tests import " + modulePath + "/internal/codehost/githubapp",
		modulePath + "/internal/agent tests import " + gh,
		modulePath + "/internal/githubhook imports " + gh,
	}, violations(pkgs))
}
