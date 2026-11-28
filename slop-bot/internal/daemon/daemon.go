// Package daemon assembles slop-bot's long-running process from its
// components and runs it until told to stop.
package daemon

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/lowaa/slop-factory/go-clock"
	"github.com/lowaa/slop-factory/slop-bot/internal/api"
	"github.com/lowaa/slop-factory/slop-bot/internal/config"
	"github.com/lowaa/slop-factory/slop-bot/internal/control"
	"github.com/lowaa/slop-factory/slop-bot/internal/ident"
	"github.com/lowaa/slop-factory/slop-bot/internal/lifecycle"
	"github.com/lowaa/slop-factory/slop-bot/internal/paths"
	"github.com/lowaa/slop-factory/slop-bot/internal/version"
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
}

// Daemon is the running slop-bot.
type Daemon struct {
	opts      *Options
	server    *control.Server
	group     *lifecycle.Group
	startedAt time.Time
	ready     chan struct{}
}

// New builds a daemon; nothing starts until Run.
func New(opts *Options) (*Daemon, error) {
	if opts == nil || opts.Config == nil || opts.Paths == nil || opts.Logger == nil || opts.Clock == nil {
		return nil, errors.New("daemon: New needs Config, Paths, Logger and Clock")
	}
	d := &Daemon{opts: opts, server: control.NewServer(opts.Logger), ready: make(chan struct{})}
	d.routes()
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
			N: "control socket",
			StartFunc: func(context.Context) error {
				var err error
				if listener, err = control.Listen(opts.Paths.Socket(), opts.Logger); err != nil {
					return err
				}
				d.server.Start(listener, noAgents{})
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

func (d *Daemon) routes() {
	d.server.HandleFunc("GET "+api.PathHealth, func(w http.ResponseWriter, _ *http.Request) {
		control.WriteJSON(w, http.StatusOK, api.Health{
			Version:   version.String(),
			PID:       os.Getpid(),
			StartedAt: d.startedAt,
			Socket:    d.opts.Paths.Socket(),
		})
	})
}

// noAgents resolves no tokens. It stands in until state.db's agent store is
// wired, so every token is refused rather than trusted.
type noAgents struct{}

func (noAgents) AgentForTokenHash(context.Context, string) (ident.Principal, error) {
	return ident.Principal{}, control.ErrUnknownToken
}
