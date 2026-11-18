package migrations

import (
	"fmt"
	"io/fs"
	"regexp"
	"strings"
)

var (
	createTableRe    = regexp.MustCompile(`(?is)CREATE TABLE\s+(\w+)\s*\(`)
	enableRLSRe      = regexp.MustCompile(`(?i)ALTER TABLE\s+(\w+)\s+ENABLE ROW LEVEL SECURITY`)
	forceRLSRe       = regexp.MustCompile(`(?i)ALTER TABLE\s+(\w+)\s+FORCE ROW LEVEL SECURITY`)
	createPolicyRe   = regexp.MustCompile(`(?i)CREATE POLICY\s+\S+\s+ON\s+(\w+)`)
	applyTenantRLSRe = regexp.MustCompile(`(?i)go_db_apply_tenant_rls\(\s*'(\w+)'\s*\)`)
	tenantIDColRe    = regexp.MustCompile(`(?i)^\s*tenant_id\b`)

	// gooseUpSectionRe pulls out the "-- +goose Up" section of a migration
	// file -- everything up to (not including) "-- +goose Down" or EOF --
	// since only the up direction is meaningful to lint here (the down
	// direction is just DROPs).
	gooseUpSectionRe = regexp.MustCompile(`(?is)--\s*\+goose\s+Up\b(.*?)(?:--\s*\+goose\s+Down\b|\z)`)
)

// LintGlobalTenantInvariants statically checks the "-- +goose Up" section of
// every *.sql migration file in fsys (rooted directly at the migration
// files, same as what Up/Down/Version expect) against the global/tenant
// classification rules:
//
//  1. A table prefixed "global_" never has ENABLE ROW LEVEL SECURITY
//     applied to it (global tables are never RLS-protected).
//  2. A table prefixed "global_" never has a tenant_id column.
//  3. A table NOT prefixed "global_" (i.e. tenant-scoped) has a tenant_id
//     column, ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY, and at
//     least one CREATE POLICY, all in the same file as its CREATE TABLE. A
//     call to go_db_apply_tenant_rls(table_name) counts as satisfying the
//     ENABLE/FORCE/policy trio on its own, since that's the one-line form
//     new migrations are expected to use instead of writing the three
//     statements out by hand.
//
// Rule 3 is what actually catches the mistake of forgetting the tenancy
// boilerplate on a new business table: Postgres RLS has no default-deny, so
// a table missing ENABLE/FORCE/policy is silently readable across tenants
// through a correctly-scoped TenantTx (the session variable simply goes
// unused) -- a schema-level gap the Go type system can't see.
//
// It needs no live database connection, so it runs as a plain unit test
// (see lint_test.go) instead of piggybacking on the dbtest harness -- static
// parsing is strictly cheaper since it needs no database at all. Callers should run this over every migration source
// in the repo (see internal/demo's own lint test), not just this package's
// test fixture.
func LintGlobalTenantInvariants(fsys fs.FS) []error {
	names, err := fs.Glob(fsys, "*.sql")
	if err != nil {
		return []error{fmt.Errorf("migrations: lint: glob: %w", err)}
	}

	var errs []error
	for _, name := range names {
		content, err := fs.ReadFile(fsys, name)
		if err != nil {
			errs = append(errs, fmt.Errorf("migrations: lint: read %s: %w", name, err))
			continue
		}
		errs = append(errs, lintFile(name, upSection(string(content)))...)
	}
	return errs
}

// upSection extracts the "-- +goose Up" portion of a migration file's
// content. A file with no such marker (shouldn't happen for a real
// migration, but easy to construct in a test fixture) lints as empty rather
// than erroring, since an empty Up section trivially satisfies every rule
// here.
func upSection(content string) string {
	m := gooseUpSectionRe.FindStringSubmatch(content)
	if m == nil {
		return ""
	}
	return m[1]
}

func lintFile(name, content string) []error {
	var errs []error

	enabledRLS := map[string]bool{}
	for _, m := range enableRLSRe.FindAllStringSubmatch(content, -1) {
		enabledRLS[m[1]] = true
	}
	forcedRLS := map[string]bool{}
	for _, m := range forceRLSRe.FindAllStringSubmatch(content, -1) {
		forcedRLS[m[1]] = true
	}
	policied := map[string]bool{}
	for _, m := range createPolicyRe.FindAllStringSubmatch(content, -1) {
		policied[m[1]] = true
	}
	for _, m := range applyTenantRLSRe.FindAllStringSubmatch(content, -1) {
		table := m[1]
		enabledRLS[table] = true
		forcedRLS[table] = true
		policied[table] = true
	}

	for table := range enabledRLS {
		if strings.HasPrefix(table, "global_") {
			errs = append(errs, fmt.Errorf("%s: table %q is prefixed global_ but has ENABLE ROW LEVEL SECURITY applied", name, table))
		}
	}

	for _, stmt := range strings.Split(content, ";") {
		m := createTableRe.FindStringSubmatch(stmt)
		if m == nil {
			continue
		}
		table := m[1]

		hasTenantIDCol := false
		for _, line := range strings.Split(stmt, "\n") {
			if tenantIDColRe.MatchString(line) {
				hasTenantIDCol = true
				break
			}
		}

		if strings.HasPrefix(table, "global_") {
			if hasTenantIDCol {
				errs = append(errs, fmt.Errorf("%s: global table %q has a tenant_id column", name, table))
			}
			continue
		}

		if !hasTenantIDCol {
			errs = append(errs, fmt.Errorf("%s: tenant-scoped table %q has no tenant_id column", name, table))
		}
		if !enabledRLS[table] {
			errs = append(errs, fmt.Errorf("%s: tenant-scoped table %q is missing ENABLE ROW LEVEL SECURITY", name, table))
		}
		if !forcedRLS[table] {
			errs = append(errs, fmt.Errorf("%s: tenant-scoped table %q is missing FORCE ROW LEVEL SECURITY", name, table))
		}
		if !policied[table] {
			errs = append(errs, fmt.Errorf("%s: tenant-scoped table %q has no CREATE POLICY", name, table))
		}
	}

	return errs
}
