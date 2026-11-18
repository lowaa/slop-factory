// Package migrationgen scaffolds new go-db-style migration files --
// "<version>_<name>.sql", carrying both directions via goose's "-- +goose
// Up"/"-- +goose Down" annotations (see the migrations package doc for the
// layout) -- assigning the next sequential version number by scanning the
// target directory. It backs cmd/go-db's create-migration subcommand; the
// CLI wiring lives there, this package is just the reusable, testable
// logic.
package migrationgen

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"

	"github.com/lowaa/slop-factory/go-db/migrations"
)

var (
	nameRe = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	fileRe = regexp.MustCompile(`^([0-9]+)_.+\.sql$`)
)

// versionWidth matches the zero-padding width already used by every
// existing migration file (e.g. "000001_init.sql").
const versionWidth = 6

// Create scaffolds a new migration file under dir: dir/<version>_<name>.sql,
// where <version> is one more than the highest existing version found in
// dir (or 1 if dir is empty or doesn't exist yet). It returns the created
// file's path. It also points dir/LATEST at the new migration, so a branch
// that races another to the same version conflicts on that file in git.
//
// If tenantScoped is true, the Up section is templated with the tenant_id
// column, a tenant_id-leading index, and a go_db_apply_tenant_rls call --
// the convention every tenant-scoped table follows (see
// migrations.LintGlobalTenantInvariants, which enforces it). Otherwise the
// Up section is a bare CREATE TABLE skeleton with a reminder comment, since
// a non-tenant-scoped table still needs a decision (global_-prefixed
// reference data, or something else) that shouldn't be templated away.
func Create(dir, name string, tenantScoped bool) (string, error) {
	if !nameRe.MatchString(name) {
		return "", fmt.Errorf("migrationgen: name %q must be lowercase snake_case (start with a letter, then letters/digits/underscores)", name)
	}

	next, err := nextVersion(dir)
	if err != nil {
		return "", fmt.Errorf("migrationgen: determine next version: %w", err)
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("migrationgen: create dir: %w", err)
	}

	base := fmt.Sprintf("%0*d_%s", versionWidth, next, name)
	file := filepath.Join(dir, base+".sql")
	if err := os.WriteFile(file, []byte(template(name, tenantScoped)), 0o644); err != nil {
		return "", fmt.Errorf("migrationgen: write migration: %w", err)
	}

	if err := os.WriteFile(filepath.Join(dir, migrations.LatestFileName), []byte(base+"\n"), 0o644); err != nil {
		return "", fmt.Errorf("migrationgen: write %s: %w", migrations.LatestFileName, err)
	}

	return file, nil
}

// nextVersion scans dir for existing "<version>_<name>.sql" files and
// returns one more than the highest version found. A missing dir is treated
// as empty (next version 1), since create-migration may be the very first
// thing run against a brand new migrations folder.
func nextVersion(dir string) (int, error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return 1, nil
	}
	if err != nil {
		return 0, err
	}

	highest := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		m := fileRe.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		v, err := strconv.Atoi(m[1])
		if err != nil {
			continue
		}
		if v > highest {
			highest = v
		}
	}
	return highest + 1, nil
}

func template(name string, tenantScoped bool) string {
	var up string
	if tenantScoped {
		up = fmt.Sprintf(`CREATE TABLE %[1]s (
    id uuid NOT NULL,
    tenant_id uuid NOT NULL,
    PRIMARY KEY (id),
    UNIQUE (tenant_id, id)
);

CREATE INDEX %[1]s_tenant_id_idx ON %[1]s (tenant_id, id);

SELECT go_db_apply_tenant_rls('%[1]s');`, name)
	} else {
		up = fmt.Sprintf(`-- TODO: add columns. If this table is genuinely global (shared across
-- all tenants, not scoped to one), prefix its name with global_ -- see
-- migrations.LintGlobalTenantInvariants. Otherwise, re-run with
-- --tenant-scoped instead of hand-adding tenant_id/RLS/policy.
CREATE TABLE %[1]s (
    id uuid PRIMARY KEY
);`, name)
	}

	return fmt.Sprintf(`-- +goose Up
%s

-- +goose Down
DROP TABLE IF EXISTS %s;
`, up, name)
}
