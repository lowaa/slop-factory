// Package daemon assembles slop-bot's long-running process from its
// components and runs it until told to stop.
package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	osuser "os/user"
	"time"

	"github.com/lowaa/slop-factory/go-clock"
	"github.com/lowaa/slop-factory/slop-bot/internal/agents"
	"github.com/lowaa/slop-factory/slop-bot/internal/api"
	"github.com/lowaa/slop-factory/slop-bot/internal/config"
	"github.com/lowaa/slop-factory/slop-bot/internal/control"
	"github.com/lowaa/slop-factory/slop-bot/internal/gitx"
	"github.com/lowaa/slop-factory/slop-bot/internal/lifecycle"
	"github.com/lowaa/slop-factory/slop-bot/internal/paths"
	"github.com/lowaa/slop-factory/slop-bot/internal/state"
	"github.com/lowaa/slop-factory/slop-bot/internal/tickets"
	"github.com/lowaa/slop-factory/slop-bot/internal/tickets/local"
	"github.com/lowaa/slop-factory/slop-bot/internal/version"
	"github.com/lowaa/slop-factory/slop-bot/internal/worktree"
)

// StopTimeout bounds a shutdown: components get this long to finish before
// the process exits anyway. The service unit's stop timeout is derived from
// it, so the supervisor never kills a daemon that is still draining.
const StopTimeout = 30 * time.Second

// Options are the daemon's inputs.
type Options struct {
	Config *config.Config
	Paths  *paths.Paths
	Logger *slog.Logger
	Clock  clock.Clock
	// CLIReference is the briefing's command list, generated from the CLI's
	// command tree (which imports this package, so it hands it in).
	CLIReference string
	// CLIPath is how agents run slop-bot; "" means this binary.
	CLIPath string
	// Harnesses overrides the implementations built from the config, for
	// tests that run a fake.
	Harnesses agents.Harnesses
}

// Daemon is the running slop-bot.
type Daemon struct {
	opts      *Options
	server    *control.Server
	group     *lifecycle.Group
	startedAt time.Time
	ready     chan struct{}

	// Set by their components' Start, before the socket serves.
	state        *state.State
	tickets      *local.System
	agents       *agents.Manager
	ticketRoutes *ticketRoutes
	agentRoutes  *agentRoutes
}

// New builds a daemon; nothing starts until Run.
func New(opts *Options) (*Daemon, error) {
	if opts == nil || opts.Config == nil || opts.Paths == nil || opts.Logger == nil || opts.Clock == nil {
		return nil, errors.New("daemon: New needs Config, Paths, Logger and Clock")
	}
	user, err := userID(opts.Config)
	if err != nil {
		return nil, err
	}
	d := &Daemon{opts: opts, server: control.NewServer(opts.Logger), ready: make(chan struct{})}
	d.ticketRoutes = &ticketRoutes{cfg: opts.Config, user: user}
	d.agentRoutes = &agentRoutes{d: d}
	d.routes()
	cliPath := opts.CLIPath
	if cliPath == "" {
		if cliPath, err = os.Executable(); err != nil {
			return nil, fmt.Errorf("daemon: finding its own binary: %w", err)
		}
	}
	harnesses := opts.Harnesses
	if harnesses == nil {
		harnesses = configuredHarnesses(opts.Config, opts.Logger, opts.Clock)
	}
	var lock *instanceLock
	var listener *control.Listener
	d.group = lifecycle.NewGroup(opts.Logger,
		&lifecycle.Func{
			N: "instance lock",
			StartFunc: func(context.Context) error {
				var err error
				lock, err = acquireLock(opts.Paths.LockFile())
				return err
			},
			StopFunc: func(context.Context) error { return lock.release() },
		},
		&lifecycle.Func{
			N: "state.db",
			StartFunc: func(ctx context.Context) error {
				var err error
				d.state, err = state.Open(ctx, &state.Options{Path: opts.Paths.StateDB(), Logger: opts.Logger, Clock: opts.Clock})
				return err
			},
			StopFunc: func(context.Context) error { return d.state.Close() },
		},
		&lifecycle.Func{
			N: "local tickets",
			StartFunc: func(ctx context.Context) error {
				var err error
				d.tickets, err = local.Open(ctx, &local.Options{
					Path:   opts.Paths.TicketsDB(),
					Config: tickets.Config{AgentsMaySetUrgent: *opts.Config.AgentsMaySetUrgent},
					Logger: opts.Logger, Clock: opts.Clock,
				})
				d.ticketRoutes.sys = d.tickets
				return err
			},
			StopFunc: func(context.Context) error { return d.tickets.Close() },
		},
		&lifecycle.Func{
			N:         "ticket feed",
			StartFunc: d.startFeed,
			StopFunc:  func(ctx context.Context) error { return d.tickets.Stop(ctx) },
		},
		d.agentsComponent(harnesses, user, cliPath),
		&lifecycle.Func{
			N: "control socket",
			StartFunc: func(context.Context) error {
				var err error
				if listener, err = control.Listen(opts.Paths.Socket(), opts.Logger); err != nil {
					return err
				}
				d.server.Start(listener, d.state.Agents)
				return nil
			},
			// Stopping the server closes the listener, which removes the
			// socket file.
			StopFunc: d.server.Stop,
		},
	)
	return d, nil
}

// Run starts every component, serves until ctx ends, then stops them.
func (d *Daemon) Run(ctx context.Context) error {
	d.startedAt = d.opts.Clock.Now()
	if err := d.group.Start(ctx); err != nil {
		return err
	}
	d.opts.Logger.Info("slop-bot daemon running", "socket", d.opts.Paths.Socket(), "version", version.String())
	close(d.ready)
	<-ctx.Done()
	d.opts.Logger.Info("slop-bot daemon stopping")
	stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), StopTimeout)
	defer cancel()
	return d.group.Stop(stopCtx)
}

// Ready is closed once every component has started and the socket serves.
func (d *Daemon) Ready() <-chan struct{} { return d.ready }

// agentsComponent runs the agent manager, on the ticket feed and state.db
// the components before it opened.
func (d *Daemon) agentsComponent(harnesses agents.Harnesses, user tickets.UserID, cliPath string) lifecycle.Component {
	opts := d.opts
	return &lifecycle.Func{
		N: "agents",
		StartFunc: func(ctx context.Context) error {
			git, err := gitx.New(&gitx.Options{Bin: "git", Logger: opts.Logger})
			if err != nil {
				return err
			}
			wts, err := worktree.NewManager(&worktree.Options{
				WorkspaceDir: opts.Config.WorkspaceDir, Repos: opts.Config, Git: git, Logger: opts.Logger,
			})
			if err != nil {
				return err
			}
			d.agents, err = agents.New(&agents.Options{
				Config: opts.Config, Paths: opts.Paths, State: d.state, Tickets: d.tickets,
				Harnesses: harnesses, Worktrees: wts, User: user, Logger: opts.Logger,
				Clock: opts.Clock, CLI: cliPath, CLIReference: opts.CLIReference,
			})
			if err != nil {
				return err
			}
			return d.agents.Start(ctx)
		},
		StopFunc: func(ctx context.Context) error { return d.agents.Stop(ctx) },
	}
}

// startFeed resumes the ticket feed from the cursor the scheduler saved. A
// cursor the system no longer knows (a restored backup) replays from the
// start, which at-least-once consumers handle.
func (d *Daemon) startFeed(ctx context.Context) error {
	cursor, _, err := d.state.Cursors.Get(ctx, agents.CursorKey)
	if err != nil {
		return err
	}
	err = d.tickets.Start(ctx, tickets.Cursor(cursor))
	if errors.Is(err, tickets.ErrInvalidCursor) {
		d.opts.Logger.Warn("daemon: the ticket feed's cursor is unknown; replaying from the start", "cursor", cursor)
		return d.tickets.Start(ctx, "")
	}
	return err
}

func (d *Daemon) routes() {
	d.ticketRoutes.register(d.server)
	d.agentRoutes.register(d.server)
	d.server.HandleFunc("GET "+api.PathHealth, func(w http.ResponseWriter, _ *http.Request) {
		control.WriteJSON(w, http.StatusOK, api.Health{
			Version:   version.String(),
			PID:       os.Getpid(),
			StartedAt: d.startedAt,
			Socket:    d.opts.Paths.Socket(),
		})
	})
}

// userID is who this slop-bot runs for: the config's user, or the OS user.
func userID(cfg *config.Config) (tickets.UserID, error) {
	if cfg.User != "" {
		return tickets.UserID(cfg.User), nil
	}
	u, err := osuser.Current()
	if err != nil {
		return "", fmt.Errorf("daemon: no user configured and the OS user is unknown: %w", err)
	}
	return tickets.UserID(u.Username), nil
}
