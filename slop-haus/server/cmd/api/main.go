// Command api boots slop-haus/server's HTTP API.
package main

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/config"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/harness"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/jobs"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/landing"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/logging"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/metrics"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/schema"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/secretscan"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/supervisorwatch"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/tenantstate"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

// logFlushTimeout is how long the logs get to reach the collector on the
// way out. It comes after the servers' shutdown, inside the same
// terminationGracePeriodSeconds (see server.DefaultShutdownTimeout).
const logFlushTimeout = 5 * time.Second

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load(os.Args[1:])
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	logs, err := logging.New(cfg.LogsOTLPEndpoint, os.Stderr)
	if err != nil {
		return err
	}
	// Pushed first, so it runs last: everything serve opened has been
	// closed, and whatever was logged on the way down is flushed with the
	// rest.
	cl := &closers{}
	cl.push("logs", func() {
		flushCtx, cancel := context.WithTimeout(context.Background(), logFlushTimeout)
		defer cancel()
		_ = logs.Close(flushCtx)
	})
	// For what logs through the package-level functions, here and in the
	// libraries, rather than a handler it was given.
	slog.SetDefault(slog.New(logs.Handler()))
	return serve(ctx, cfg, cl, logs.Handler())
}

// serveSeams are what a test replaces in serve; run passes none.
type serveSeams struct {
	changes codehost.Changes
	// proposalClock is the proposals' time source; the wall clock when nil.
	proposalClock func() time.Time
	// bound are listeners already open, by the address cfg names for them.
	bound map[string]net.Listener
}

type serveOption func(*serveSeams)

// withChanges replaces the GitHub host's operations (see serveSeams.changes).
func withChanges(c codehost.Changes) serveOption {
	return func(s *serveSeams) { s.changes = c }
}

// withProposalClock replaces the clock proposals expire by.
func withProposalClock(now func() time.Time) serveOption {
	return func(s *serveSeams) { s.proposalClock = now }
}

// withBoundListeners has serve use the open listeners for the addresses they
// are keyed by, instead of binding those addresses.
func withBoundListeners(bound map[string]net.Listener) serveOption {
	return func(s *serveSeams) { s.bound = bound }
}

// serve builds everything from cfg and serves until ctx ends or a listener
// fails. Each step hands the next what it needs, and pushes what it opens
// onto cl; serve closes cl on every way out, once the servers have stopped,
// in reverse: the jobs, last started first (the landing runner, the server merge reaper, the webhook reconciler, the closing rule reconciler, the cooldown sweep, the prune), the sign-in providers' discovery, the mail outbox,
// the database, then whatever the caller pushed before calling serve.
func serve(ctx context.Context, cfg *config.Config, cl *closers, logHandler slog.Handler, opts ...serveOption) error {
	defer cl.close()
	var seams serveSeams
	for _, opt := range opts {
		opt(&seams)
	}

	// What alerts are raised on; served on a listener of its own. events
	// records each security event to the log and counts it.
	telemetry := metrics.New(&metrics.Options{Actions: audit.Actions(), Methods: signInMethods(cfg), SecretKinds: secretKinds()})
	events := audit.New(logHandler, telemetry)

	database, err := openDatabase(ctx, cfg)
	if err != nil {
		return err
	}
	cl.push("database", database.Close)

	if err := telemetry.Register(ticket.NewBoardCollector(database, logHandler)); err != nil {
		return fmt.Errorf("register board metrics: %w", err)
	}

	limits := &limiters{telemetry: telemetry, logHandler: logHandler}
	late := newUserLate()
	users, err := newUsers(ctx, &usersDeps{cfg: cfg, database: database, events: events, limits: limits, late: late}, cl, logHandler)
	if err != nil {
		return err
	}
	c, err := newContainer(cfg, database, events, limits, users, late, logHandler)
	if err != nil {
		return err
	}
	svcs, err := newServices(ctx, c, &seams)
	if err != nil {
		return err
	}

	// Pushed after the database, so it runs before it closes: a discussion's
	// label is queued off its request and must not lose its connection.
	cl.push("discussion hooks", svcs.tickets.WaitDiscussionHooks)

	if err := telemetry.Register(supervisorwatch.NewCollector(lookup[*supervisorModule](svcs.modules).Service, logHandler)); err != nil {
		return fmt.Errorf("register supervisor metrics: %w", err)
	}
	queue := landing.NewPostgresQueue(database)
	roster := tenantstate.Active(database.Global())
	if err := telemetry.Register(metrics.NewLockCollector(ticketLockSource{svcs.tickets, landing.NewSettingsMode(svcs.settings, cfg.MergeQueueEnabled), queue}, roster, logHandler)); err != nil {
		return fmt.Errorf("register integration lock metrics: %w", err)
	}
	bounds, err := landingStepBounds(cfg, logHandler)
	if err != nil {
		return err
	}
	if err := telemetry.Register(metrics.NewLandingCollector(landingProgressSource{queue: queue, bounds: bounds}, roster, logHandler)); err != nil {
		return fmt.Errorf("register landing progress metrics: %w", err)
	}

	boardHTTP, err := newBoard(ctx, &boardDeps{container: c, svcs: svcs}, logHandler)
	if err != nil {
		return err
	}

	cl.push("promotion carries", boardHTTP.promotions.WaitForCarries)
	sched := jobs.New(&jobs.Options{Logger: logHandler})
	if err := telemetry.Register(sched.Collector()); err != nil {
		return fmt.Errorf("register job metrics: %w", err)
	}
	if err := startJobs(ctx, cfg, database, boardHTTP.changes, boardHTTP.codeHost, svcs.steward.Applier(), svcs.discussionApply, lookup[*attachmentModule](svcs.modules).Jobs, sched, cl, logHandler); err != nil {
		return err
	}
	if err := startServerMerge(ctx, sched, cfg, database, svcs, boardHTTP, cl, logHandler); err != nil {
		return err
	}
	// Pushed after the server merge runner's Close, so it runs before it: a
	// hook still handing an approval to the runner finishes first.
	cl.push("approval hooks", svcs.tickets.WaitApprovalHooks)
	if err := startLanding(ctx, cfg, database, svcs, boardHTTP, cl, &batchDeps{
		database:  database,
		manifests: c.manifestStore, settings: svcs.settings, events: c.events,
	}, logHandler); err != nil {
		return err
	}
	if err := svcs.serverCells.verifyAll(); err != nil {
		return err
	}

	servers, err := listeners(cfg, boardHTTP, lookup[*githubHookModule](svcs.modules).Service, lookup[*intakeModule](svcs.modules).Service, telemetry, limits, logHandler)
	if err != nil {
		return err
	}
	return runEndpoints(ctx, servers, seams.bound)
}

// openDatabase migrates as the owning role, records the registered harnesses,
// makes sure the app role exists,
// and returns a pool connected as the app role -- the only one requests are
// served through. The owner's pool is closed before this returns: nothing
// after startup should hold a connection RLS does not bind.
func openDatabase(ctx context.Context, cfg *config.Config) (*db.DB, error) {
	owner, err := db.New(ctx, cfg.DatabaseURL)
	if err != nil {
		return nil, fmt.Errorf("connect database as owner: %w", err)
	}
	defer owner.Close()

	slog.Info("running migrations...")
	if err := db.Migrate(ctx, owner, schema.Migrations); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}
	if err := harness.Sync(ctx, owner.Global()); err != nil {
		return nil, err
	}
	if err := db.EnsureAppRole(ctx, owner, cfg.AppDBRole, cfg.AppDBPassword); err != nil {
		return nil, err
	}

	appDSN, err := db.WithCredentials(cfg.DatabaseURL, cfg.AppDBRole, cfg.AppDBPassword)
	if err != nil {
		return nil, err
	}
	database, err := db.New(ctx, appDSN)
	if err != nil {
		return nil, fmt.Errorf("connect database as %s: %w", cfg.AppDBRole, err)
	}
	return database, nil
}

// ticketLockSource adapts the lock state the board's lock panel reads to what
// the metrics package counts, which may not name the ticket package. A tenant
// landing by the merge queue has no lock holder, so nothing is held and only
// the length of the line behind the landing in hand counts as waiting: how
// long the line has waited says nothing about a stuck head (landingProgressSource
// reports that).
type ticketLockSource struct {
	tickets lockStates
	mode    landing.Mode
	queue   queueProgress
}

type lockStates interface {
	IntegrationLocks(ctx context.Context) ([]ticket.NamedLockState, error)
}

type queueProgress interface {
	Progress(ctx context.Context) (landing.Progress, error)
}

func (s ticketLockSource) LockLoads(ctx context.Context) ([]metrics.LockLoad, error) {
	queued, err := s.mode.Enabled(ctx)
	if err != nil {
		return nil, err
	}
	if queued {
		p, err := s.queue.Progress(ctx)
		if err != nil {
			return nil, err
		}
		return []metrics.LockLoad{{Waiters: max(p.Landing-1, 0)}}, nil
	}
	locks, err := s.tickets.IntegrationLocks(ctx)
	if err != nil {
		return nil, err
	}
	loads := make([]metrics.LockLoad, len(locks))
	for i, l := range locks {
		loads[i].Waiters = len(l.Queue)
		if l.Holder != nil {
			loads[i].Held = l.Holder.Held
		}
	}
	return loads, nil
}

type stepBounder interface {
	Of(ctx context.Context, step landing.Step) time.Duration
}

// landingProgressSource adapts the landing queue's Progress to the metrics
// package, with the bound of the step the landing in hand is on.
type landingProgressSource struct {
	queue  queueProgress
	bounds stepBounder
}

func (s landingProgressSource) LandingProgress(ctx context.Context) (metrics.LandingProgress, error) {
	p, err := s.queue.Progress(ctx)
	if err != nil {
		return metrics.LandingProgress{}, err
	}
	out := metrics.LandingProgress{Step: string(p.Step), StepAge: p.StepAge, Queued: p.Landing, OldestWait: p.OldestWait,
		TopWait: p.TopWait, BatchesWithoutCI: p.BatchesWithoutCI, OldestBatchWithoutCI: p.OldestBatchWithoutCI}
	if p.Step != "" {
		out.StepBound = s.bounds.Of(ctx, p.Step)
	}
	return out, nil
}

// landingStepBounds is how long each landing step may take, from the same
// CI the runner waits on.
func landingStepBounds(cfg *config.Config, logHandler slog.Handler) (*landing.StepBounds, error) {
	lcfg := landing.Config{Affinity: cfg.ResumeAffinity}
	if !cfg.MergeQueueEnabled {
		// No runner: no landing has a step, so nothing reads a bound.
		return landing.NewStepBounds(landing.NewHostOnlyPipelines(time.Hour), lcfg, nil, logHandler), nil
	}
	pipelines, err := landingPipelines(cfg, &lcfg)
	if err != nil {
		return nil, err
	}
	return landing.NewStepBounds(pipelines, lcfg, nil, logHandler), nil
}

func secretKinds() []string {
	kinds := make([]string, len(secretscan.Kinds))
	for i, k := range secretscan.Kinds {
		kinds[i] = string(k)
	}
	return kinds
}
