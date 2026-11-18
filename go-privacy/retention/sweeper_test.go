package retention_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-db/dbtest"
	"github.com/lowaa/slop-factory/go-db/engine"
	"github.com/lowaa/slop-factory/go-db/tenancy"
	"github.com/lowaa/slop-factory/go-privacy/retention"
	"github.com/lowaa/slop-factory/go-privacy/retention/retentiontest"
)

func TestMain(m *testing.M) {
	dbtest.Main(m, retentiontest.Migrations())
}

var now = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

const period = 30 * 24 * time.Hour

func newSweeper(cfg retention.Config) *retention.Sweeper {
	cfg.Now = func() time.Time { return now }
	return retention.NewSweeper(&cfg, slog.DiscardHandler)
}

func globalRule() *retention.Rule {
	return &retention.Rule{Class: "things", Table: "global_things", TimeColumn: "created_at", Period: period}
}

func tenantRule() *retention.Rule {
	return &retention.Rule{Class: "tenant_things", Table: "tenant_things", TimeColumn: "created_at", Period: period}
}

func age(d time.Duration) time.Time { return now.Add(-d) }

func insertGlobal(t *testing.T, eng engine.Engine, created time.Time) uuid.UUID {
	t.Helper()
	id := uuid.New()
	_, err := eng.ExecContext(t.Context(), `INSERT INTO global_things (id, created_at) VALUES ($1, $2)`, id, created)
	require.NoError(t, err)
	return id
}

func insertTenant(t *testing.T, eng engine.Engine, tenant uuid.UUID, created time.Time) uuid.UUID {
	t.Helper()
	tx, err := tenancy.NewTenantTx(t.Context(), eng, tenant)
	require.NoError(t, err)
	id := uuid.New()
	_, err = tx.ExecContext(t.Context(), `INSERT INTO tenant_things (id, tenant_id, created_at) VALUES ($1, $2, $3)`, id, tenant, created)
	require.NoError(t, err)
	require.NoError(t, tx.Commit(t.Context()))
	return id
}

func globalIDs(t *testing.T, eng engine.Engine) map[uuid.UUID]bool {
	t.Helper()
	return ids(t, eng, `SELECT id FROM global_things`)
}

func tenantIDs(t *testing.T, eng engine.Engine, tenant uuid.UUID) map[uuid.UUID]bool {
	t.Helper()
	tx, err := tenancy.NewTenantTx(t.Context(), eng, tenant)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(context.Background()) }()
	return ids(t, tx, `SELECT id FROM tenant_things`)
}

func ids(t *testing.T, q engine.Querier, query string) map[uuid.UUID]bool {
	t.Helper()
	rows, err := q.QueryContext(t.Context(), query)
	require.NoError(t, err)
	defer rows.Close()
	out := map[uuid.UUID]bool{}
	for rows.Next() {
		var id uuid.UUID
		require.NoError(t, rows.Scan(&id))
		out[id] = true
	}
	require.NoError(t, rows.Err())
	return out
}

func TestDeletesOnlyExpiredRows(t *testing.T) {
	eng := dbtest.WithCommittedDB(t)
	old := insertGlobal(t, eng, age(period+time.Hour))
	older := insertGlobal(t, eng, age(period*10))
	// Just inside the period, and exactly on its edge, are not expired.
	inside := insertGlobal(t, eng, age(period-time.Hour))
	edge := insertGlobal(t, eng, age(period))
	fresh := insertGlobal(t, eng, now)

	res, err := newSweeper(retention.Config{}).Sweep(t.Context(), globalRule(), retention.NewGlobalScope(eng), nil)
	require.NoError(t, err)

	require.EqualValues(t, 2, res.Deleted)
	require.Equal(t, map[uuid.UUID]bool{inside: true, edge: true, fresh: true}, globalIDs(t, eng))
	require.NotContains(t, globalIDs(t, eng), old)
	require.NotContains(t, globalIDs(t, eng), older)
}

func TestStaysWithinItsScope(t *testing.T) {
	eng := dbtest.WithCommittedDB(t)
	a, b := uuid.New(), uuid.New()
	aOld := insertTenant(t, eng, a, age(period*2))
	aFresh := insertTenant(t, eng, a, now)
	bOld := insertTenant(t, eng, b, age(period*2))

	// Only tenant a is swept. The rule's SQL names no tenant: that b's
	// expired row survives is row-level security doing its job.
	only := func(context.Context) ([]uuid.UUID, error) { return []uuid.UUID{a}, nil }
	res, err := newSweeper(retention.Config{}).Sweep(t.Context(), tenantRule(), retention.NewTenantScopes(eng, only), nil)
	require.NoError(t, err)

	require.EqualValues(t, 1, res.Deleted)
	require.Equal(t, map[uuid.UUID]bool{aFresh: true}, tenantIDs(t, eng, a))
	require.Equal(t, map[uuid.UUID]bool{bOld: true}, tenantIDs(t, eng, b))
	require.NotContains(t, tenantIDs(t, eng, a), aOld)

	// Both tenants listed: each is swept in its own scope.
	both := func(context.Context) ([]uuid.UUID, error) { return []uuid.UUID{a, b}, nil }
	res, err = newSweeper(retention.Config{}).Sweep(t.Context(), tenantRule(), retention.NewTenantScopes(eng, both), nil)
	require.NoError(t, err)
	require.EqualValues(t, 1, res.Deleted)
	require.Empty(t, tenantIDs(t, eng, b))
	require.Len(t, tenantIDs(t, eng, a), 1)
}

// Without a tenant the app role sees no rows of a tenant table, so a rule
// run in the global scope against it deletes nothing rather than everything.
func TestTenantTableOutsideATenantIsUntouched(t *testing.T) {
	eng := dbtest.WithCommittedDB(t)
	tenant := uuid.New()
	old := insertTenant(t, eng, tenant, age(period*2))

	res, err := newSweeper(retention.Config{}).Sweep(t.Context(), tenantRule(), retention.NewGlobalScope(eng), nil)
	require.NoError(t, err)

	require.Zero(t, res.Deleted)
	require.Equal(t, map[uuid.UUID]bool{old: true}, tenantIDs(t, eng, tenant))
}

// recordingScopes wraps Scopes to see each batch's transaction.
type recordingScopes struct {
	retention.Scopes
	deletes []int64
	failFor map[uuid.UUID]bool
}

func (r *recordingScopes) Tx(ctx context.Context, id uuid.UUID, fn func(q engine.Querier) error) error {
	if r.failFor[id] {
		return errors.New("injected failure")
	}
	return r.Scopes.Tx(ctx, id, func(q engine.Querier) error {
		return fn(&recordingQuerier{Querier: q, scopes: r})
	})
}

type recordingQuerier struct {
	engine.Querier
	scopes *recordingScopes
}

func (q *recordingQuerier) ExecContext(ctx context.Context, query string, args ...any) (engine.Result, error) {
	tag, err := q.Querier.ExecContext(ctx, query, args...)
	if err == nil {
		q.scopes.deletes = append(q.scopes.deletes, tag.RowsAffected())
	}
	return tag, err
}

func TestDeletesInBoundedBatches(t *testing.T) {
	eng := dbtest.WithCommittedDB(t)
	for range 25 {
		insertGlobal(t, eng, age(period*2))
	}
	scopes := &recordingScopes{Scopes: retention.NewGlobalScope(eng)}

	res, err := newSweeper(retention.Config{BatchSize: 10}).Sweep(t.Context(), globalRule(), scopes, nil)
	require.NoError(t, err)

	require.EqualValues(t, 25, res.Deleted)
	require.Equal(t, []int64{10, 10, 5}, scopes.deletes, "no transaction deletes more than a batch")
	require.Empty(t, globalIDs(t, eng))
}

func TestIsIdempotentAndResumesAfterInterruption(t *testing.T) {
	eng := dbtest.WithCommittedDB(t)
	for range 25 {
		insertGlobal(t, eng, age(period*2))
	}
	survivor := insertGlobal(t, eng, now)

	// Cancelled after the first batch: what committed stays deleted, the
	// rest is left for the next run.
	ctx, cancel := context.WithCancel(t.Context())
	res, err := newSweeper(retention.Config{BatchSize: 10}).Sweep(ctx, globalRule(), retention.NewGlobalScope(eng), cancel)
	require.ErrorIs(t, err, context.Canceled)
	require.EqualValues(t, 10, res.Deleted)
	require.Len(t, globalIDs(t, eng), 16)

	res, err = newSweeper(retention.Config{BatchSize: 10}).Sweep(t.Context(), globalRule(), retention.NewGlobalScope(eng), nil)
	require.NoError(t, err)
	require.EqualValues(t, 15, res.Deleted)
	require.Equal(t, map[uuid.UUID]bool{survivor: true}, globalIDs(t, eng))

	res, err = newSweeper(retention.Config{BatchSize: 10}).Sweep(t.Context(), globalRule(), retention.NewGlobalScope(eng), nil)
	require.NoError(t, err)
	require.Zero(t, res.Deleted, "a repeat run finds nothing")
	require.Equal(t, map[uuid.UUID]bool{survivor: true}, globalIDs(t, eng))
}

func TestDryRunCountsAndDeletesNothing(t *testing.T) {
	eng := dbtest.WithCommittedDB(t)
	for range 3 {
		insertGlobal(t, eng, age(period*2))
	}
	insertGlobal(t, eng, now)

	res, err := newSweeper(retention.Config{DryRun: true, BatchSize: 2}).Sweep(t.Context(), globalRule(), retention.NewGlobalScope(eng), nil)
	require.NoError(t, err)

	require.True(t, res.DryRun)
	require.EqualValues(t, 3, res.WouldDelete)
	require.Zero(t, res.Deleted)
	require.Len(t, globalIDs(t, eng), 4)
}

// A row another transaction holds (as a claim path does) is skipped, not
// waited for: the sweep finishes while the lock is still held, and takes the
// row on a later run.
func TestSkipsLockedRowsInsteadOfWaiting(t *testing.T) {
	eng := dbtest.WithCommittedDB(t)
	locked := insertGlobal(t, eng, age(period*2))
	free := insertGlobal(t, eng, age(period*2))

	holder, err := eng.BeginTx(t.Context())
	require.NoError(t, err)
	defer func() { _ = holder.Rollback(context.Background()) }()
	_, err = holder.ExecContext(t.Context(), `SELECT id FROM global_things WHERE id = $1 FOR UPDATE`, locked)
	require.NoError(t, err)

	// Bounded so a sweep that waited on the lock fails instead of hanging.
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	res, err := newSweeper(retention.Config{}).Sweep(ctx, globalRule(), retention.NewGlobalScope(eng), nil)
	require.NoError(t, err)
	require.EqualValues(t, 1, res.Deleted)
	require.Equal(t, map[uuid.UUID]bool{locked: true}, globalIDs(t, eng))
	require.NotContains(t, globalIDs(t, eng), free)

	require.NoError(t, holder.Rollback(t.Context()))
	res, err = newSweeper(retention.Config{}).Sweep(t.Context(), globalRule(), retention.NewGlobalScope(eng), nil)
	require.NoError(t, err)
	require.EqualValues(t, 1, res.Deleted)
	require.Empty(t, globalIDs(t, eng))
}

func TestOneScopesFailureDoesNotStopTheOthers(t *testing.T) {
	eng := dbtest.WithCommittedDB(t)
	a, b := uuid.New(), uuid.New()
	insertTenant(t, eng, a, age(period*2))
	insertTenant(t, eng, b, age(period*2))
	both := func(context.Context) ([]uuid.UUID, error) { return []uuid.UUID{a, b}, nil }
	scopes := &recordingScopes{Scopes: retention.NewTenantScopes(eng, both), failFor: map[uuid.UUID]bool{a: true}}

	res, err := newSweeper(retention.Config{}).Sweep(t.Context(), tenantRule(), scopes, nil)

	require.ErrorContains(t, err, "injected failure")
	require.EqualValues(t, 1, res.Deleted)
	require.Len(t, tenantIDs(t, eng, a), 1)
	require.Empty(t, tenantIDs(t, eng, b))
}

func TestRefusesRulesThatCouldDeleteTheWrongRows(t *testing.T) {
	eng := dbtest.WithCommittedDB(t)
	keep := insertGlobal(t, eng, age(period*2))

	for name, mutate := range map[string]func(*retention.Rule){
		"zero period":       func(r *retention.Rule) { r.Period = 0 },
		"negative period":   func(r *retention.Rule) { r.Period = -time.Hour },
		"no class":          func(r *retention.Rule) { r.Class = "" },
		"table injection":   func(r *retention.Rule) { r.Table = `global_things"; DELETE FROM global_things; --` },
		"column injection":  func(r *retention.Rule) { r.TimeColumn = `created_at" < now() OR true OR "x` },
		"uppercase/quoted":  func(r *retention.Rule) { r.Table = `Global_Things` },
		"empty time column": func(r *retention.Rule) { r.TimeColumn = "" },
	} {
		t.Run(name, func(t *testing.T) {
			rule := globalRule()
			mutate(rule)
			_, err := newSweeper(retention.Config{}).Sweep(t.Context(), rule, retention.NewGlobalScope(eng), nil)
			require.ErrorIs(t, err, retention.ErrInvalidRule)
			require.Contains(t, globalIDs(t, eng), keep)
		})
	}
}

// failAfterExec runs a batch's statement and then fails the transaction, as
// a commit or a later statement failing would.
type failAfterExec struct{ engine.Querier }

func (q *failAfterExec) ExecContext(ctx context.Context, query string, args ...any) (engine.Result, error) {
	if _, err := q.Querier.ExecContext(ctx, query, args...); err != nil {
		return engine.Result{}, err
	}
	return engine.Result{}, errors.New("failed after the delete ran")
}

type failingBatches struct{ retention.Scopes }

func (f failingBatches) Tx(ctx context.Context, id uuid.UUID, fn func(q engine.Querier) error) error {
	return f.Scopes.Tx(ctx, id, func(q engine.Querier) error { return fn(&failAfterExec{Querier: q}) })
}

func TestABatchThatFailsDeletesNothing(t *testing.T) {
	eng := dbtest.WithCommittedDB(t)
	for range 3 {
		insertGlobal(t, eng, age(period*2))
	}

	res, err := newSweeper(retention.Config{}).Sweep(t.Context(), globalRule(), failingBatches{retention.NewGlobalScope(eng)}, nil)

	require.ErrorContains(t, err, "failed after the delete ran")
	require.Zero(t, res.Deleted)
	require.Len(t, globalIDs(t, eng), 3, "the batch rolled back")
}

func TestWhereNarrowsWhatIsDeleted(t *testing.T) {
	eng := dbtest.WithCommittedDB(t)
	kept := insertGlobal(t, eng, age(2*period))
	gone := insertGlobal(t, eng, age(2*period))
	fresh := insertGlobal(t, eng, age(time.Hour))

	rule := globalRule()
	rule.Where = fmt.Sprintf(`id <> '%s'`, kept)
	res, err := newSweeper(retention.Config{}).Sweep(t.Context(), rule, retention.NewGlobalScope(eng), nil)
	require.NoError(t, err)

	require.EqualValues(t, 1, res.Deleted)
	require.Equal(t, map[uuid.UUID]bool{kept: true, fresh: true}, globalIDs(t, eng))
	require.NotContains(t, globalIDs(t, eng), gone)
}
