package main

import (
	"context"
	"errors"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/landing"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/servermerge/runner"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"time"

	"github.com/lowaa/slop-factory/go-auth/clientip"
	"github.com/lowaa/slop-factory/go-auth/ratelimit"
	"github.com/lowaa/slop-factory/go-web/server"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/change"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost/githubapp"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/config"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/githubhook"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/hookkit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/intake"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/jobs"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/metrics"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/prune"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/steward"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/steward/discussionapply"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// endpoint is a server and the address it is configured to listen on.
type endpoint struct {
	addr string
	srv  *server.Server
}

// runEndpoints runs every endpoint as server.RunAll does, except that an
// endpoint whose address is a key of bound serves on that listener instead
// of binding the address itself. A bound listener no endpoint wants (an
// optional door that is configured off) is closed, so that address refuses
// connections as an unbound one would. A test that must know its ports before
// serve starts binds them first and keeps them, rather than picking free
// ports, releasing them and racing whatever else binds in between.
func runEndpoints(ctx context.Context, endpoints []*endpoint, bound map[string]net.Listener) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	wanted := map[string]bool{}
	for _, e := range endpoints {
		wanted[e.addr] = true
	}
	for addr, ln := range bound {
		if !wanted[addr] {
			_ = ln.Close()
		}
	}
	errs := make(chan error, len(endpoints))
	for _, e := range endpoints {
		go func() {
			if ln, ok := bound[e.addr]; ok {
				errs <- e.srv.Serve(ctx, ln)
			} else {
				errs <- e.srv.Run(ctx)
			}
			cancel()
		}()
	}
	var all []error
	for range endpoints {
		all = append(all, <-errs)
	}
	return errors.Join(all...)
}

// listeners are the servers the api runs: the board's, and the webhook
// door and the metrics when they are configured.
func listeners(cfg *config.Config, b *board, webhooks *githubhook.Service, intakes *intake.Service, telemetry *metrics.Metrics, limits *limiters, logHandler slog.Handler) ([]*endpoint, error) {
	logger := slog.New(logHandler)
	resolver, err := clientip.NewResolver(cfg.TrustedProxies)
	if err != nil {
		return nil, err
	}
	servers := []*endpoint{{cfg.ListenAddr, boardServer(cfg.ListenAddr, b.handler, b.waits, nil)}}
	logger.Info("listening", "addr", cfg.ListenAddr)

	// The GitHub webhook door is a listener of its own, serving the
	// receiver and nothing else: it is the one address the internet
	// reaches (see githubhook), and a routing mistake in front of it can
	// then expose one signature-checked handler, never the board.
	if webhooks.Enabled() {
		servers = append(servers, &endpoint{cfg.Webhook.ListenAddr, server.New(cfg.Webhook.ListenAddr, webhookHandler(cfg, webhooks, limits, resolver, logHandler), nil)})
		logger.Info("github webhooks listening", "addr", cfg.Webhook.ListenAddr, "public_base_url", cfg.Webhook.PublicBaseURL)
	} else {
		logger.Warn("github webhooks disabled: WEBHOOK_PUBLIC_BASE_URL or the credential key is not set, so no listener is started")
	}

	// The intake door is a listener of its own too, and for the same
	// reason: it serves the receiver and nothing else. Like the webhook door
	// it may be routed to from the internet.
	if intakes.Enabled() {
		servers = append(servers, &endpoint{cfg.Intake.ListenAddr, server.New(cfg.Intake.ListenAddr, intakeHandler(cfg, intakes, limits, resolver, logHandler), nil)})
		logger.Info("intake listening", "addr", cfg.Intake.ListenAddr, "public_base_url", cfg.Intake.PublicBaseURL)
	}

	// Metrics are a listener of their own, like the webhook door, but the
	// other way round: nothing in front of the server routes to it. The
	// frontend's nginx and every Ingress name the board's listener, whose
	// mux has no /metrics; only the cluster's scraper is pointed here.
	if cfg.MetricsListenAddr != "" {
		servers = append(servers, &endpoint{cfg.MetricsListenAddr, server.New(cfg.MetricsListenAddr, metricsMux(telemetry), nil)})
		logger.Info("metrics listening", "addr", cfg.MetricsListenAddr)
	}
	return servers, nil
}

func webhookHandler(cfg *config.Config, webhooks *githubhook.Service, limits *limiters, resolver *clientip.Resolver, logHandler slog.Handler) http.Handler {
	policy := config.RateLimit{Name: "webhook_client", Limit: cfg.Webhook.ClientLimitPerMinute, Window: time.Minute}
	return githubhook.NewReceiver(webhooks, logHandler).HandlerBehind(clientGate(limits, resolver, policy, logHandler))
}

func intakeHandler(cfg *config.Config, intakes *intake.Service, limits *limiters, resolver *clientip.Resolver, logHandler slog.Handler) http.Handler {
	policy := config.RateLimit{Name: "intake_client", Limit: cfg.Intake.ClientLimitPerMinute, Window: time.Minute}
	return intake.NewReceiver(intakes, logHandler).HandlerBehind(clientGate(limits, resolver, policy, logHandler))
}

// clientGate limits each client address on a hook door before the door
// reads anything. The address is the one the rest of the app counts by:
// X-Forwarded-For is believed only from TRUSTED_PROXIES, and otherwise the
// connection's own address is used.
func clientGate(limits *limiters, resolver *clientip.Resolver, policy config.RateLimit, logHandler slog.Handler) hookkit.Gate {
	addr := func(r *http.Request) netip.Addr {
		return resolver.Resolve(r.RemoteAddr, r.Header.Values("X-Forwarded-For"))
	}
	return hookkit.ClientGate(ratelimit.New(limits.limit(policy)), addr, policy.Window, logHandler)
}

// boardServer is the listener the board, the API and MCP are served on.
// Its shutdown answers every open worker and conductor poll first
// ("restarting, call again"), and ends the conversation streams: a wait is
// held open for up to half an hour, so left alone it would hold the shutdown
// up until its timeout cut the connection instead.
func boardServer(addr string, handler http.Handler, waits interface{ Close() }, opts *server.Options) *server.Server {
	srv := server.New(addr, handler, opts)
	srv.OnShutdown(waits.Close)
	return srv
}

// metricsMux serves the metrics, and nothing else.
func metricsMux(m *metrics.Metrics) *http.ServeMux {
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", m.Handler())
	return mux
}

// startJobs starts the periodic jobs alongside the server rather than as cron
// entries: they need the same config and database handle, and a coordination
// server that quietly filled its disk because a separate schedule was never
// installed would be a poor showing. Each job's closer is pushed under its
// name, so the database, pushed before them, cannot be closed while a pass is
// still mid-query against the pool.
func startJobs(ctx context.Context, cfg *config.Config, database *db.DB, changes *change.Service, codeHost *githubapp.Service, stewardApplier *steward.Applier, discussionApply *discussionapply.Service, extraJobs []*jobs.Job, sched *jobs.Scheduler, cl *closers, logHandler slog.Handler) error {
	for _, job := range append([]*jobs.Job{
		prune.NewService(prune.NewPostgresStore(database, logHandler), cfg.Prune, logHandler).Job(),
		ticket.NewCooldownSweeper(ticket.NewPostgresStore(database, ticket.WithMergeQueue(landing.NewDequeuer())), cfg.CooldownSweepInterval, logHandler).Job(),
		stewardApplier.Job(steward.DefaultApplyInterval),
		discussionApply.Job(discussionapply.DefaultInterval),
		change.NewReconciler(changes, change.NewPostgresStore(database), cfg.ExperimentReconcileInterval, logHandler).Job(),
		// Once at startup, so a changed WEBHOOK_PUBLIC_BASE_URL reaches the
		// Apps. In the background: it calls GitHub once per tenant and must not
		// hold up serving.
		&jobs.Job{Name: "webhook_reconcile", Run: codeHost.ReconcileWebhooks},
	}, extraJobs...) {
		if err := cl.startJob(ctx, sched, job); err != nil {
			return err
		}
	}
	return nil
}

// startServerMerge has the server merge a change itself when a person
// approves it, for tenants with a code host credential. Off, nothing is
// built and approvals are reported to no one. Its closer abandons attempts
// in flight (their leases run out and any replica's reaper hands the tickets
// back) and stops the reaper.
func startServerMerge(ctx context.Context, sched *jobs.Scheduler, cfg *config.Config, database *db.DB, svcs *services, b *board, cl *closers, logHandler slog.Handler) error {
	if !cfg.ServerMergeEnabled {
		return nil
	}
	r, err := runner.New(&runner.Options{
		Tickets:  ticket.NewServerAgent(svcs.tickets),
		Changes:  b.changes,
		Tools:    b.codeHost,
		Outcomes: landing.NewStore(database),
		Routing:  approvalRouting(cfg, svcs),
		Reaper:   runner.NewPostgresReaper(database),
		Config:   runner.Config{Affinity: cfg.ResumeAffinity},
		Logger:   logHandler,
	})
	if err != nil {
		return err
	}
	svcs.mergeHook.Bind(r)
	cl.push("server merge", r.Close)
	return cl.startJob(ctx, sched, r.Job())
}
