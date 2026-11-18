package db_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
)

func TestInTx_CommitsOnSuccess(t *testing.T) {
	database := dbtest.WithDB(t)
	ctx := db.WithTenant(context.Background(), uuid.New())

	err := database.InTx(ctx, func(q db.Querier) error {
		_, err := q.Exec(ctx, `INSERT INTO tickets (title) VALUES ('committed')`)
		return err
	})
	require.NoError(t, err)

	var count int
	require.NoError(t, database.QueryRow(ctx,
		`SELECT count(*) FROM tickets WHERE title = 'committed'`).Scan(&count))
	assert.Equal(t, 1, count)
}

func TestInTx_RollsBackOnError(t *testing.T) {
	database := dbtest.WithDB(t)
	ctx := db.WithTenant(context.Background(), uuid.New())

	sentinel := errors.New("deliberate failure")
	err := database.InTx(ctx, func(q db.Querier) error {
		if _, err := q.Exec(ctx, `INSERT INTO tickets (title) VALUES ('rolled back')`); err != nil {
			return err
		}
		return sentinel
	})
	require.ErrorIs(t, err, sentinel)

	var count int
	require.NoError(t, database.QueryRow(ctx,
		`SELECT count(*) FROM tickets WHERE title = 'rolled back'`).Scan(&count))
	assert.Zero(t, count, "the failed transaction's row must not be visible")
}

func TestNew_FailsFastOnAnUnreachableDatabase(t *testing.T) {
	// A bad DSN must fail at startup rather than on the first request --
	// pgxpool.New alone is lazy, which is why db.New pings.
	_, err := db.New(context.Background(),
		"postgres://nobody:nobody@127.0.0.1:1/nothing?sslmode=disable&connect_timeout=1")
	require.Error(t, err)
}

// The point of InReadTx is that a multi-statement read cannot splice two
// instants together. Read committed -- what InTx gives -- takes a fresh
// snapshot per statement, so the second read below would see the
// concurrent insert and the caller would compute over a state that never
// existed at any one moment.
func TestInReadTx_SeesOneSnapshotAcrossStatements(t *testing.T) {
	database := dbtest.WithDB(t)
	ctx := db.WithTenant(context.Background(), uuid.New())

	_, err := database.Exec(ctx, `INSERT INTO tickets (title) VALUES ('before')`)
	require.NoError(t, err)

	var first, second int
	err = database.InReadTx(ctx, func(q db.Querier) error {
		if err := q.QueryRow(ctx, `SELECT count(*) FROM tickets`).Scan(&first); err != nil {
			return err
		}
		// A writer committing mid-read is the whole hazard, so commit one
		// on another connection between the two statements.
		if _, err := database.Exec(ctx, `INSERT INTO tickets (title) VALUES ('during')`); err != nil {
			return err
		}
		return q.QueryRow(ctx, `SELECT count(*) FROM tickets`).Scan(&second)
	})
	require.NoError(t, err)

	assert.Equal(t, first, second, "the snapshot must not move under the reader")

	var after int
	require.NoError(t, database.QueryRow(ctx, `SELECT count(*) FROM tickets`).Scan(&after))
	assert.Equal(t, first+1, after, "and the writer really did commit")
}

func TestInReadTx_RefusesToWrite(t *testing.T) {
	database := dbtest.WithDB(t)
	ctx := db.WithTenant(context.Background(), uuid.New())

	err := database.InReadTx(ctx, func(q db.Querier) error {
		_, err := q.Exec(ctx, `INSERT INTO tickets (title) VALUES ('nope')`)
		return err
	})
	require.Error(t, err, "a write inside a read transaction must not be silently accepted")
}

// A query with no tenant on its context is a wiring bug, and must fail
// loudly rather than run unscoped.
func TestQueriesOutsideATenantAreRefused(t *testing.T) {
	database := dbtest.WithDB(t)
	ctx := context.Background()

	_, err := database.Exec(ctx, `INSERT INTO tickets (title) VALUES ('x')`)
	require.ErrorIs(t, err, db.ErrNoTenant)
	_, err = database.Query(ctx, `SELECT id FROM tickets`)
	require.ErrorIs(t, err, db.ErrNoTenant)
	var id int64
	require.ErrorIs(t, database.QueryRow(ctx, `SELECT 1`).Scan(&id), db.ErrNoTenant)
	require.ErrorIs(t, database.InTx(ctx, func(db.Querier) error { return nil }), db.ErrNoTenant)
}

// Query and QueryRow outside InTx each get a transaction of their own,
// ended when the rows are closed or the row is scanned. An INSERT ...
// RETURNING goes through them too, so ending it must commit, not roll back.
func TestOneStatementQueriesCommitTheirWrites(t *testing.T) {
	database := dbtest.WithDB(t)
	ctx := db.WithTenant(context.Background(), uuid.New())

	var viaRow int64
	require.NoError(t, database.QueryRow(ctx,
		`INSERT INTO tickets (title) VALUES ('via row') RETURNING id`).Scan(&viaRow))

	rows, err := database.Query(ctx, `INSERT INTO tickets (title) VALUES ('via rows') RETURNING id`)
	require.NoError(t, err)
	for rows.Next() {
	}
	require.NoError(t, rows.Err())
	rows.Close()

	var count int
	require.NoError(t, database.QueryRow(ctx,
		`SELECT count(*) FROM tickets WHERE title IN ('via row', 'via rows')`).Scan(&count))
	assert.Equal(t, 2, count)
}

// The whole point: one tenant's rows are invisible to another, and to the
// global handle, which carries no tenant at all.
func TestTenantsSeeOnlyTheirOwnRows(t *testing.T) {
	database := dbtest.WithDB(t)
	a := db.WithTenant(context.Background(), uuid.New())
	b := db.WithTenant(context.Background(), uuid.New())

	_, err := database.Exec(a, `INSERT INTO tickets (title) VALUES ('a''s ticket')`)
	require.NoError(t, err)

	count := func(ctx context.Context, q db.Querier) int {
		var n int
		require.NoError(t, q.QueryRow(ctx, `SELECT count(*) FROM tickets`).Scan(&n))
		return n
	}
	assert.Equal(t, 1, count(a, database))
	assert.Equal(t, 0, count(b, database), "tenant b must not see tenant a's ticket")
	assert.Equal(t, 0, count(context.Background(), database.Global()),
		"the global handle carries no tenant, so tenant tables read as empty")
}

// The claim lock serializes one tenant's claims; another tenant's claims
// must not queue behind them.
func TestAdvisoryLocksAreScopedToTheTenant(t *testing.T) {
	database := dbtest.WithDB(t)
	a := db.WithTenant(context.Background(), uuid.New())
	b := db.WithTenant(context.Background(), uuid.New())

	held := make(chan struct{})
	release := make(chan struct{})
	holderDone := make(chan error, 1)
	go func() {
		holderDone <- database.InTx(a, func(q db.Querier) error {
			if err := db.TakeAdvisoryLock(a, q, db.ClaimLockKey); err != nil {
				return err
			}
			close(held)
			<-release
			return nil
		})
	}()
	<-held

	acquired := func(ctx context.Context) bool {
		timeout, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
		defer cancel()
		return database.InTx(timeout, func(q db.Querier) error {
			return db.TakeAdvisoryLock(timeout, q, db.ClaimLockKey)
		}) == nil
	}
	assert.True(t, acquired(b), "another tenant's claim lock is free")
	assert.False(t, acquired(a), "the same tenant's claim lock is held")

	close(release)
	require.NoError(t, <-holderDone)
}

// A write through Query -- an INSERT ... RETURNING -- is committed when the
// rows run out. A commit that fails there (a deferred constraint, here) must
// surface through rows.Err(), which callers check, rather than vanish.
func TestAQueryWhoseCommitFailsReportsIt(t *testing.T) {
	app, owner := dbtest.WithOwnerDB(t)
	_, err := owner.Global().Exec(context.Background(),
		`CREATE TABLE deferred_probe (n int UNIQUE DEFERRABLE INITIALLY DEFERRED)`)
	require.NoError(t, err)
	ctx := db.WithTenant(context.Background(), uuid.New())

	rows, err := app.Query(ctx, `INSERT INTO deferred_probe (n) VALUES (1), (1) RETURNING n`)
	require.NoError(t, err, "the statement itself succeeds; the constraint is checked at commit")
	defer rows.Close()
	for rows.Next() {
	}
	require.Error(t, rows.Err(), "the failed commit is reported")

	var count int
	require.NoError(t, owner.Global().QueryRow(context.Background(), `SELECT count(*) FROM deferred_probe`).Scan(&count))
	assert.Zero(t, count, "and nothing was kept")
}
