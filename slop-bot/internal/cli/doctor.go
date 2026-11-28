package cli

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/lowaa/slop-factory/slop-bot/internal/config"
	"github.com/lowaa/slop-factory/slop-bot/internal/doctor"
	"github.com/lowaa/slop-factory/slop-bot/internal/paths"
	"github.com/lowaa/slop-factory/slop-bot/internal/service"
)

// errChecksFailed ends doctor and install with a non-zero exit, after the
// report has said what failed.
var errChecksFailed = errors.New("some checks failed; see the report above")

func (a *app) doctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check this machine can run slop-bot, and say how to fix what can't",
		Long: "Check the tools slop-bot needs, each configured harness (installed, a tested " +
			"version, signed in, its model usable), the repos and workspace, the service " +
			"manager, and that this shell and the installed service agree on where things " +
			"are. It changes nothing and spends no model tokens. Exits non-zero when a " +
			"required check fails.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, perr := a.paths()
			lc := a.readConfig(p)
			if lc.missing && a.configPath != "" {
				lc.err = fmt.Errorf("--config %s doesn't exist", lc.path)
			}
			report := a.newDoctor(cmd, p, perr, lc, false).Run(cmd.Context())
			if err := report.Write(cmd.OutOrStdout()); err != nil {
				return err
			}
			if report.Failed() {
				return errChecksFailed
			}
			return nil
		},
	}
}

// loadedConfig is the config file and what reading it gave.
type loadedConfig struct {
	path    string
	cfg     *config.Config
	missing bool
	err     error
}

// readConfig reads the config doctor and install check. Without paths and
// without --config there is nothing to read; the paths failure is reported
// on its own.
func (a *app) readConfig(p *paths.Paths) *loadedConfig {
	lc := &loadedConfig{path: a.configPath}
	if lc.path != "" {
		// Absolute, so the report and the summary name it wherever they're read.
		if abs, err := filepath.Abs(lc.path); err == nil {
			lc.path = abs
		}
	} else {
		if p == nil {
			lc.err = errors.New("not read: its directory isn't known until the paths resolve")
			return lc
		}
		lc.path = p.ConfigFile()
	}
	lc.cfg, lc.err = config.Load(lc.path, a.deps.Env.Home)
	if errors.Is(lc.err, fs.ErrNotExist) {
		lc.missing = true
		lc.cfg, lc.err = config.Parse(nil, filepath.Dir(lc.path), a.deps.Env.Home)
	}
	return lc
}

func (a *app) newDoctor(cmd *cobra.Command, p *paths.Paths, perr error, lc *loadedConfig, installing bool) *doctor.Doctor {
	opts := &doctor.Options{
		GOOS: a.deps.Env.GOOS, Lookup: a.deps.Env.Lookup, Environ: os.Environ,
		Paths: p, PathsErr: perr,
		ConfigFile: lc.path, Config: lc.cfg, ConfigMissing: lc.missing, ConfigErr: lc.err,
		Commands: a.deps.Commands, Installing: installing,
	}
	if p != nil {
		// No manager for this OS leaves Service nil, which doctor reports.
		if m, err := a.newManager(p, "", "", cmd.ErrOrStderr()); err == nil {
			opts.Service = m
		}
	}
	return doctor.New(opts)
}

// unreachableNote explains a daemon the CLI can't reach, when the installed
// service says why: it isn't installed, or it listens somewhere this shell
// doesn't resolve. "" when there's nothing to add.
func (a *app) unreachableNote(socket string) string {
	p, err := a.paths()
	if err != nil {
		return ""
	}
	m, err := a.newManager(p, "", "", io.Discard)
	if err != nil {
		return ""
	}
	svc, err := service.ServicePaths(m)
	if errors.Is(err, service.ErrNotInstalled) {
		return "the slop-bot service isn't installed: `slop-bot install` sets it up, or run `slop-bot daemon` yourself"
	}
	if err != nil || svc.Socket() == socket {
		return ""
	}
	return fmt.Sprintf("the installed service (%s) listens at %s, not %s; %s",
		m.UnitPath(), svc.Socket(), socket, doctor.MismatchFix(a.deps.Env.GOOS,
			service.Mismatch{What: "control socket", CLI: socket, Service: svc.Socket()}))
}

// mismatchWarnings are `slop-bot paths`'s warnings about an installed
// service that resolves locations differently from this shell.
func (a *app) mismatchWarnings(p *paths.Paths) []string {
	m, err := a.newManager(p, "", "", io.Discard)
	if err != nil {
		return nil
	}
	svc, err := service.ServicePaths(m)
	if err != nil {
		return nil
	}
	var out []string
	for _, mm := range service.Mismatches(p, svc) {
		out = append(out, fmt.Sprintf("%s: this shell resolves %s, but the installed service (%s) uses %s\nfix: %s",
			mm.What, mm.CLI, m.UnitPath(), mm.Service, doctor.MismatchFix(a.deps.Env.GOOS, mm)))
	}
	return out
}
