package service_test

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-bot/internal/paths"
	"github.com/lowaa/slop-factory/slop-bot/internal/service"
	"github.com/lowaa/slop-factory/slop-bot/internal/service/servicetest"
)

const testUID = 1000

// fixture is a Manager over a temp home and a fake runner. Everything it
// writes is under root.
type fixture struct {
	root, home string
	runner     *servicetest.Runner
	opts       *service.Options
	mgr        service.Manager
}

type fixtureOpts struct {
	goos     string
	homeName string
	// vars are extra environment variables; XDG ones are relative to home
	// when they start with "~/".
	vars map[string]string
	// binary replaces the default binary path.
	binary string
	// realBinary creates the binary as an executable file.
	realBinary bool
	// extraPATH is appended to the default install-time PATH.
	extraPATH string
}

func newFixture(t *testing.T, fo *fixtureOpts) *fixture {
	t.Helper()
	root := t.TempDir()
	name := fo.homeName
	if name == "" {
		name = "ada"
	}
	home := filepath.Join(root, name)
	require.NoError(t, os.MkdirAll(home, 0o700))
	vars := map[string]string{}
	switch fo.goos {
	case "darwin":
		vars["TMPDIR"] = filepath.Join(home, "tmp")
	default:
		vars["XDG_RUNTIME_DIR"] = "/run/user/1000"
	}
	for k, v := range fo.vars {
		if rest, ok := strings.CutPrefix(v, "~/"); ok {
			v = filepath.Join(home, rest)
		}
		vars[k] = v
	}
	p, err := paths.Resolve(&paths.Env{
		Lookup: func(k string) (string, bool) { v, ok := vars[k]; return v, ok },
		Home:   home, UID: testUID, GOOS: fo.goos,
	})
	require.NoError(t, err)
	binary := filepath.Join(home, "go", "bin", "slop-bot")
	if fo.binary != "" {
		binary = fo.binary
	}
	if fo.realBinary {
		require.NoError(t, os.MkdirAll(filepath.Dir(binary), 0o700))
		require.NoError(t, os.WriteFile(binary, []byte("#!/bin/sh\n"), 0o700)) // #nosec G306 -- a test stand-in that must be executable
	}
	pathVar := strings.Join([]string{
		filepath.Join(home, ".local", "bin"), filepath.Join(home, "go", "bin"),
		"/usr/local/bin", "/usr/bin", "/bin",
	}, ":") + fo.extraPATH
	f := &fixture{root: root, home: home, runner: servicetest.NewRunner()}
	f.opts = &service.Options{
		Paths: p, Home: home, UID: testUID, GOOS: fo.goos,
		Binary: binary, PATH: pathVar,
		Runner: f.runner, Logger: slog.New(slog.DiscardHandler),
	}
	f.mgr, err = service.New(f.opts)
	require.NoError(t, err)
	return f
}

// stable swaps the temp root for a fixed one, so rendered files compare
// against golden copies.
func (f *fixture) stable(s string) string {
	fixed := "/home"
	if f.opts.GOOS == "darwin" {
		fixed = "/Users"
	}
	return strings.ReplaceAll(s, f.root, fixed)
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path) // #nosec G304 -- a path under the test's temp dir
	require.NoError(t, err)
	return string(b)
}
