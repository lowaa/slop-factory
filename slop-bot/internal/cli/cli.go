// Package cli is the slop-bot command line: the user's and the agents'
// interface to the daemon. Its command tree is also the source of the CLI
// reference in the agents' built-in briefing.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/lowaa/slop-factory/go-clock"
	"github.com/lowaa/slop-factory/slop-bot/internal/config"
	"github.com/lowaa/slop-factory/slop-bot/internal/control"
	"github.com/lowaa/slop-factory/slop-bot/internal/daemon"
	"github.com/lowaa/slop-factory/slop-bot/internal/doctor"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/tuidriver"
	"github.com/lowaa/slop-factory/slop-bot/internal/paths"
	"github.com/lowaa/slop-factory/slop-bot/internal/service"
	"github.com/lowaa/slop-factory/slop-bot/internal/version"
)

// Deps is what the CLI needs from the outside world, so tests can run it
// against a temp home and an in-process daemon.
type Deps struct {
	Stdout io.Writer
	Stderr io.Writer
	Env    *paths.Env
	// Token is the agent token from the environment, "" for the user.
	Token string
	// ServiceRunner runs systemctl, loginctl and launchctl for `slop-bot
	// service`.
	ServiceRunner service.Runner
	// FindBinary is the slop-bot binary the service should run.
	FindBinary func() (string, error)
	// Commands runs doctor's probes: each harness's version, sign-in and
	// model list.
	Commands doctor.Commands
	// Stdin answers `uninstall --purge`'s confirmation, and is the terminal
	// an attached TUI reads.
	Stdin io.Reader

	// daemonBuilt lets in-package tests see the daemon `slop-bot daemon`
	// builds, to wait on its Ready.
	daemonBuilt func(*daemon.Daemon)
	// harnesses lets in-package tests run the daemon on a fake harness.
	harnesses daemon.Harnesses
	// tuiHarnesses and tmuxSocket let them run a TUI fake on the daemon's
	// own tmux server (one per test) and hook hub.
	tuiHarnesses func(kit *tuidriver.Sessions) daemon.Harnesses
	tmuxSocket   string
	// clock lets in-package tests run the daemon on a fake clock.
	clock clock.Clock
	// tuiGrace shortens the daemon's wait for a hung-up TUI in tests.
	tuiGrace time.Duration
	// daemonAnswers lets in-package tests stand a daemon in for the one at
	// a socket they can't listen on; nil dials it.
	daemonAnswers func(ctx context.Context, socket string) bool
}

// FromOS is the real outside world.
func FromOS() (*Deps, error) {
	env, err := paths.FromOS()
	if err != nil {
		return nil, err
	}
	return &Deps{
		Stdout: os.Stdout, Stderr: os.Stderr, Env: env, Token: os.Getenv(control.TokenEnv),
		ServiceRunner: service.ExecRunner{}, FindBinary: service.NewBinaryFinder().Find,
		Commands: doctor.ExecCommands{Timeout: probeTimeout}, Stdin: os.Stdin,
	}, nil
}

// probeTimeout bounds each of doctor's probes. agy's model list and
// Codex's app-server go over the network, so it allows for a slow one.
const probeTimeout = 30 * time.Second

// app holds what commands share once flags are parsed.
type app struct {
	deps       *Deps
	configPath string
}

// root builds the command tree.
func (a *app) root() *cobra.Command {
	deps := a.deps
	root := &cobra.Command{
		Use:           "slop-bot",
		Short:         "Run coding agents on tickets, one agent per ticket",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.SetOut(deps.Stdout)
	root.SetErr(deps.Stderr)
	root.PersistentFlags().StringVar(&a.configPath, "config", "",
		"path to slop-bot-config.yaml (default: under $XDG_CONFIG_HOME/slop-bot)")
	root.AddCommand(
		a.versionCmd(),
		a.pathsCmd(),
		a.daemonCmd(),
		a.healthCmd(),
		a.serviceCmd(),
		a.doctorCmd(),
		a.installCmd(),
		a.uninstallCmd(),
		a.ticketCmd(),
		a.tuiCmd(),
		a.hookCmd(),
	)
	root.AddCommand(a.agentCmds()...)
	return root
}

// Execute runs the CLI and returns the process exit code.
func Execute(ctx context.Context, deps *Deps, args []string) int {
	a := &app{deps: deps}
	root := a.root()
	root.SetArgs(args)
	if err := root.ExecuteContext(ctx); err != nil {
		_, _ = fmt.Fprintln(deps.Stderr, "slop-bot:", err)
		var notRunning *control.NotRunningError
		if errors.As(err, &notRunning) {
			if note := a.unreachableNote(notRunning.Socket); note != "" {
				_, _ = fmt.Fprintln(deps.Stderr, "slop-bot: note:", note)
			}
		}
		return 1
	}
	return 0
}

func (a *app) paths() (*paths.Paths, error) { return paths.Resolve(a.deps.Env) }

// loadConfig reads the config, falling back to the defaults when there is no
// file yet: a first run before `slop-bot install` still works.
func (a *app) loadConfig(p *paths.Paths) (*config.Config, error) {
	path := a.configPath
	if path == "" {
		path = p.ConfigFile()
	}
	c, err := config.Load(path, a.deps.Env.Home)
	if errors.Is(err, fs.ErrNotExist) && a.configPath == "" {
		return config.Parse(nil, p.Config.Path, a.deps.Env.Home)
	}
	return c, err
}

func (a *app) client() (*control.Client, error) {
	p, err := a.paths()
	if err != nil {
		return nil, err
	}
	return control.NewClient(p.Socket(), a.deps.Token), nil
}

func (a *app) versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print slop-bot's version",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, err := fmt.Fprintln(cmd.OutOrStdout(), version.String())
			return err
		},
	}
}

func (a *app) daemonCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "daemon",
		Short: "Run the slop-bot daemon in the foreground",
		Long: "Run the daemon in the foreground until interrupted. The installed " +
			"service (systemd or launchd) runs exactly this.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := a.paths()
			if err != nil {
				return err
			}
			cfg, err := a.loadConfig(p)
			if err != nil {
				return err
			}
			logger := slog.New(slog.NewTextHandler(cmd.ErrOrStderr(), nil))
			opts := &daemon.Options{
				Config: cfg, Paths: p, Logger: logger, Clock: clock.Or(a.deps.clock), TUIGrace: a.deps.tuiGrace,
				TUIHarnesses: a.deps.tuiHarnesses, TmuxSocket: a.deps.tmuxSocket,
				CLIReference: AgentReference(cmd.Root()),
			}
			// Only when set: a nil map in the interface would read as
			// "no harnesses", not "build them from the config".
			if a.deps.harnesses != nil {
				opts.Harnesses = a.deps.harnesses
			}
			d, err := daemon.New(opts)
			if err != nil {
				return err
			}
			if a.deps.daemonBuilt != nil {
				a.deps.daemonBuilt(d)
			}
			return d.Run(cmd.Context())
		},
	}
}
