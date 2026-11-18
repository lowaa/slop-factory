package ticket

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/jobs"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/tenantstate"
)

// DefaultCooldownSweepInterval is how often expired cooldowns are looked for.
// A cooldown is at least minutes, so this only bounds how late one reopens.
const DefaultCooldownSweepInterval = 30 * time.Second

// CooldownSweeper reopens tickets whose cooldown has ended.
//
// Nothing else would: a cooled-down ticket is blocked, and a blocked ticket
// is re-judged only when some ticket's status changes. Time is the input that
// changes with nothing else happening, so it needs a timer of its own.
// Claim, Peek and the worker poll stay as they are -- once the ticket is open
// again, the next poll finds it.
type CooldownSweeper struct {
	store    *PostgresStore
	interval time.Duration
	logger   *slog.Logger
}

// NewCooldownSweeper sweeps every interval; Job is nil for a zero one.
func NewCooldownSweeper(store *PostgresStore, interval time.Duration, logHandler slog.Handler) *CooldownSweeper {
	return &CooldownSweeper{store: store, interval: interval, logger: slog.New(logHandler)}
}

// SweepOnce reopens every expired cooldown of every tenant. A tenant that
// fails does not stop the others; the first error is returned once all have
// been tried.
func (w *CooldownSweeper) SweepOnce(ctx context.Context) error {
	err := tenantstate.Each(ctx, tenantstate.Active(w.store.db.Global()), func(ctx context.Context, _ uuid.UUID) error {
		return w.store.reopenExpiredCooldowns(ctx)
	})
	if err != nil {
		return fmt.Errorf("cooldown sweep: %w", err)
	}
	return nil
}

// reopenExpiredCooldowns runs unblockSatisfied under the claim lock when a
// ticket is due. The check is made first and without the lock: the sweep runs
// every few seconds in every tenant, and almost always finds nothing.
func (s *PostgresStore) reopenExpiredCooldowns(ctx context.Context) error {
	var due bool
	if err := s.db.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM tickets t
			 WHERE t.status = 'blocked' AND t.blocked_until IS NOT NULL AND t.blocked_until <= now()
			   AND NOT `+hasUnfinishedDeps+`)`).Scan(&due); err != nil || !due {
		if err != nil {
			return fmt.Errorf("cooldown sweep: %w", err)
		}
		return nil
	}
	return s.underClaimLock(ctx, func(q db.Querier) error { return unblockSatisfied(ctx, q) })
}

// Job is the sweep as a background job, or nil when it is disabled. A failed
// pass is logged by the scheduler and the next one still runs, so a database
// blip cannot leave cooled tickets stranded.
func (w *CooldownSweeper) Job() *jobs.Job {
	if w.interval <= 0 {
		w.logger.Info("cooldown sweep disabled")
		return nil
	}
	return &jobs.Job{Name: "cooldown_sweep", Interval: w.interval, Run: w.SweepOnce}
}
