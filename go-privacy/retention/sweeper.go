// Package retention deletes data that has outlived its retention period:
// "delete rows older than N, in bounded batches, per scope, as the app
// role". It is the mechanism behind a data inventory retention class whose
// data lives in a database table; the period itself is the class's, set in
// the app's config and checked against the inventory (inventorytest).
package retention

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"time"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/go-db/engine"
)

// DefaultBatchSize bounds the rows one transaction deletes, so a backlog is
// worked off in short transactions rather than one long lock.
const DefaultBatchSize = 500

// Rule is one retention class's table: rows whose TimeColumn is older than
// Period are expired.
type Rule struct {
	// Class is the inventory retention class this enforces; it labels the
	// log lines and the result.
	Class string
	// Table and TimeColumn are identifiers, checked against a strict
	// pattern and quoted. They are the app's constants, never input.
	Table      string
	TimeColumn string
	// Period is how long a row's TimeColumn must be in the past before the
	// row is expired. It must be positive: zero would mean everything.
	Period time.Duration
	// Where, if set, narrows the expired rows to those it also matches: a
	// SQL boolean expression over the table's columns, the app's constant
	// and never input. The only parameter it may use is $1, the cutoff.
	Where string
}

var identifier = regexp.MustCompile(`^[a-z_][a-z0-9_]{0,62}$`)

// ErrInvalidRule is returned for a rule that could delete the wrong rows.
var ErrInvalidRule = errors.New("retention: invalid rule")

func (r *Rule) validate() error {
	switch {
	case r.Class == "":
		return fmt.Errorf("%w: no class", ErrInvalidRule)
	case !identifier.MatchString(r.Table):
		return fmt.Errorf("%w: table %q is not a plain identifier", ErrInvalidRule, r.Table)
	case !identifier.MatchString(r.TimeColumn):
		return fmt.Errorf("%w: time column %q is not a plain identifier", ErrInvalidRule, r.TimeColumn)
	case r.Period <= 0:
		return fmt.Errorf("%w: class %s has period %s; a period must be positive", ErrInvalidRule, r.Class, r.Period)
	}
	return nil
}

// predicate is the expired-row condition, with the cutoff as $1.
func (r *Rule) predicate() string {
	if r.Where != "" {
		return fmt.Sprintf(`"%s" < $1 AND (%s)`, r.TimeColumn, r.Where)
	}
	return fmt.Sprintf(`"%s" < $1`, r.TimeColumn)
}

// Config tunes a Sweeper.
type Config struct {
	// BatchSize is how many rows one transaction deletes. Zero takes
	// DefaultBatchSize.
	BatchSize int
	// DryRun counts what would be deleted and deletes nothing.
	DryRun bool
	// Now is the clock the period is measured on. Zero takes time.Now.
	Now func() time.Time
}

// Sweeper enforces Rules.
type Sweeper struct {
	cfg    Config
	logger *slog.Logger
}

func NewSweeper(cfg *Config, logHandler slog.Handler) *Sweeper {
	c := *cfg
	if c.BatchSize <= 0 {
		c.BatchSize = DefaultBatchSize
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	return &Sweeper{cfg: c, logger: slog.New(logHandler)}
}

// Result counts what one Sweep did.
type Result struct {
	Class  string
	Scopes int
	// Deleted rows were removed. Zero in a dry run.
	Deleted int64
	// WouldDelete is what a dry run found expired. Zero when not dry.
	WouldDelete int64
	DryRun      bool
}

// Sweep deletes rule's expired rows in every scope. Each batch is its own
// transaction; a rule that dies part way leaves its committed batches
// deleted and the rest for the next run, so a run can be repeated or
// retried at any point. One scope's failure doesn't stop the others: the
// errors come back joined, with the counts of what did happen. progress, if
// set, is called after every batch so a caller can report liveness through
// a long sweep.
func (s *Sweeper) Sweep(ctx context.Context, rule *Rule, scopes Scopes, progress func()) (Result, error) {
	res := Result{Class: rule.Class, DryRun: s.cfg.DryRun}
	if err := rule.validate(); err != nil {
		return res, err
	}
	if progress == nil {
		progress = func() {}
	}
	ids, err := scopes.List(ctx)
	if err != nil {
		return res, fmt.Errorf("retention: list scopes for %s: %w", rule.Class, err)
	}
	cutoff := s.cfg.Now().Add(-rule.Period)
	var errs []error
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			errs = append(errs, err)
			break
		}
		n, err := s.sweepScope(ctx, rule, scopes, id, cutoff, progress)
		res.Scopes++
		if s.cfg.DryRun {
			res.WouldDelete += n
		} else {
			res.Deleted += n
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("retention: %s in scope %s: %w", rule.Class, id, err))
		}
	}
	s.logger.InfoContext(ctx, "retention sweep",
		"class", rule.Class, "dry_run", res.DryRun, "scopes", res.Scopes,
		"deleted", res.Deleted, "would_delete", res.WouldDelete, "cutoff", cutoff.UTC())
	return res, errors.Join(errs...)
}

func (s *Sweeper) sweepScope(ctx context.Context, rule *Rule, scopes Scopes, id uuid.UUID, cutoff time.Time, progress func()) (int64, error) {
	if s.cfg.DryRun {
		var n int64
		err := scopes.Tx(ctx, id, func(q engine.Querier) error {
			return q.QueryRowContext(ctx,
				fmt.Sprintf(`SELECT count(*) FROM "%s" WHERE %s`, rule.Table, rule.predicate()), cutoff).Scan(&n)
		})
		return n, err
	}

	// SKIP LOCKED: a row some other transaction holds (a claim path
	// updating it) is not expired in any way that matters this run, and
	// waiting for it would hold this batch's locks while it waits. ctid
	// finds the same rows again for the delete without needing to know the
	// table's key.
	stmt := fmt.Sprintf(`
		DELETE FROM "%[1]s" WHERE ctid IN (
			SELECT ctid FROM "%[1]s" WHERE %[2]s
			ORDER BY "%[3]s" LIMIT $2
			FOR UPDATE SKIP LOCKED)`,
		rule.Table, rule.predicate(), rule.TimeColumn)
	var total int64
	for {
		var deleted int64
		err := scopes.Tx(ctx, id, func(q engine.Querier) error {
			tag, err := q.ExecContext(ctx, stmt, cutoff, s.cfg.BatchSize)
			if err != nil {
				return err
			}
			deleted = tag.RowsAffected()
			return nil
		})
		if err != nil {
			return total, err
		}
		total += deleted
		progress()
		if deleted < int64(s.cfg.BatchSize) {
			return total, nil
		}
		if err := ctx.Err(); err != nil {
			return total, err
		}
	}
}
