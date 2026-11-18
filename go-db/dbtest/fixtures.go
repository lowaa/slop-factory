package dbtest

import (
	"context"
	"fmt"
	"testing"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/go-db/engine"
)

// SeedTenants inserts n rows into global_tenants (a global table -- reached
// here via a plain Querier, no tenant context needed) against eng, and
// registers cleanup deleting them via t.Cleanup. It's usable against either
// mode's Engine: the package's shared AppEngine() for Mode A tests, or a
// per-test WithCommittedDB(t) engine for Mode B tests.
func SeedTenants(t *testing.T, eng engine.Engine, n int) []uuid.UUID {
	t.Helper()
	ctx := context.Background()

	tx, err := eng.BeginTx(ctx)
	if err != nil {
		t.Fatalf("dbtest: seed tenants: begin tx: %v", err)
	}

	ids := make([]uuid.UUID, n)
	for i := range ids {
		id := uuid.New()
		ids[i] = id
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO global_tenants (id, name) VALUES ($1, $2)`,
			id, fmt.Sprintf("test-tenant-%s", id),
		); err != nil {
			_ = tx.Rollback(ctx)
			t.Fatalf("dbtest: seed tenants: insert: %v", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("dbtest: seed tenants: commit: %v", err)
	}

	t.Cleanup(func() {
		cctx := context.Background()
		cleanupTx, err := eng.BeginTx(cctx)
		if err != nil {
			t.Logf("dbtest: seed tenants cleanup: begin tx: %v", err)
			return
		}
		for _, id := range ids {
			if _, err := cleanupTx.ExecContext(cctx, `DELETE FROM global_tenants WHERE id = $1`, id); err != nil {
				t.Logf("dbtest: seed tenants cleanup: delete %s: %v", id, err)
			}
		}
		if err := cleanupTx.Commit(cctx); err != nil {
			t.Logf("dbtest: seed tenants cleanup: commit: %v", err)
		}
	})

	return ids
}
