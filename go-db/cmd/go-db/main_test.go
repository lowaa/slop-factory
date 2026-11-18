package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// These tests exercise run() -- the flag-parsing/dispatch layer -- rather
// than re-testing migrationgen's own scaffolding logic (covered in
// internal/migrationgen's tests); the thing worth proving here is that the
// CLI wires -f/-n/--tenant-scoped through correctly.

func TestRunCreateMigration(t *testing.T) {
	dir := t.TempDir()

	if err := run([]string{"create-migration", "-f", dir, "-n", "widgets"}); err != nil {
		t.Fatalf("run: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, "000001_widgets.sql")); err != nil {
		t.Errorf("stat migration file: %v", err)
	}
}

func TestRunCreateMigrationTenantScoped(t *testing.T) {
	dir := t.TempDir()

	if err := run([]string{"create-migration", "-f", dir, "-n", "accounts", "--tenant-scoped"}); err != nil {
		t.Fatalf("run: %v", err)
	}

	content, err := os.ReadFile(filepath.Join(dir, "000001_accounts.sql"))
	if err != nil {
		t.Fatalf("read migration file: %v", err)
	}
	if !strings.Contains(string(content), "go_db_apply_tenant_rls") {
		t.Errorf("migration file missing go_db_apply_tenant_rls call:\n%s", content)
	}
}

func TestRunCreateMigrationMissingRequiredFlags(t *testing.T) {
	dir := t.TempDir()

	cases := [][]string{
		{"create-migration", "-n", "widgets"},
		{"create-migration", "-f", dir},
		{"create-migration"},
	}
	for _, args := range cases {
		if err := run(args); err == nil {
			t.Errorf("run(%v): want error, got nil", args)
		}
	}
}

func TestRunUnknownCommand(t *testing.T) {
	if err := run([]string{"bogus"}); err == nil {
		t.Error("run: want error for unknown command, got nil")
	}
}

func TestRunNoCommand(t *testing.T) {
	if err := run(nil); err == nil {
		t.Error("run: want error when no command given, got nil")
	}
}
