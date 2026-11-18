package demo_test

import (
	"context"
	"testing"

	"github.com/lowaa/slop-factory/go-db/engine"
	"github.com/lowaa/slop-factory/go-db/internal/dbtestutil"
	"github.com/lowaa/slop-factory/go-db/internal/demo"
	"github.com/lowaa/slop-factory/go-db/migrations"
)

// TestLintGlobalTenantInvariants is the demo schema's own lint coverage --
// migrations package's tests only lint its own minimal library fixture, so
// nothing else checks the demo migrations against the two global/tenant
// invariants unless this does.
func TestLintGlobalTenantInvariants(t *testing.T) {
	errs := migrations.LintGlobalTenantInvariants(demo.Migrations)
	for _, err := range errs {
		t.Error(err)
	}
}

// TestMigrateCreatesExpectedSchema proves demo.Migrations actually produces
// the schema cmd/demo-api and the tenancy/dbtest package tests all rely on.
func TestMigrateCreatesExpectedSchema(t *testing.T) {
	ctx := context.Background()

	ctr, err := dbtestutil.StartPostgres(ctx)
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}
	t.Cleanup(func() { _ = ctr.Terminate(context.Background()) })

	eng := engine.NewPostgres(ctr.ConnString)
	if err := eng.Connect(ctx); err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = eng.Close() })

	if err := migrations.Up(ctx, eng, demo.Migrations); err != nil {
		t.Fatalf("up: %v", err)
	}

	for _, table := range []string{"global_tenants", "global_currencies", "accounts", "account_items"} {
		var exists bool
		err := eng.QueryRowContext(ctx,
			`SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema = 'public' AND table_name = $1)`,
			table,
		).Scan(&exists)
		if err != nil {
			t.Fatalf("check table %s exists: %v", table, err)
		}
		if !exists {
			t.Errorf("table %s does not exist after Up", table)
		}
	}

	// Unlike the exists-check above (a fixed list of tables this test
	// specifically cares about), this discovers every table in the schema
	// and checks its RLS state against the global_ naming convention --
	// no list to keep in sync as tables are added.
	for _, err := range migrations.CheckLiveTenantInvariants(ctx, eng) {
		t.Error(err)
	}

	version, err := migrations.Version(ctx, eng, demo.Migrations)
	if err != nil {
		t.Fatalf("version: %v", err)
	}
	if version != 4 {
		t.Errorf("version = %d, want 4", version)
	}
}

func TestMigrationSequenceIsGaplessAndLatestIsCurrent(t *testing.T) {
	for _, err := range (&migrations.SequenceLint{}).Lint(demo.Migrations) {
		t.Error(err)
	}
}
