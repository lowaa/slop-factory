package migrations

import (
	"context"
	"fmt"
	"strings"

	"github.com/lowaa/slop-factory/go-db/engine"
)

// tenantScopingQuery lists every ordinary table in the public schema
// (excluding goose's own goose_db_version bookkeeping table, which is
// neither global_-prefixed nor tenant data) alongside its actual, live RLS
// state.
const tenantScopingQuery = `
SELECT
	c.relname,
	c.relrowsecurity,
	c.relforcerowsecurity,
	EXISTS (
		SELECT 1 FROM pg_policies p
		WHERE p.schemaname = n.nspname AND p.tablename = c.relname
	)
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = 'public' AND c.relkind = 'r' AND c.relname <> 'goose_db_version'
`

// CheckLiveTenantInvariants queries the live schema behind q (after
// migrations have been applied) and asserts that every table's actual RLS
// state matches what the global_ naming convention says it should be:
// global_-prefixed tables carry no RLS at all, and every other table has
// ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY, and at least one
// policy.
//
// This complements LintGlobalTenantInvariants rather than replacing it: the
// static lint proves a single migration file is internally consistent by
// itself; this proves the database's actual end state -- after every
// migration in the chain -- still matches the convention, which is the only
// way to catch a later migration that ALTERs a table's RLS or DROPs its
// policy (the static, per-file lint has no visibility across files). It
// also needs no hand-maintained table list: every table in the schema is
// discovered and checked automatically, so a new table is covered the
// moment it exists rather than the moment someone remembers to list it.
func CheckLiveTenantInvariants(ctx context.Context, q engine.Querier) []error {
	rows, err := q.QueryContext(ctx, tenantScopingQuery)
	if err != nil {
		return []error{fmt.Errorf("migrations: check live tenant invariants: query: %w", err)}
	}
	defer rows.Close()

	var errs []error
	for rows.Next() {
		var (
			table     string
			enabled   bool
			forced    bool
			hasPolicy bool
		)
		if err := rows.Scan(&table, &enabled, &forced, &hasPolicy); err != nil {
			return []error{fmt.Errorf("migrations: check live tenant invariants: scan: %w", err)}
		}

		if strings.HasPrefix(table, "global_") {
			if enabled || forced || hasPolicy {
				errs = append(errs, fmt.Errorf("global table %q has row-level security applied", table))
			}
			continue
		}

		if !enabled {
			errs = append(errs, fmt.Errorf("tenant-scoped table %q is missing ENABLE ROW LEVEL SECURITY", table))
		}
		if !forced {
			errs = append(errs, fmt.Errorf("tenant-scoped table %q is missing FORCE ROW LEVEL SECURITY", table))
		}
		if !hasPolicy {
			errs = append(errs, fmt.Errorf("tenant-scoped table %q has no RLS policy", table))
		}
	}
	if err := rows.Err(); err != nil {
		errs = append(errs, fmt.Errorf("migrations: check live tenant invariants: rows: %w", err))
	}
	return errs
}
