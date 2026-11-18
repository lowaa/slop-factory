package prune

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/jobs"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/tenantstate"
)

type Store interface {
	// Tenants lists every tenant, archived ones too: their rows still age.
	// Read through the global handle: pruning runs on a background timer
	// with no request to inherit a tenant from.
	Tenants(ctx context.Context) ([]uuid.UUID, error)
	// Sweep deletes what a rule has expired, in every tenant or, for a
	// global rule, outside any, and returns how many rows.
	Sweep(ctx context.Context, rule *Rule) (int64, error)
	// ExpirePendingActions closes proposals waiting past their expiry. It
	// deletes nothing, so it is not a count of the pass.
	ExpirePendingActions(ctx context.Context, now time.Time) error
}

type Service struct {
	store  Store
	cfg    Config
	logger *slog.Logger
	// now is swappable so tests can age rows without sleeping.
	now func() time.Time
}

func NewService(store Store, cfg Config, logHandler slog.Handler) *Service {
	return &Service{store: store, cfg: cfg, logger: slog.New(logHandler), now: time.Now}
}

// RunOnce prunes every tenant's data and returns the total across all of
// them.
//
// A failure on one rule, or one tenant, does not stop the rest: they are
// independent, and a session prune failing for one tenant is no reason to
// skip its leases, or to leave every other tenant unpruned. The first error
// seen is returned once everything has been attempted.
func (s *Service) RunOnce(ctx context.Context) (Result, error) {
	var total Result
	var firstErr error
	record := func(err error) {
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}

	// Always, whatever the retention: an undecided proposal past its time
	// holds its ticket's slot until something closes it. Per tenant, on a
	// context scoped to it: this runs on a background timer, not behind a
	// request, so nothing has scoped the context already.
	record(tenantstate.Each(ctx, s.store, func(ctx context.Context, _ uuid.UUID) error {
		return s.store.ExpirePendingActions(ctx, s.now())
	}))

	for _, rule := range Rules(&s.cfg) {
		deleted, err := s.store.Sweep(ctx, rule)
		*rule.count(&total) += deleted
		record(err)
	}

	if firstErr != nil {
		return total, fmt.Errorf("prune: %w", firstErr)
	}
	return total, nil
}

// Job is the pruning pass as a background job, or nil when pruning is
// disabled.
//
// It runs once at startup rather than waiting out the first interval, so a
// server restarted more often than the interval still prunes. The scheduler
// logs a failed pass and carries on: a database blip must not silently stop
// pruning forever, which is the failure that ends with a full disk.
func (s *Service) Job() *jobs.Job {
	if s.cfg.Interval <= 0 {
		s.logger.Info("pruning disabled")
		return nil
	}
	return &jobs.Job{Name: "prune", Interval: s.cfg.Interval, Run: s.pass}
}

func (s *Service) pass(ctx context.Context) error {
	result, err := s.RunOnce(ctx)
	switch {
	case err != nil:
		return err
	case !result.Empty():
		s.logger.Info("pruned",
			"sessions", result.Sessions,
			"credentials", result.Credentials,
			"leases", result.Leases,
			"lease_activity", result.LeaseActivity,
			"github_deliveries", result.Deliveries,
			"llm_usage", result.LLMUsage,
			"pending_actions", result.PendingActions,
			"conversations", result.Conversations)
	}
	return nil
}
