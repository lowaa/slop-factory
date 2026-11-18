package engine_test

import (
	"context"
	"os"
	"testing"

	"github.com/lowaa/slop-factory/go-db/engine"
	"github.com/lowaa/slop-factory/go-db/internal/dbtestutil"
)

var testConnString string

func TestMain(m *testing.M) {
	ctx := context.Background()

	ctr, err := dbtestutil.StartPostgres(ctx)
	if err != nil {
		panic(err)
	}
	testConnString = ctr.ConnString

	code := m.Run()
	_ = ctr.Terminate(ctx)
	os.Exit(code)
}

func newConnectedPostgres(t *testing.T) *engine.Postgres {
	t.Helper()
	ctx := context.Background()

	pg := engine.NewPostgres(testConnString)
	if err := pg.Connect(ctx); err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = pg.Close() })
	return pg
}

func TestConnectAndPing(t *testing.T) {
	pg := newConnectedPostgres(t)
	if err := pg.Ping(context.Background()); err != nil {
		t.Fatalf("ping: %v", err)
	}
}

func TestConnStringRoundTrip(t *testing.T) {
	pg := engine.NewPostgres(testConnString)
	if pg.ConnString() != testConnString {
		t.Fatalf("ConnString() = %q, want %q", pg.ConnString(), testConnString)
	}
}

func TestExecQueryQueryRow(t *testing.T) {
	pg := newConnectedPostgres(t)
	ctx := context.Background()

	// A regular (non-temp) table is used deliberately: pgxpool.Pool may
	// hand out a different physical connection to each Exec/Query call,
	// and Postgres temp tables are scoped to a single connection/session,
	// so a temp table created on one call can be invisible to the next.
	if _, err := pg.ExecContext(ctx, `CREATE TABLE engine_smoke (id int, name text)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	t.Cleanup(func() { _, _ = pg.ExecContext(context.Background(), `DROP TABLE IF EXISTS engine_smoke`) })
	if _, err := pg.ExecContext(ctx, `INSERT INTO engine_smoke (id, name) VALUES (1, 'a'), (2, 'b')`); err != nil {
		t.Fatalf("insert: %v", err)
	}

	var name string
	if err := pg.QueryRowContext(ctx, `SELECT name FROM engine_smoke WHERE id = $1`, 1).Scan(&name); err != nil {
		t.Fatalf("query row: %v", err)
	}
	if name != "a" {
		t.Fatalf("name = %q, want %q", name, "a")
	}

	rows, err := pg.QueryContext(ctx, `SELECT id, name FROM engine_smoke ORDER BY id`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()

	var got []string
	for rows.Next() {
		var id int
		var n string
		if err := rows.Scan(&id, &n); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, n)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows err: %v", err)
	}
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("got %v, want [a b]", got)
	}
}

func TestBeginTxCommit(t *testing.T) {
	pg := newConnectedPostgres(t)
	ctx := context.Background()

	if _, err := pg.ExecContext(ctx, `CREATE TABLE engine_tx_commit (id int)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	t.Cleanup(func() { _, _ = pg.ExecContext(context.Background(), `DROP TABLE IF EXISTS engine_tx_commit`) })

	tx, err := pg.BeginTx(ctx)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO engine_tx_commit (id) VALUES (1)`); err != nil {
		t.Fatalf("insert in tx: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}

	var count int
	if err := pg.QueryRowContext(ctx, `SELECT count(*) FROM engine_tx_commit`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Fatalf("count = %d, want 1", count)
	}
}

func TestBeginTxRollback(t *testing.T) {
	pg := newConnectedPostgres(t)
	ctx := context.Background()

	if _, err := pg.ExecContext(ctx, `CREATE TABLE engine_tx_rollback (id int)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	t.Cleanup(func() { _, _ = pg.ExecContext(context.Background(), `DROP TABLE IF EXISTS engine_tx_rollback`) })

	tx, err := pg.BeginTx(ctx)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO engine_tx_rollback (id) VALUES (1)`); err != nil {
		t.Fatalf("insert in tx: %v", err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatalf("rollback: %v", err)
	}

	var count int
	if err := pg.QueryRowContext(ctx, `SELECT count(*) FROM engine_tx_rollback`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Fatalf("count = %d, want 0", count)
	}
}

func TestBeginTxWithOptionsReadOnlyRejectsWrites(t *testing.T) {
	pg := newConnectedPostgres(t)
	ctx := context.Background()

	if _, err := pg.ExecContext(ctx, `CREATE TABLE engine_tx_readonly (id int)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	t.Cleanup(func() { _, _ = pg.ExecContext(context.Background(), `DROP TABLE IF EXISTS engine_tx_readonly`) })

	tx, err := pg.BeginTxWithOptions(ctx, engine.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.ExecContext(ctx, `INSERT INTO engine_tx_readonly (id) VALUES (1)`); err == nil {
		t.Fatal("insert in a read-only transaction succeeded; want it refused")
	}
}

// A repeatable-read transaction keeps the snapshot of its first query: a
// row committed by another connection afterwards stays invisible to it.
func TestBeginTxWithOptionsRepeatableReadHoldsItsSnapshot(t *testing.T) {
	pg := newConnectedPostgres(t)
	ctx := context.Background()

	if _, err := pg.ExecContext(ctx, `CREATE TABLE engine_tx_snapshot (id int)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	t.Cleanup(func() { _, _ = pg.ExecContext(context.Background(), `DROP TABLE IF EXISTS engine_tx_snapshot`) })

	tx, err := pg.BeginTxWithOptions(ctx, engine.TxOptions{Isolation: engine.IsolationRepeatableRead, ReadOnly: true})
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	count := func(q engine.Querier) int {
		var n int
		if err := q.QueryRowContext(ctx, `SELECT count(*) FROM engine_tx_snapshot`).Scan(&n); err != nil {
			t.Fatalf("count: %v", err)
		}
		return n
	}
	if got := count(tx); got != 0 {
		t.Fatalf("count before = %d, want 0", got)
	}
	if _, err := pg.ExecContext(ctx, `INSERT INTO engine_tx_snapshot (id) VALUES (1)`); err != nil {
		t.Fatalf("insert outside tx: %v", err)
	}
	if got := count(tx); got != 0 {
		t.Fatalf("count inside the snapshot = %d, want 0: the later commit should not be visible", got)
	}
}

func TestBeginTxWithOptionsRefusesAnUnknownIsolationLevel(t *testing.T) {
	pg := newConnectedPostgres(t)

	if _, err := pg.BeginTxWithOptions(context.Background(), engine.TxOptions{Isolation: "read sideways"}); err == nil {
		t.Fatal("unknown isolation level accepted; want an error")
	}
}
