package change

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/jobs"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/tenantstate"
)

// DefaultReconcileInterval is how often the closing rule is checked for
// changes the transitions missed.
const DefaultReconcileInterval = time.Minute

// ReconcilerStore is what the reconciler needs beyond the rule itself.
type ReconcilerStore interface {
	Tenants(ctx context.Context) ([]uuid.UUID, error)
}

// Reconciler applies the closing rule to every tenant on a timer: the repair
// for a crash between a transition's commit and its host call, and for a host
// that was down. Like prune it runs inside the server rather than as a cron
// entry nobody would notice missing.
type Reconciler struct {
	svc      *Service
	store    ReconcilerStore
	interval time.Duration
	logger   *slog.Logger
}

// NewReconciler passes every interval; Job is nil for a zero one.
func NewReconciler(svc *Service, store ReconcilerStore, interval time.Duration, logHandler slog.Handler) *Reconciler {
	return &Reconciler{svc: svc, store: store, interval: interval, logger: slog.New(logHandler)}
}

// PassOnce applies the rule in every tenant. A tenant that fails does not stop
// the others; the first error is returned once all have been tried.
func (r *Reconciler) PassOnce(ctx context.Context) error {
	return tenantstate.Each(ctx, r.store, func(ctx context.Context, tenant uuid.UUID) error {
		report, err := r.svc.ApplyClosingRule(ctx, tenant, 0)
		if report != nil && (len(report.Closed) > 0 || len(report.BasesDeleted) > 0) {
			r.logger.Info("closing rule reconciled", "tenant_id", tenant.String(), "close", report.Closed, "delete_base_branches", report.BasesDeleted)
		}
		return err
	})
}

// Job is the pass as a background job, or nil when the interval is zero. A
// failed pass is logged by the scheduler and the next one still runs.
func (r *Reconciler) Job() *jobs.Job {
	if r.interval <= 0 {
		r.logger.Info("closing rule reconciler disabled")
		return nil
	}
	return &jobs.Job{Name: "closing_rule_reconciler", Interval: r.interval, Run: r.PassOnce}
}
