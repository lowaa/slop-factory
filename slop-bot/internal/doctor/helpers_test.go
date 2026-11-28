package doctor_test

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-bot/internal/config"
	"github.com/lowaa/slop-factory/slop-bot/internal/doctor"
	"github.com/lowaa/slop-factory/slop-bot/internal/doctor/doctortest"
	"github.com/lowaa/slop-factory/slop-bot/internal/paths"
	"github.com/lowaa/slop-factory/slop-bot/internal/service"
	"github.com/lowaa/slop-factory/slop-bot/internal/service/servicetest"
)

const testUID = 1000

// fixture is a doctor over a temp home, a fake PATH and a fake service
// manager. Nothing it runs leaves the test.
type fixture struct {
	t          *testing.T
	root, home string
	goos       string
	vars       map[string]string
	cmds       *doctortest.Commands
	runner     *servicetest.Runner
	opts       *doctor.Options
}

func newFixture(t *testing.T, goos string) *fixture {
	t.Helper()
	root := t.TempDir()
	home := filepath.Join(root, "ada")
	require.NoError(t, os.MkdirAll(home, 0o700))
	// PATH is the one the fixture's service is installed with.
	f := &fixture{t: t, root: root, home: home, goos: goos, vars: map[string]string{"PATH": "/usr/bin"},
		cmds: doctortest.Healthy(filepath.Join(home, ".local", "bin")), runner: servicetest.NewRunner()}
	if goos == "darwin" {
		// Short and fixed: a Mac's own temp dir breaks the socket limit.
		f.vars["TMPDIR"] = "/tmp/sbt"
	} else {
		f.vars["XDG_RUNTIME_DIR"] = "/run/user/1000"
		f.runner.Answer("loginctl show-user 1000 -p Linger", &service.Result{Stdout: "Linger=yes\n"})
	}
	f.opts = &doctor.Options{GOOS: goos, Lookup: f.lookup, Environ: f.environ, Commands: f.cmds}
	f.resolve()
	f.config("")
	return f
}

func (f *fixture) lookup(k string) (string, bool) { v, ok := f.vars[k]; return v, ok }

func (f *fixture) environ() []string {
	out := make([]string, 0, len(f.vars))
	for k, v := range f.vars {
		out = append(out, k+"="+v)
	}
	return out
}

// resolve re-resolves the CLI's paths and its service manager from vars.
func (f *fixture) resolve() {
	f.t.Helper()
	p, err := paths.Resolve(&paths.Env{Lookup: f.lookup, Home: f.home, UID: testUID, GOOS: f.goos})
	f.opts.Paths, f.opts.PathsErr, f.opts.Service = p, err, nil
	if err != nil {
		return
	}
	f.opts.Service = f.manager(p)
}

func (f *fixture) manager(p *paths.Paths) service.Manager {
	f.t.Helper()
	bin := filepath.Join(f.home, ".local", "bin", "slop-bot")
	require.NoError(f.t, os.MkdirAll(filepath.Dir(bin), 0o700))
	require.NoError(f.t, os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o700)) // #nosec G306 -- a test stand-in that must be executable
	m, err := service.New(&service.Options{
		Paths: p, Home: f.home, UID: testUID, GOOS: f.goos, Binary: bin, PATH: "/usr/bin",
		Runner: f.runner, Logger: slog.New(slog.DiscardHandler),
	})
	require.NoError(f.t, err)
	return m
}

// config writes yml as the config file and loads it; "~" is the home.
// Empty yml means no file: the defaults.
func (f *fixture) config(yml string) {
	f.t.Helper()
	file := f.opts.Paths.ConfigFile()
	f.opts.ConfigFile = file
	if yml == "" {
		cfg, err := config.Parse(nil, f.opts.Paths.Config.Path, f.home)
		require.NoError(f.t, err)
		f.opts.Config, f.opts.ConfigMissing, f.opts.ConfigErr = cfg, true, nil
		return
	}
	require.NoError(f.t, os.MkdirAll(filepath.Dir(file), 0o700))
	require.NoError(f.t, os.WriteFile(file, []byte(yml), 0o600))
	cfg, err := config.Load(file, f.home)
	f.opts.Config, f.opts.ConfigMissing, f.opts.ConfigErr = cfg, false, err
}

// repo makes a directory the fake git calls a repo.
func (f *fixture) repo(name string) string {
	f.t.Helper()
	dir := filepath.Join(f.home, "src", name)
	require.NoError(f.t, os.MkdirAll(dir, 0o700))
	f.cmds.Answer("git -C "+dir+" rev-parse --is-bare-repository", &service.Result{Stdout: "false\n"})
	f.cmds.Answer("git -C "+dir+" rev-parse --show-toplevel", &service.Result{Stdout: dir + "\n"})
	return dir
}

func (f *fixture) run() *doctor.Report {
	f.t.Helper()
	return doctor.New(f.opts).Run(context.Background())
}

// result finds one check in the report.
func (f *fixture) result(r *doctor.Report, group, name string) doctor.Result {
	f.t.Helper()
	for _, res := range r.Results {
		if res.Group == group && res.Name == name {
			return res
		}
	}
	require.FailNowf(f.t, "no such check", "%s / %s in\n%s", group, name, f.text(r))
	return doctor.Result{}
}

func (f *fixture) text(r *doctor.Report) string {
	f.t.Helper()
	var b strings.Builder
	require.NoError(f.t, r.Write(&b))
	return b.String()
}

// stable swaps the temp root for a fixed one, for golden files.
func (f *fixture) stable(s string) string {
	fixed := "/home"
	if f.goos == "darwin" {
		fixed = "/Users"
	}
	return strings.ReplaceAll(s, f.root, fixed)
}
