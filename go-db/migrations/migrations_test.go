package migrations_test

import (
	"context"
	"embed"
	"io/fs"
	"os"
	"testing"

	"github.com/lowaa/slop-factory/go-db/engine"
	"github.com/lowaa/slop-factory/go-db/internal/dbtestutil"
	"github.com/lowaa/slop-factory/go-db/migrations"
)

// testMigrationsRaw/testMigrations are a minimal, library-only fixture --
// one global table, one tenant-scoped table with RLS -- used to prove the
// generic Up/Down/Version wrapper and the lint check work, without this
// package's own tests depending on the demo schema (internal/demo).
//
//go:embed testdata
var testMigrationsRaw embed.FS

var testMigrations = mustSub(testMigrationsRaw, "testdata")

func mustSub(fsys embed.FS, dir string) fs.FS {
	sub, err := fs.Sub(fsys, dir)
	if err != nil {
		panic(err)
	}
	return sub
}

var testConnString string

func TestMain(m *testing.M) {
	ctx := context.Background()

	ctr, err := dbtestutil.StartDedicatedPostgres(ctx)
	if err != nil {
		panic(err)
	}
	testConnString = ctr.ConnString

	code := m.Run()
	_ = ctr.Terminate(ctx)
	os.Exit(code)
}

func newConnectedEngine(t *testing.T) engine.Engine {
	t.Helper()
	ctx := context.Background()

	pg := engine.NewPostgres(testConnString)
	if err := pg.Connect(ctx); err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = pg.Close() })
	return pg
}

func tableExists(t *testing.T, eng engine.Engine, name string) bool {
	t.Helper()
	var exists bool
	err := eng.QueryRowContext(context.Background(),
		`SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema = 'public' AND table_name = $1)`,
		name,
	).Scan(&exists)
	if err != nil {
		t.Fatalf("check table %s exists: %v", name, err)
	}
	return exists
}

func policyExists(t *testing.T, eng engine.Engine, table string) bool {
	t.Helper()
	var exists bool
	err := eng.QueryRowContext(context.Background(),
		`SELECT EXISTS (SELECT 1 FROM pg_policies WHERE tablename = $1)`,
		table,
	).Scan(&exists)
	if err != nil {
		t.Fatalf("check policy on %s exists: %v", table, err)
	}
	return exists
}

// TestUpCreatesExpectedSchema runs every migration and asserts the expected
// tables and RLS policies are (or are not) present, using
// information_schema/pg_policies introspection rather than trusting the
// migration files ran without checking.
func TestUpCreatesExpectedSchema(t *testing.T) {
	eng := newConnectedEngine(t)
	ctx := context.Background()

	if err := migrations.Up(ctx, eng, testMigrations); err != nil {
		t.Fatalf("up: %v", err)
	}
	t.Cleanup(func() {
		_ = migrations.Down(context.Background(), eng, testMigrations, 2)
	})

	for _, table := range []string{"global_widget_types", "widgets"} {
		if !tableExists(t, eng, table) {
			t.Errorf("table %s does not exist after Up", table)
		}
	}

	if policyExists(t, eng, "global_widget_types") {
		t.Error("global_widget_types has an RLS policy but should not")
	}
	if !policyExists(t, eng, "widgets") {
		t.Error("widgets has no RLS policy")
	}

	version, err := migrations.Version(ctx, eng, testMigrations)
	if err != nil {
		t.Fatalf("version: %v", err)
	}
	if version != 2 {
		t.Errorf("version = %d, want 2", version)
	}
}

// TestDownRevertsCleanly runs Up, then Down all the way, and asserts every
// table this fixture creates is gone.
func TestDownRevertsCleanly(t *testing.T) {
	eng := newConnectedEngine(t)
	ctx := context.Background()

	if err := migrations.Up(ctx, eng, testMigrations); err != nil {
		t.Fatalf("up: %v", err)
	}
	if err := migrations.Down(ctx, eng, testMigrations, 2); err != nil {
		t.Fatalf("down: %v", err)
	}

	for _, table := range []string{"global_widget_types", "widgets"} {
		if tableExists(t, eng, table) {
			t.Errorf("table %s still exists after Down", table)
		}
	}

	version, err := migrations.Version(ctx, eng, testMigrations)
	if err != nil {
		t.Fatalf("version: %v", err)
	}
	if version != 0 {
		t.Errorf("version = %d, want 0 after full revert", version)
	}
}

// TestUpIsIdempotentAfterDownUp proves Up -> Down -> Up leaves the schema in
// the same state as a single Up (the "idempotency check" from the design
// doc's Mode C).
func TestUpIsIdempotentAfterDownUp(t *testing.T) {
	eng := newConnectedEngine(t)
	ctx := context.Background()

	if err := migrations.Up(ctx, eng, testMigrations); err != nil {
		t.Fatalf("first up: %v", err)
	}
	if err := migrations.Down(ctx, eng, testMigrations, 2); err != nil {
		t.Fatalf("down: %v", err)
	}
	if err := migrations.Up(ctx, eng, testMigrations); err != nil {
		t.Fatalf("second up: %v", err)
	}
	t.Cleanup(func() {
		_ = migrations.Down(context.Background(), eng, testMigrations, 2)
	})

	for _, table := range []string{"global_widget_types", "widgets"} {
		if !tableExists(t, eng, table) {
			t.Errorf("table %s does not exist after second Up", table)
		}
	}
}
