package migrations_test

import (
	"testing"
	"testing/fstest"

	"github.com/lowaa/slop-factory/go-db/migrations"
)

// TestLintGlobalTenantInvariants dogfoods the lint check against this
// package's own real test fixture (testMigrations, declared in
// migrations_test.go). It doesn't prove much about detection logic on its
// own -- that's TestLintCatches*/TestLintPassesCleanSchema below -- but it
// does prove the fixture itself stays compliant.
func TestLintGlobalTenantInvariants(t *testing.T) {
	errs := migrations.LintGlobalTenantInvariants(testMigrations)
	for _, err := range errs {
		t.Error(err)
	}
}

func TestLintCatchesRLSOnGlobalTable(t *testing.T) {
	fsys := fstest.MapFS{
		"000001_bad.sql": &fstest.MapFile{Data: []byte(`
-- +goose Up
CREATE TABLE global_widgets (id uuid PRIMARY KEY);
ALTER TABLE global_widgets ENABLE ROW LEVEL SECURITY;
-- +goose Down
DROP TABLE global_widgets;
		`)},
	}
	errs := migrations.LintGlobalTenantInvariants(fsys)
	if len(errs) != 1 {
		t.Fatalf("got %d errs, want 1: %v", len(errs), errs)
	}
}

func TestLintCatchesTenantIDOnGlobalTable(t *testing.T) {
	fsys := fstest.MapFS{
		"000001_bad.sql": &fstest.MapFile{Data: []byte(`
-- +goose Up
CREATE TABLE global_widgets (
	id uuid PRIMARY KEY,
	tenant_id uuid NOT NULL
);
-- +goose Down
DROP TABLE global_widgets;
		`)},
	}
	errs := migrations.LintGlobalTenantInvariants(fsys)
	if len(errs) != 1 {
		t.Fatalf("got %d errs, want 1: %v", len(errs), errs)
	}
}

func TestLintPassesCleanSchema(t *testing.T) {
	fsys := fstest.MapFS{
		"000001_ok.sql": &fstest.MapFile{Data: []byte(`
-- +goose Up
CREATE TABLE global_widgets (id uuid PRIMARY KEY);

CREATE TABLE widgets (
	id uuid NOT NULL,
	tenant_id uuid NOT NULL,
	PRIMARY KEY (id)
);
ALTER TABLE widgets ENABLE ROW LEVEL SECURITY;
ALTER TABLE widgets FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON widgets
	USING (tenant_id = NULLIF(current_setting('app.current_tenant', true), '')::uuid);
-- +goose Down
DROP TABLE widgets;
DROP TABLE global_widgets;
		`)},
	}
	errs := migrations.LintGlobalTenantInvariants(fsys)
	if len(errs) != 0 {
		t.Fatalf("got %d errs, want 0: %v", len(errs), errs)
	}
}

func TestLintCatchesTenantTableMissingTenantIDColumn(t *testing.T) {
	fsys := fstest.MapFS{
		"000001_bad.sql": &fstest.MapFile{Data: []byte(`
-- +goose Up
CREATE TABLE widgets (id uuid PRIMARY KEY);
ALTER TABLE widgets ENABLE ROW LEVEL SECURITY;
ALTER TABLE widgets FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON widgets
	USING (tenant_id = NULLIF(current_setting('app.current_tenant', true), '')::uuid);
-- +goose Down
DROP TABLE widgets;
		`)},
	}
	errs := migrations.LintGlobalTenantInvariants(fsys)
	if len(errs) != 1 {
		t.Fatalf("got %d errs, want 1: %v", len(errs), errs)
	}
}

func TestLintCatchesTenantTableMissingEnableRLS(t *testing.T) {
	fsys := fstest.MapFS{
		"000001_bad.sql": &fstest.MapFile{Data: []byte(`
-- +goose Up
CREATE TABLE widgets (
	id uuid NOT NULL,
	tenant_id uuid NOT NULL,
	PRIMARY KEY (id)
);
ALTER TABLE widgets FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON widgets
	USING (tenant_id = NULLIF(current_setting('app.current_tenant', true), '')::uuid);
-- +goose Down
DROP TABLE widgets;
		`)},
	}
	errs := migrations.LintGlobalTenantInvariants(fsys)
	if len(errs) != 1 {
		t.Fatalf("got %d errs, want 1: %v", len(errs), errs)
	}
}

func TestLintCatchesTenantTableMissingForceRLS(t *testing.T) {
	fsys := fstest.MapFS{
		"000001_bad.sql": &fstest.MapFile{Data: []byte(`
-- +goose Up
CREATE TABLE widgets (
	id uuid NOT NULL,
	tenant_id uuid NOT NULL,
	PRIMARY KEY (id)
);
ALTER TABLE widgets ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON widgets
	USING (tenant_id = NULLIF(current_setting('app.current_tenant', true), '')::uuid);
-- +goose Down
DROP TABLE widgets;
		`)},
	}
	errs := migrations.LintGlobalTenantInvariants(fsys)
	if len(errs) != 1 {
		t.Fatalf("got %d errs, want 1: %v", len(errs), errs)
	}
}

func TestLintPassesTenantTableUsingHelperFunction(t *testing.T) {
	fsys := fstest.MapFS{
		"000001_ok.sql": &fstest.MapFile{Data: []byte(`
-- +goose Up
CREATE TABLE widgets (
	id uuid NOT NULL,
	tenant_id uuid NOT NULL,
	PRIMARY KEY (id)
);
SELECT go_db_apply_tenant_rls('widgets');
-- +goose Down
DROP TABLE widgets;
		`)},
	}
	errs := migrations.LintGlobalTenantInvariants(fsys)
	if len(errs) != 0 {
		t.Fatalf("got %d errs, want 0: %v", len(errs), errs)
	}
}

func TestLintCatchesTenantTableMissingPolicy(t *testing.T) {
	fsys := fstest.MapFS{
		"000001_bad.sql": &fstest.MapFile{Data: []byte(`
-- +goose Up
CREATE TABLE widgets (
	id uuid NOT NULL,
	tenant_id uuid NOT NULL,
	PRIMARY KEY (id)
);
ALTER TABLE widgets ENABLE ROW LEVEL SECURITY;
ALTER TABLE widgets FORCE ROW LEVEL SECURITY;
-- +goose Down
DROP TABLE widgets;
		`)},
	}
	errs := migrations.LintGlobalTenantInvariants(fsys)
	if len(errs) != 1 {
		t.Fatalf("got %d errs, want 1: %v", len(errs), errs)
	}
}
