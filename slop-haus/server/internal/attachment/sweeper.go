package attachment

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/go-clock"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/jobs"
)

const (
	defaultSweepBatch    = 100
	defaultDeleteTimeout = 30 * time.Second
	// DefaultUploadGrace is how long a reserved key may wait for its row
	// before it is taken for an abandoned upload.
	DefaultUploadGrace = 24 * time.Hour
	// MinUploadGrace is the shortest grace allowed: a reservation younger than
	// an upload's Put and insert would have that upload's object deleted from
	// under the row it is about to get.
	MinUploadGrace = time.Hour
	// DefaultSweptRetention is how long a deletion's record is kept.
	DefaultSweptRetention = 90 * 24 * time.Hour
	DefaultSweepInterval  = 15 * time.Minute
)

// SweeperConfig tunes a Sweeper; a zero field takes its default.
type SweeperConfig struct {
	BatchSize     int
	DeleteTimeout time.Duration
	UploadGrace   time.Duration
	// SweptRetention is how long a swept deletion's record is kept.
	SweptRetention time.Duration
	Clock          clock.Clock
}

// SweepResult counts what a sweep did.
type SweepResult struct {
	Deleted int
	// Failed deletions stay queued for the next sweep.
	Failed int
	// StillReferenced were dropped without touching the store: a live row has
	// the same key.
	StillReferenced int
	// Abandoned reservations had their objects queued.
	Abandoned int
	// Removed attachments belonged to an archived tenant or a pruned conversation.
	Removed int
	Pruned  int
}

func (r *SweepResult) add(o SweepResult) {
	r.Deleted += o.Deleted
	r.Failed += o.Failed
	r.StillReferenced += o.StillReferenced
	r.Abandoned += o.Abandoned
	r.Removed += o.Removed
	r.Pruned += o.Pruned
}

// Sweeper deletes the objects nothing points at: those of deleted attachment
// rows (queued by a trigger in the deleting transaction, so a rollback never
// loses content a row still has) and those of uploads whose row never
// committed (expired reservations). It also removes the attachments of
// archived tenants and the staged ones of pruned conversations.
//
// Every step is safe to repeat: a sweep that dies after a store delete but
// before its commit leaves the row queued, and the next sweep deletes an
// already-missing object, which succeeds. It runs one tenant at a time in that
// tenant's own transaction, so row-level security bounds it to the tenant being
// swept, and the store calls are namespaced by the same tenant.
type Sweeper struct {
	repo   *Repository
	store  Store
	cfg    SweeperConfig
	logger *slog.Logger
}

func NewSweeper(repo *Repository, store Store, cfg *SweeperConfig, logHandler slog.Handler) (*Sweeper, error) {
	c := *cfg
	if c.BatchSize <= 0 {
		c.BatchSize = defaultSweepBatch
	}
	if c.DeleteTimeout <= 0 {
		c.DeleteTimeout = defaultDeleteTimeout
	}
	if c.UploadGrace == 0 {
		c.UploadGrace = DefaultUploadGrace
	}
	if c.UploadGrace < MinUploadGrace {
		return nil, fmt.Errorf("attachment: upload grace %s is shorter than the minimum %s", c.UploadGrace, MinUploadGrace)
	}
	if c.SweptRetention <= 0 {
		c.SweptRetention = DefaultSweptRetention
	}
	c.Clock = clock.Or(c.Clock)
	return &Sweeper{repo: repo, store: store, cfg: c, logger: slog.New(logHandler)}, nil
}

// Job runs the sweeper on the server's jobs runner.
func (s *Sweeper) Job(interval time.Duration) *jobs.Job {
	return &jobs.Job{Name: "attachment_sweep", Interval: interval, Jitter: interval / 10, Run: func(ctx context.Context) error {
		_, err := s.Sweep(ctx)
		return err
	}}
}

type tenantRef struct {
	id       uuid.UUID
	archived bool
}

func (s *Sweeper) tenants(ctx context.Context) ([]tenantRef, error) {
	rows, err := s.repo.db.Global().Query(ctx, `SELECT id, archived_at IS NOT NULL FROM global_tenants`)
	if err != nil {
		return nil, fmt.Errorf("attachment: list tenants: %w", err)
	}
	defer rows.Close()
	var out []tenantRef
	for rows.Next() {
		var t tenantRef
		if err := rows.Scan(&t.id, &t.archived); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// Sweep works through every tenant once. One tenant's failure does not stop
// the others; the errors come back joined.
func (s *Sweeper) Sweep(ctx context.Context) (SweepResult, error) {
	var total SweepResult
	tenants, err := s.tenants(ctx)
	if err != nil {
		return total, err
	}
	var errs []error
	for _, t := range tenants {
		res, err := s.sweepTenant(db.WithTenant(ctx, t.id), t)
		total.add(res)
		if err != nil {
			errs = append(errs, fmt.Errorf("tenant %s: %w", t.id, err))
		}
		if ctx.Err() != nil {
			errs = append(errs, ctx.Err())
			break
		}
	}
	err = errors.Join(errs...)
	if err != nil && ctx.Err() == nil {
		s.logger.ErrorContext(ctx, "attachment sweep failed", "error", err)
	}
	s.logger.InfoContext(ctx, "attachment sweep", "tenants", len(tenants), "deleted", total.Deleted, "failed", total.Failed,
		"still_referenced", total.StillReferenced, "abandoned", total.Abandoned, "removed", total.Removed, "pruned", total.Pruned)
	return total, err
}

// step does one batch of a tenant's work in q and says whether more may remain.
type step func(ctx context.Context, q db.Querier, now time.Time) (SweepResult, bool, error)

// sweepTenant removes what retention says goes first, so the objects of what
// it removed or expired are deleted in the same sweep.
func (s *Sweeper) sweepTenant(ctx context.Context, t tenantRef) (SweepResult, error) {
	var total SweepResult
	now := s.cfg.Clock.Now()
	steps := []step{s.expireBatch, s.removeStagedBatch}
	if t.archived {
		steps = append(steps, s.removeAllBatch)
	}
	steps = append(steps, s.deleteBatch, s.pruneBatch)
	for _, st := range steps {
		for {
			var res SweepResult
			var more bool
			err := s.repo.db.InTx(ctx, func(q db.Querier) (err error) {
				res, more, err = st(ctx, q, now)
				return err
			})
			total.add(res)
			if err != nil {
				return total, err
			}
			if !more {
				break
			}
		}
	}
	return total, nil
}

func (s *Sweeper) expireBatch(ctx context.Context, q db.Querier, now time.Time) (SweepResult, bool, error) {
	abandoned, claimed, err := s.repo.ExpireReservations(ctx, q, now.Add(-s.cfg.UploadGrace), s.cfg.BatchSize)
	return SweepResult{Abandoned: abandoned}, claimed == s.cfg.BatchSize, err
}

func (s *Sweeper) removeStagedBatch(ctx context.Context, q db.Querier, _ time.Time) (SweepResult, bool, error) {
	n, err := s.repo.DeleteStagedOfMissingConversations(ctx, q, s.cfg.BatchSize)
	return SweepResult{Removed: n}, n == s.cfg.BatchSize, err
}

func (s *Sweeper) removeAllBatch(ctx context.Context, q db.Querier, _ time.Time) (SweepResult, bool, error) {
	n, err := s.repo.DeleteAll(ctx, q, s.cfg.BatchSize)
	return SweepResult{Removed: n}, n == s.cfg.BatchSize, err
}

// deleteBatch deletes the objects of one claimed batch. A batch with a failure
// ends the tenant's deletions for this sweep: the failed rows would only be
// claimed again straight away.
func (s *Sweeper) deleteBatch(ctx context.Context, q db.Querier, now time.Time) (SweepResult, bool, error) {
	var res SweepResult
	tenantID, err := db.TenantFrom(ctx)
	if err != nil {
		return res, false, err
	}
	claimed, err := s.repo.ClaimDeletions(ctx, q, s.cfg.BatchSize)
	if err != nil || len(claimed) == 0 {
		return res, false, err
	}
	for _, d := range claimed {
		live, err := s.repo.KeyIsLive(ctx, q, d.StorageKey)
		if err != nil {
			return SweepResult{}, false, err
		}
		if live {
			// A key is only written under a fresh id, so this should not
			// happen; deleting here would destroy content a row needs.
			s.logger.WarnContext(ctx, "attachment sweep: deleted key still referenced, keeping object",
				"tenant_id", tenantID, "storage_key", d.StorageKey)
			res.StillReferenced++
			if err := s.repo.ClearDeletion(ctx, q, d.StorageKey); err != nil {
				return SweepResult{}, false, err
			}
			continue
		}
		if err := s.deleteObject(ctx, tenantID, d.StorageKey); err != nil {
			if ctx.Err() != nil {
				return SweepResult{}, false, ctx.Err()
			}
			s.logger.WarnContext(ctx, "attachment sweep: store delete failed, will retry",
				"tenant_id", tenantID, "storage_key", d.StorageKey, "attempts", d.Attempts+1, "error", err)
			if err := s.repo.RecordFailure(ctx, q, d.StorageKey, err.Error()); err != nil {
				return SweepResult{}, false, err
			}
			res.Failed++
			continue
		}
		if err := s.repo.MarkSwept(ctx, q, d.StorageKey, now); err != nil {
			return SweepResult{}, false, err
		}
		res.Deleted++
	}
	return res, len(claimed) == s.cfg.BatchSize && res.Failed == 0, nil
}

func (s *Sweeper) pruneBatch(ctx context.Context, q db.Querier, now time.Time) (SweepResult, bool, error) {
	n, err := s.repo.PruneSwept(ctx, q, now.Add(-s.cfg.SweptRetention), s.cfg.BatchSize)
	return SweepResult{Pruned: n}, n == s.cfg.BatchSize, err
}

func (s *Sweeper) deleteObject(ctx context.Context, tenantID uuid.UUID, key string) error {
	ctx, cancel := context.WithTimeout(ctx, s.cfg.DeleteTimeout)
	defer cancel()
	return s.store.Delete(ctx, tenantID, key)
}
