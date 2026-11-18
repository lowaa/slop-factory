package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/change"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/config"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/landing"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/landing/guidance"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/tenantstate"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// landingPipelines is the CI the landing runner reads: Woodpecker where it is
// configured, and otherwise the code host's check statuses alone, so a tenant
// with no CI adapter still lands.
func landingPipelines(cfg *config.Config, fallback *landing.Config) (landing.Pipelines, error) {
	w := cfg.Woodpecker
	if w.URL == "" {
		return landing.NewHostOnlyPipelines(fallback.FallbackChecksTimeout), nil
	}
	token, err := os.ReadFile(w.TokenFile)
	if err != nil {
		// The error names the file, never its content.
		return nil, fmt.Errorf("read WOODPECKER_TOKEN_FILE: %w", err)
	}
	return landing.NewWoodpeckerPipelines(&landing.WoodpeckerConfig{
		URL: w.URL, Token: strings.TrimSpace(string(token)), RepoID: w.RepoID, BaseBranch: w.BaseBranch,
	})
}

// newLandingGuidance is what names the change a hand-back collided with. Left
// unset, conflict and red hand-backs fall back to the generic text.
func newLandingGuidance(database *db.DB, svcs *services, logHandler slog.Handler) *guidance.Advisor {
	return guidance.New(
		guidance.NewPostgresSource(database, change.NewPostgresStore(database), svcs.hosts),
		guidance.NewPostgresHistory(database), guidance.DefaultBound, logHandler)
}

// approvalRouting is the decision both engines read of where an approval goes.
func approvalRouting(cfg *config.Config, svcs *services) *landing.ApprovalRouting {
	return landing.NewApprovalRouting(landing.NewSettingsMode(svcs.settings, cfg.MergeQueueEnabled), cfg.MergeQueueApprovalEnabled)
}

type landingDeps struct {
	cfg        *config.Config
	lcfg       landing.Config
	database   *db.DB
	svcs       *services
	b          *board
	pipelines  landing.Pipelines
	host       landing.Host
	logHandler slog.Handler
}

// landingOptions wires the runner's collaborators. Guidance is set here so
// the single-ticket and batch paths both name the change a hand-back
// collided with.
func landingOptions(d *landingDeps) *landing.Options {
	var observer landing.Observer
	if d.b.telemetry != nil {
		d.b.telemetry.TrackLanding(outcomeNames())
		observer = d.b.telemetry
	}
	roster := tenantstate.Active(d.database.Global())
	return &landing.Options{
		Observer:  observer,
		Tickets:   ticket.NewServerAgent(d.svcs.tickets),
		Changes:   d.b.changes,
		Host:      d.host,
		Guidance:  newLandingGuidance(d.database, d.svcs, d.logHandler),
		Pipelines: d.pipelines,
		Queue:     landing.NewPostgresQueue(d.database),
		Outcomes:  landing.NewStore(d.database),
		Mode:      landing.NewSettingsMode(d.svcs.settings, d.cfg.MergeQueueEnabled),
		Tenants:   roster.Tenants,
		Config:    d.lcfg,
		Logger:    d.logHandler,
	}
}

// startLanding builds the landing runner, unless the MERGE_QUEUE_ENABLED kill
// switch is off, and sweeps it: a landing the server died in is picked up again
// from its row. The finisher kicks it as tickets are enqueued. Its closer
// abandons landings in flight and releases their claims, so the next sweep
// reconciles them from the code host without waiting out a lease; after a
// crash the lease's TTL is the recovery.
func startLanding(ctx context.Context, cfg *config.Config, database *db.DB, svcs *services, b *board, cl *closers, bd *batchDeps, logHandler slog.Handler) error {
	if !cfg.MergeQueueEnabled {
		return nil
	}
	lcfg := landing.Config{Affinity: cfg.ResumeAffinity, KnownFlaky: cfg.LandingKnownFlaky}
	pipelines, err := landingPipelines(cfg, &lcfg)
	if err != nil {
		return err
	}
	host := landing.NewPostgresHost(change.NewPostgresStore(database), svcs.hosts)
	r, err := landing.New(landingOptions(&landingDeps{
		cfg: cfg, lcfg: lcfg, database: database, svcs: svcs, b: b, pipelines: pipelines, host: host, logHandler: logHandler,
	}))
	if err != nil {
		return err
	}
	batches, err := newBatchRunner(r, host, bd, logHandler)
	if err != nil {
		return err
	}
	r.UseBatches(batches)
	cl.push("landing runner", r.Close)
	lookup[*landingModule](svcs.modules).Finisher.SetKicker(r)
	if svcs.queueHook != nil {
		svcs.queueHook.Bind(landing.NewApprovalHook(approvalRouting(cfg, svcs), host, landing.NewLandingStore(database), r))
	}
	go r.Run(ctx)
	startInvariantMonitor(ctx, database, landing.NewSettingsMode(svcs.settings, cfg.MergeQueueEnabled), bd.manifests, b.telemetry, logHandler)
	return nil
}

func outcomeNames() []string {
	names := make([]string, len(landing.All))
	for i, o := range landing.All {
		names[i] = string(o)
	}
	return names
}
