package migrations_test

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/lowaa/slop-factory/go-db/engine"
	"github.com/lowaa/slop-factory/go-db/internal/dbtestutil"
	"github.com/lowaa/slop-factory/go-db/migrations"
)

// Replicas rolling out together each run EnsureRole at startup. GRANT and
// ALTER DEFAULT PRIVILEGES rewrite one shared catalog row (the schema's
// ACL, the default-ACL entry), and a session that finds another's
// in-flight update of that row waits for it and then fails with "tuple
// concurrently updated" (SQLSTATE XX000) rather than retrying -- which
// restarted an api pod during a kube upgrade.
//
// The overlap is forced, not hoped for: replica A runs EnsureRole through
// heldEngine, which keeps everything A did in one open transaction after
// EnsureRole returns. Replica B then runs EnsureRole for real, and only once
// Postgres reports B waiting on the advisory lock does A commit. Without
// serialisation B waits on A's catalog row instead, never on the advisory
// lock -- EnsureRole's retry would then paper over the conflict, so the
// test asks for the advisory wait specifically; with it, B is waiting to
// start and then finds A's work committed.
func TestEnsureRole_ConcurrentReplicasDoNotConflict(t *testing.T) {
	for _, tc := range []struct {
		name        string
		roleExisted bool
	}{
		// The kube-upgrade case: every replica finds the role already there
		// and goes straight to re-granting.
		{name: "role already exists", roleExisted: true},
		// A first deploy: both replicas find the role missing.
		{name: "role is new", roleExisted: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			admin := newConnectedEngine(t)
			role := fmt.Sprintf("concurrent_role_%t", tc.roleExisted)
			table := role + "_rows"
			if _, err := admin.ExecContext(ctx, `CREATE TABLE `+table+` (label text NOT NULL)`); err != nil {
				t.Fatalf("create table: %v", err)
			}
			t.Cleanup(func() {
				_, _ = admin.ExecContext(context.Background(), `DROP TABLE IF EXISTS `+table)
				dropRole(t, admin, role)
			})
			if tc.roleExisted {
				if err := migrations.EnsureRole(ctx, admin, role, "pw"); err != nil {
					t.Fatalf("pre-create role: %v", err)
				}
			}

			replicaA := newHeldEngine(ctx, t, newConnectedEngine(t))
			if err := migrations.EnsureRole(ctx, replicaA, role, "pw"); err != nil {
				t.Fatalf("replica A: ensure role: %v", err)
			}

			const appName = "ensure_role_replica_b"
			replicaB := newConnectedEngineAs(t, testConnString, appName)
			bDone := make(chan error, 1)
			go func() { bDone <- migrations.EnsureRole(ctx, replicaB, role, "pw") }()

			if err := waitForLockWait(ctx, admin, appName, "advisory", bDone); err != nil {
				t.Fatalf("replica B never waited on replica A: %v", err)
			}
			if err := replicaA.commit(ctx); err != nil {
				t.Fatalf("replica A: commit: %v", err)
			}
			if err := <-bDone; err != nil {
				t.Fatalf("replica B: ensure role: %v", err)
			}

			assertCanInsertAs(ctx, t, testConnString, role, table)
		})
	}
}

// The advisory lock only orders EnsureRole against itself. A statement
// that doesn't take it can still rewrite a catalog row EnsureRole's grants
// rewrite -- seen in an e2e stack, where the suite's
// before-each TRUNCATE (a new relfilenode, so a new pg_class row version
// for every table and, with RESTART IDENTITY, every sequence) landed while
// the worker was granting, and the worker exited on "tuple concurrently
// updated". GRANT takes no lock on the table, so nothing makes it wait for
// the TRUNCATE to finish first.
//
// As above, the overlap is forced: the TRUNCATE is held uncommitted until
// Postgres reports EnsureRole waiting on it, and only then committed.
func TestEnsureRole_RetriesWhenACatalogRowIsRewrittenUnderIt(t *testing.T) {
	for _, tc := range []struct {
		name string
		// rewrite is run, and held uncommitted, in another session; table
		// is a table in the public schema the role is granted.
		rewrite func(table string) string
	}{
		{
			name:    "truncate restart identity",
			rewrite: func(table string) string { return `TRUNCATE ` + table + ` RESTART IDENTITY` },
		},
		{
			// A grant issued outside EnsureRole, e.g. by a migration or by
			// hand: it rewrites the schema's ACL without the advisory lock.
			name:    "grant on the schema",
			rewrite: func(string) string { return `GRANT USAGE ON SCHEMA public TO PUBLIC` },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			admin := newConnectedEngine(t)
			role := "rewritten_role"
			table := role + "_rows"
			if _, err := admin.ExecContext(ctx, `CREATE TABLE `+table+` (id bigserial PRIMARY KEY, label text NOT NULL)`); err != nil {
				t.Fatalf("create table: %v", err)
			}
			t.Cleanup(func() {
				_, _ = admin.ExecContext(context.Background(), `DROP TABLE IF EXISTS `+table)
				dropRole(t, admin, role)
			})
			// The role exists, as in the stack that hit this, but holds no
			// grants: the insert at the end can only succeed if the attempt
			// after the conflict did the granting.
			if _, err := admin.ExecContext(ctx, `CREATE ROLE `+role+` LOGIN PASSWORD 'pw'`); err != nil {
				t.Fatalf("pre-create role: %v", err)
			}

			rewriter, err := newConnectedEngine(t).BeginTx(ctx)
			if err != nil {
				t.Fatalf("begin rewrite: %v", err)
			}
			t.Cleanup(func() { _ = rewriter.Rollback(context.Background()) })
			if _, err := rewriter.ExecContext(ctx, tc.rewrite(table)); err != nil {
				t.Fatalf("rewrite: %v", err)
			}

			const appName = "ensure_role_under_rewrite"
			replica := newConnectedEngineAs(t, testConnString, appName)
			done := make(chan error, 1)
			go func() { done <- migrations.EnsureRole(ctx, replica, role, "pw") }()

			if err := waitForLockWait(ctx, admin, appName, "transactionid", done); err != nil {
				t.Fatalf("EnsureRole never waited on the rewrite: %v", err)
			}
			if err := rewriter.Commit(ctx); err != nil {
				t.Fatalf("commit rewrite: %v", err)
			}
			if err := <-done; err != nil {
				t.Fatalf("ensure role: %v", err)
			}
			assertCanInsertAs(ctx, t, testConnString, role, table)
		})
	}
}

// Two sessions that each hold one catalog row and want the other's deadlock
// rather than conflict: EnsureRole's GRANT on the schema rewrites the
// schema's row, its GRANT ON ALL TABLES then wants a table's pg_class row,
// and a migration (here a TRUNCATE, then a grant on the schema) that took
// those two in the opposite order meets it halfway. Postgres aborts one
// with 40P01, which was seen in an e2e stack was EnsureRole and
// stopped the worker at startup.
//
// The victim is the session whose deadlock_timeout expires first, so the
// rewriter's is made long and EnsureRole is already waiting by the time the
// rewriter's second statement starts.
func TestEnsureRole_RetriesWhenDeadlockedWithAnotherSession(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	admin := newConnectedEngine(t)
	const role = "deadlocked_role"
	const table = role + "_rows"
	if _, err := admin.ExecContext(ctx, `CREATE TABLE `+table+` (id bigserial PRIMARY KEY, label text NOT NULL)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	t.Cleanup(func() {
		_, _ = admin.ExecContext(context.Background(), `DROP TABLE IF EXISTS `+table)
		dropRole(t, admin, role)
	})
	if _, err := admin.ExecContext(ctx, `CREATE ROLE `+role+` LOGIN PASSWORD 'pw'`); err != nil {
		t.Fatalf("pre-create role: %v", err)
	}

	rewriter, err := newConnectedEngineAs(t, testConnString, "deadlock_rewriter").BeginTx(ctx)
	if err != nil {
		t.Fatalf("begin rewrite: %v", err)
	}
	t.Cleanup(func() { _ = rewriter.Rollback(context.Background()) })
	// The rewriter must not be the one to run deadlock detection first.
	if _, err := rewriter.ExecContext(ctx, `SET LOCAL deadlock_timeout = '10s'`); err != nil {
		t.Fatalf("set deadlock_timeout: %v", err)
	}
	if _, err := rewriter.ExecContext(ctx, `TRUNCATE `+table+` RESTART IDENTITY`); err != nil {
		t.Fatalf("truncate: %v", err)
	}

	const appName = "ensure_role_deadlocked"
	replica := newConnectedEngineAs(t, testConnString, appName)
	done := make(chan error, 1)
	go func() { done <- migrations.EnsureRole(ctx, replica, role, "pw") }()

	if err := waitForLockWait(ctx, admin, appName, "transactionid", done); err != nil {
		t.Fatalf("EnsureRole never waited on the truncate: %v", err)
	}
	// EnsureRole holds the schema's row and waits for the table's; this
	// wants the schema's row while holding the table's.
	if _, err := rewriter.ExecContext(ctx, `GRANT USAGE ON SCHEMA public TO PUBLIC`); err != nil {
		t.Fatalf("rewriter's grant (it should outlive the deadlock): %v", err)
	}
	if err := rewriter.Commit(ctx); err != nil {
		t.Fatalf("commit rewrite: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("ensure role: %v", err)
	}
	assertCanInsertAs(ctx, t, testConnString, role, table)
}

// Roles are cluster-wide but advisory locks are per database, so EnsureRole
// run for one role from two databases of one cluster is not serialised,
// and the second database's CREATE ROLE can find the first's role either
// in flight or freshly committed.
func TestEnsureRole_SameRoleFromTwoDatabases(t *testing.T) {
	for _, tc := range []struct {
		name string
		// overlap runs the second database's EnsureRole and commits the
		// first's at the point the case is about.
		overlap func(ctx context.Context, t *testing.T, admin, second engine.Engine, first *heldEngine, ensure func(engine.Engine) error) error
	}{
		{
			// The two CREATE ROLEs meet on pg_authid's unique index: the
			// second waits for the first and then fails with 23505.
			name: "first commits while the second waits in CREATE ROLE",
			overlap: func(ctx context.Context, t *testing.T, admin, second engine.Engine, first *heldEngine, ensure func(engine.Engine) error) error {
				done := make(chan error, 1)
				go func() { done <- ensure(second) }()
				if err := waitForLockWait(ctx, admin, secondDBAppName, "transactionid", done); err != nil {
					t.Fatalf("second database never waited on the first: %v", err)
				}
				if err := first.commit(ctx); err != nil {
					t.Fatalf("first database: commit: %v", err)
				}
				return <-done
			},
		},
		{
			// The second looked the role up before the first committed and
			// creates it after: 42710, role already exists.
			name: "first commits between the second's look-up and its CREATE ROLE",
			overlap: func(ctx context.Context, t *testing.T, _, second engine.Engine, first *heldEngine, ensure func(engine.Engine) error) error {
				return ensure(&beforeCreateRoleEngine{Engine: second, hook: func() {
					if err := first.commit(ctx); err != nil {
						t.Errorf("first database: commit: %v", err)
					}
				}})
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			admin := newConnectedEngine(t)
			const role = "two_db_role"
			const otherDB = "ensure_role_other_db"
			if _, err := admin.ExecContext(ctx, `CREATE DATABASE `+otherDB); err != nil {
				t.Fatalf("create database: %v", err)
			}
			otherConnString, err := dbtestutil.WithDatabaseName(testConnString, otherDB)
			if err != nil {
				t.Fatalf("other db conn string: %v", err)
			}
			t.Cleanup(func() {
				_, _ = admin.ExecContext(context.Background(), `DROP DATABASE IF EXISTS `+otherDB+` WITH (FORCE)`)
				dropRole(t, admin, role)
			})
			second := newConnectedEngineAs(t, otherConnString, secondDBAppName)
			table := role + "_rows"
			if _, err := second.ExecContext(ctx, `CREATE TABLE `+table+` (label text NOT NULL)`); err != nil {
				t.Fatalf("create table: %v", err)
			}

			first := newHeldEngine(ctx, t, newConnectedEngine(t))
			if err := migrations.EnsureRole(ctx, first, role, "pw"); err != nil {
				t.Fatalf("first database: ensure role: %v", err)
			}

			ensure := func(eng engine.Engine) error { return migrations.EnsureRole(ctx, eng, role, "pw") }
			if err := tc.overlap(ctx, t, admin, second, first, ensure); err != nil {
				t.Fatalf("second database: ensure role: %v", err)
			}
			assertCanInsertAs(ctx, t, otherConnString, role, table)
		})
	}
}

const secondDBAppName = "ensure_role_second_db"

// beforeCreateRoleEngine runs hook once, inside EnsureRole's transaction,
// just before the statement that builds its CREATE ROLE -- after the
// transaction has looked the role up and found it missing.
type beforeCreateRoleEngine struct {
	engine.Engine
	hook func()
}

func (e *beforeCreateRoleEngine) BeginTx(ctx context.Context) (engine.Tx, error) {
	tx, err := e.Engine.BeginTx(ctx)
	if err != nil {
		return nil, err
	}
	return &beforeCreateRoleTx{Tx: tx, engine: e}, nil
}

type beforeCreateRoleTx struct {
	engine.Tx
	engine *beforeCreateRoleEngine
}

func (x *beforeCreateRoleTx) QueryRowContext(ctx context.Context, query string, args ...any) engine.Row {
	if x.engine.hook != nil && strings.Contains(query, "CREATE ROLE") {
		hook := x.engine.hook
		x.engine.hook = nil
		hook()
	}
	return x.Tx.QueryRowContext(ctx, query, args...)
}

func assertCanInsertAs(ctx context.Context, t *testing.T, connString, role, table string) {
	t.Helper()
	u, err := url.Parse(connString)
	if err != nil {
		t.Fatalf("parse conn string: %v", err)
	}
	u.User = url.UserPassword(role, "pw")
	appEng := engine.NewPostgres(u.String())
	if err := appEng.Connect(ctx); err != nil {
		t.Fatalf("connect as %s: %v", role, err)
	}
	defer func() { _ = appEng.Close() }()
	if _, err := appEng.ExecContext(ctx, `INSERT INTO `+table+` (label) VALUES ('x')`); err != nil {
		t.Fatalf("insert as %s: %v", role, err)
	}
}

// waitForLockWait returns once the session named appName is blocked on a
// heavyweight lock of the given kind (pg_stat_activity's wait_event:
// "advisory" for pg_advisory_xact_lock, "transactionid" for another
// transaction's in-flight write to the same row). It polls pg_stat_activity, which is the only place
// that state is visible from outside the session; the poll interval is a
// cost, not a correctness assumption. It gives up early if the session
// finishes without ever waiting.
func waitForLockWait(ctx context.Context, admin engine.Engine, appName, waitEvent string, done <-chan error) error {
	for {
		// A session waits on one lock at a time, so a wait on any other
		// kind is final: fail fast rather than at the context's deadline.
		var waitingOn string
		err := admin.QueryRowContext(ctx,
			`SELECT wait_event FROM pg_stat_activity WHERE application_name = $1 AND wait_event_type = 'Lock'`,
			appName).Scan(&waitingOn)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
		case err != nil:
			return err
		case waitingOn == waitEvent:
			return nil
		default:
			return fmt.Errorf("it is waiting on a %q lock, not %q", waitingOn, waitEvent)
		}
		select {
		case err := <-done:
			return fmt.Errorf("it finished first (err: %v)", err)
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func newConnectedEngineAs(t *testing.T, connString, appName string) engine.Engine {
	t.Helper()
	u, err := url.Parse(connString)
	if err != nil {
		t.Fatalf("parse conn string: %v", err)
	}
	q := u.Query()
	q.Set("application_name", appName)
	u.RawQuery = q.Encode()
	pg := engine.NewPostgres(u.String())
	if err := pg.Connect(context.Background()); err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = pg.Close() })
	return pg
}

func dropRole(t *testing.T, admin engine.Engine, role string) {
	t.Helper()
	ctx := context.Background()
	for _, stmt := range []string{
		`ALTER DEFAULT PRIVILEGES IN SCHEMA public REVOKE ALL ON TABLES FROM ` + pgxIdent(role),
		`ALTER DEFAULT PRIVILEGES IN SCHEMA public REVOKE ALL ON SEQUENCES FROM ` + pgxIdent(role),
		`DROP OWNED BY ` + pgxIdent(role),
		`DROP ROLE IF EXISTS ` + pgxIdent(role),
	} {
		if _, err := admin.ExecContext(ctx, stmt); err != nil {
			t.Logf("cleanup %q: %v", stmt, err)
		}
	}
}

// heldEngine stands in for a replica caught part way through EnsureRole:
// every statement run through it -- directly or in a transaction it hands
// out -- goes into one transaction that stays open, uncommitted, until the
// test calls commit. Transactions it hands out share that one: their
// Commit is deferred to the test's, and a Rollback before that Commit rolls
// it all back (one after it is a no-op, as for a real transaction).
type heldEngine struct {
	engine.Engine
	tx engine.Tx
}

func newHeldEngine(ctx context.Context, t *testing.T, eng engine.Engine) *heldEngine {
	t.Helper()
	tx, err := eng.BeginTx(ctx)
	if err != nil {
		t.Fatalf("begin held tx: %v", err)
	}
	h := &heldEngine{Engine: eng, tx: tx}
	t.Cleanup(func() { _ = h.tx.Rollback(context.Background()) })
	return h
}

func (h *heldEngine) ExecContext(ctx context.Context, query string, args ...any) (engine.Result, error) {
	return h.tx.ExecContext(ctx, query, args...)
}

func (h *heldEngine) QueryContext(ctx context.Context, query string, args ...any) (engine.Rows, error) {
	return h.tx.QueryContext(ctx, query, args...)
}

func (h *heldEngine) QueryRowContext(ctx context.Context, query string, args ...any) engine.Row {
	return h.tx.QueryRowContext(ctx, query, args...)
}

func (h *heldEngine) BeginTx(ctx context.Context) (engine.Tx, error) {
	return h.BeginTxWithOptions(ctx, engine.TxOptions{})
}

func (h *heldEngine) BeginTxWithOptions(_ context.Context, opts engine.TxOptions) (engine.Tx, error) {
	if opts != (engine.TxOptions{}) {
		return nil, errors.New("heldEngine: only default transaction options are supported")
	}
	return &heldTx{held: h}, nil
}

func (h *heldEngine) commit(ctx context.Context) error {
	return h.tx.Commit(ctx)
}

type heldTx struct {
	held      *heldEngine
	committed bool
}

func (x *heldTx) ExecContext(ctx context.Context, query string, args ...any) (engine.Result, error) {
	return x.held.ExecContext(ctx, query, args...)
}

func (x *heldTx) QueryContext(ctx context.Context, query string, args ...any) (engine.Rows, error) {
	return x.held.QueryContext(ctx, query, args...)
}

func (x *heldTx) QueryRowContext(ctx context.Context, query string, args ...any) engine.Row {
	return x.held.QueryRowContext(ctx, query, args...)
}

func (x *heldTx) Commit(context.Context) error {
	x.committed = true
	return nil
}

func (x *heldTx) Rollback(ctx context.Context) error {
	if x.committed {
		return nil
	}
	return x.held.tx.Rollback(ctx)
}
