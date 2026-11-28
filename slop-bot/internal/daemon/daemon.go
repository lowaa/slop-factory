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
	"github.com/lowaa/slop-factory/slop-bot/internal/harness"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/sessionhost"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/tuidriver"
	"github.com/lowaa/slop-factory/slop-bot/internal/ident"
	"github.com/lowaa/slop-factory/slop-bot/internal/lifecycle"
	"github.com/lowaa/slop-factory/slop-bot/internal/mergeguard"
	"github.com/lowaa/slop-factory/slop-bot/internal/paths"
	"github.com/lowaa/slop-factory/slop-bot/internal/state"
	"github.com/lowaa/slop-factory/slop-bot/internal/tickets"
	"github.com/lowaa/slop-factory/slop-bot/internal/tickets/local"
	"github.com/lowaa/slop-factory/slop-bot/internal/transcript"
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
	// TUIGrace is how long a hung-up attach's TUI gets to exit on SIGHUP
	// before it is killed; 0 is DefaultTUIGrace.
	TUIGrace time.Duration
	// TUIHarnesses builds implementations on the daemon's TUI kit (its tmux
	// server and hook hub), for tests that run a TUI fake; they go over
	// Harnesses, or over the configured ones.
	TUIHarnesses func(kit *tuidriver.Sessions) Harnesses
	// TmuxSocket names the daemon's tmux server (tmux -L); "" is
	// sessionhost.DefaultSocket. Tests use one each.
	TmuxSocket string
	// StopTimeout bounds the shutdown; 0 is StopTimeout. Tests shorten it to
	// see what a drain past its deadline does.
	StopTimeout time.Duration
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
	attachRoutes *attachRoutes
	guard        *mergeguard.Guard
	// transcripts reads agents' transcripts, live or retained.
	transcripts *transcript.Library
	archive     *transcript.Archive
	// host and hub are the TUI harnesses' tmux server and hook hub; nil
	// when no harness runs under the tui driver.
	host sessionhost.SessionHost
	hub  *hookHub
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
	d.ticketRoutes = &ticketRoutes{cfg: opts.Config, user: user,
		autonomy: func(ctx context.Context) bool { return d.autonomy(ctx) },
		hasLiveAgent: func(ctx context.Context, id ident.TicketID) bool {
			_, err := d.state.Agents.LiveForTicket(ctx, id)
			return err == nil
		},
		tellUser: func(ctx context.Context, body string) { d.agents.TellUser(ctx, body) },
	}
	d.agentRoutes = &agentRoutes{d: d}
	d.attachRoutes = &attachRoutes{d: d}
	d.routes()
	cliPath := opts.CLIPath
	if cliPath == "" {
		if cliPath, err = os.Executable(); err != nil {
			return nil, fmt.Errorf("daemon: finding its own binary: %w", err)
		}
	}
	harnesses, err := d.buildHarnesses(cliPath)
	if err != nil {
		return nil, err
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
		d.archiveComponent(harnesses),
		d.retentionSweep(),
		d.agentsBuild(harnesses, user, cliPath),
		d.guardComponent(),
		// The socket outlives the agents: they call done and ask while
		// they drain, and resumed agents call it as they start.
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
		// Stopped after the agents, which let go of their sessions first;
		// started after them, once they have taken their sessions back.
		d.tuiSessions(),
		&lifecycle.Func{
			N:         "agents",
			StartFunc: func(ctx context.Context) error { return d.agents.Start(ctx) },
			StopFunc:  func(ctx context.Context) error { return d.agents.Stop(ctx) },
		},
		d.orphanedSessions(),
	)
	return d, nil
}

// buildHarnesses is opts' harnesses, or the configured ones, with any built
// on the TUI kit over them. The kit, and so tmux, is needed only when a
// harness runs under the tui driver: without tmux such a harness is left
// out (its tickets are misconfigured, and doctor says why).
func (d *Daemon) buildHarnesses(cliPath string) (agents.Harnesses, error) {
	opts := d.opts
	var kit *tuidriver.Sessions
	if opts.TUIHarnesses != nil || needsTUI(opts.Config) {
		var err error
		if kit, err = d.tuiKit(cliPath); err != nil {
			if opts.TUIHarnesses != nil {
				return nil, err
			}
			opts.Logger.Warn("daemon: no tmux, so the tui harnesses can't run", "err", err)
		}
	}
	harnesses := opts.Harnesses
	if harnesses == nil {
		harnesses = configuredHarnesses(opts.Config, &buildOptions{
			logger: opts.Logger, clock: opts.Clock, preToolHook: []string{cliPath, "hook", "pre-tool"}, tui: kit,
			guard: guardFunc(d.checkTool),
		})
	}
	if opts.TUIHarnesses == nil {
		return harnesses, nil
	}
	return overlay{over: opts.TUIHarnesses(kit), under: harnesses}, nil
}

// overlay finds a harness in over first, then under.
type overlay struct {
	over  Harnesses
	under agents.Harnesses
}

// Harness implements agents.Harnesses.
func (o overlay) Harness(name string) (harness.AIHarness, bool) {
	if h, ok := o.over[name]; ok {
		return h, true
	}
	return o.under.Harness(name)
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
	timeout := d.opts.StopTimeout
	if timeout <= 0 {
		timeout = StopTimeout
	}
	stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
	defer cancel()
	return d.group.Stop(stopCtx)
}

// Ready is closed once every component has started and the socket serves.
func (d *Daemon) Ready() <-chan struct{} { return d.ready }

// SweepEvery is how often the retention sweep runs: retention is counted
// in days, so an hour late is nothing.
const SweepEvery = time.Hour

// archiveComponent keeps ended agents' transcripts, on state.db's index.
func (d *Daemon) archiveComponent(harnesses agents.Harnesses) lifecycle.Component {
	opts := d.opts
	return &lifecycle.Func{
		N: "transcript archive",
		StartFunc: func(context.Context) error {
			var err error
			d.archive, err = transcript.NewArchive(&transcript.ArchiveOptions{
				Dir: opts.Paths.TranscriptsDir(), Index: d.state.Transcripts, Retain: opts.Config.Retention.Transcripts.D(),
				Clock: opts.Clock,
			})
			if err != nil {
				return err
			}
			d.transcripts = transcript.NewLibrary(harnesses, d.archive)
			return nil
		},
	}
}

// retentionSweep deletes what is past its retention, on the clock:
// retained transcripts; the event log and delivered messages, which hold
// some of what agents said; agents' state history; and ended agents.
func (d *Daemon) retentionSweep() lifecycle.Component {
	return &lifecycle.Periodic{
		N: "retention sweep", Every: SweepEvery, Clock: d.opts.Clock, Logger: d.opts.Logger,
		Run: func(ctx context.Context) error { return d.sweep(ctx) },
	}
}

func (d *Daemon) sweep(ctx context.Context) error {
	now, r := d.opts.Clock.Now(), &d.opts.Config.Retention
	_, err := d.state.Pruner.Prune(ctx, &state.PruneCutoffs{
		Events:            now.Add(-r.Events.D()),
		StateChanges:      now.Add(-r.StateChanges.D()),
		DeliveredMessages: now.Add(-r.Messages.D()),
		EndedAgents:       now.Add(-r.Agents.D()),
	})
	return errors.Join(d.archive.Sweep(ctx), err)
}

// agentsBuild creates the agent manager, on the ticket feed and state.db
// the components before it opened, so the control socket's routes have one
// to call; the "agents" component after the socket starts it.
func (d *Daemon) agentsBuild(harnesses agents.Harnesses, user tickets.UserID, cliPath string) lifecycle.Component {
	opts := d.opts
	return &lifecycle.Func{
		N: "agent manager",
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
				Clock: opts.Clock, CLI: cliPath, CLIReference: opts.CLIReference, Transcripts: d.transcripts,
			})
			return err
		},
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
	d.attachRoutes.register(d.server)
	(&observeRoutes{d: d}).register(d.server)
	d.server.HandleFunc("POST "+api.PathHookTUI, d.hookTUI)
	(&worktreeRoutes{d: d}).register(d.server)
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
