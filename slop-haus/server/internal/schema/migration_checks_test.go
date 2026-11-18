package schema_test

import (
	"context"
	"io/fs"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/schema"
)

// go_db_with_rls_lifted is defined in the baseline, whose own body lifts
// FORCE by hand; the lint holds every migration after it.
const firstRLSLintedMigration = baselineVersion + 1

const rlsTenantScopedMarker = "rls-lint: tenant-scoped"

var (
	sqlLineCommentRe = regexp.MustCompile(`--[^\n]*`)
	noForceRLSRe     = regexp.MustCompile(`(?i)\bNO\s+FORCE\s+ROW\s+LEVEL\s+SECURITY\b`)
	dollarTagRe      = regexp.MustCompile(`\$\w*\$`)
	doBlockRe        = regexp.MustCompile(`(?i)\bDO\s+(LANGUAGE\s+\w+\s+)?$`)
	rowWriteRe       = regexp.MustCompile(`(?is)^\s*((WITH|DO)\b.*)?\b(UPDATE|DELETE\s+FROM|INSERT\s+INTO\b.*\bSELECT)\b`)
)

// lintRLSBackfills reports what in a migration's Up section would silently do
// nothing, or hand-roll the lift, under FORCE ROW LEVEL SECURITY: the owner
// that runs migrations is bound by it and sees no tenant's rows.
//   - A bare NO FORCE ROW LEVEL SECURITY: use go_db_with_rls_lifted, which
//     restores what it lifted.
//   - An INSERT ... SELECT, UPDATE or DELETE that names a forced table outside
//     the helper. A statement that really is scoped to one tenant says so
//     with a `-- rls-lint: tenant-scoped` comment inside it.
//
// Function bodies are skipped (they run later, as the caller), as is the
// helper's own body. A DO block is not: it runs now.
func lintRLSBackfills(file, sql string, forced []string) []string {
	sql = sqlLineCommentRe.ReplaceAllStringFunc(sql, func(c string) string {
		if strings.Contains(c, rlsTenantScopedMarker) {
			return "/*TENANT_SCOPED*/"
		}
		return ""
	})

	var problems []string
	if noForceRLSRe.MatchString(sql) {
		problems = append(problems, file+": NO FORCE ROW LEVEL SECURITY by hand; wrap the backfill in go_db_with_rls_lifted(tables, body)")
	}

	for _, stmt := range strings.Split(stripDollarQuoted(sql), ";") {
		if strings.Contains(stmt, "TENANT_SCOPED") || !rowWriteRe.MatchString(stmt) {
			continue
		}
		for _, table := range forced {
			if regexp.MustCompile(`(?i)\b` + regexp.QuoteMeta(table) + `\b`).MatchString(stmt) {
				problems = append(problems, file+": a statement over FORCE ROW LEVEL SECURITY table "+table+
					" runs as the owner and sees no tenant's rows; put it in go_db_with_rls_lifted, or mark it `-- "+rlsTenantScopedMarker+"`: "+
					strings.Join(strings.Fields(stmt), " "))
				break
			}
		}
	}
	return problems
}

// stripDollarQuoted drops every dollar-quoted body except a DO block's.
func stripDollarQuoted(sql string) string {
	var out strings.Builder
	for {
		open := dollarTagRe.FindStringIndex(sql)
		if open == nil {
			out.WriteString(sql)
			return out.String()
		}
		tag := sql[open[0]:open[1]]
		end := strings.Index(sql[open[1]:], tag)
		if end < 0 {
			out.WriteString(sql)
			return out.String()
		}
		end += open[1] + len(tag)
		out.WriteString(sql[:open[0]])
		if doBlockRe.MatchString(sql[:open[0]]) {
			out.WriteString(sql[open[0]:end])
		}
		sql = sql[end:]
	}
}

func migrationVersion(t *testing.T, name string) int {
	t.Helper()
	v, err := strconv.Atoi(strings.SplitN(name, "_", 2)[0])
	require.NoError(t, err, name)
	return v
}

func forcedTables(t *testing.T) []string {
	t.Helper()
	database := dbtest.WithDB(t)
	rows, err := database.Global().Query(context.Background(),
		`SELECT relname FROM pg_class WHERE relkind = 'r' AND relnamespace = 'public'::regnamespace AND relforcerowsecurity`)
	require.NoError(t, err)
	defer rows.Close()
	var tables []string
	for rows.Next() {
		var name string
		require.NoError(t, rows.Scan(&name))
		tables = append(tables, name)
	}
	require.NoError(t, rows.Err())
	require.NotEmpty(t, tables)
	sort.Strings(tables)
	return tables
}

// A backfill once forgot to lift FORCE ROW LEVEL SECURITY and backfilled nothing, which
// took a follow-up migration to find. A new migration cannot make that mistake, or
// lift by hand and forget to restore.
func TestNewMigrationsBackfillThroughTheRLSHelper(t *testing.T) {
	forced := forcedTables(t)
	names, err := fs.Glob(schema.Migrations, "*.sql")
	require.NoError(t, err)

	checked := 0
	for _, name := range names {
		if migrationVersion(t, name) < firstRLSLintedMigration {
			continue
		}
		checked++
		content, err := fs.ReadFile(schema.Migrations, name)
		require.NoError(t, err)
		up, _, _ := strings.Cut(string(content), "-- +goose Down")
		assert.Empty(t, lintRLSBackfills(name, up, forced))
	}
	t.Logf("%d migrations after the helper linted", checked)
}

func TestTheRLSBackfillLint(t *testing.T) {
	forced := []string{"agents", "llm_usage"}
	for name, c := range map[string]struct {
		sql     string
		problem string
	}{
		"a hand-rolled lift": {`
			ALTER TABLE agents NO FORCE ROW LEVEL SECURITY;
			UPDATE agents SET name = name;
			ALTER TABLE agents FORCE ROW LEVEL SECURITY;`, "by hand"},
		"an unlifted update": {`UPDATE llm_usage SET unreliable = true;`, "llm_usage"},
		"an unlifted insert select": {`
			INSERT INTO scratch (id) SELECT id FROM agents;`, "agents"},
		"an unlifted delete":   {`DELETE FROM agents WHERE name = 'x';`, "agents"},
		"a lift in a do block": {`DO $$ BEGIN UPDATE agents SET name = name; END $$;`, "agents"},
	} {
		t.Run(name, func(t *testing.T) {
			problems := lintRLSBackfills("fixture.sql", c.sql, forced)
			require.NotEmpty(t, problems)
			assert.Contains(t, strings.Join(problems, "\n"), c.problem)
		})
	}

	for name, sql := range map[string]string{
		"the helper": `
			SELECT go_db_with_rls_lifted(ARRAY['agents'], $body$
			    UPDATE agents SET name = name;
			$body$);`,
		"a tenant-scoped statement": `
			UPDATE agents SET name = 'x'
			 -- rls-lint: tenant-scoped
			 WHERE tenant_id = current_tenant();`,
		"a function body": `
			CREATE FUNCTION f() RETURNS void LANGUAGE sql AS $$ UPDATE agents SET name = name $$;`,
		"a table that is not forced":       `UPDATE scratch SET x = 1;`,
		"a comment about the lift":         `-- ALTER TABLE agents NO FORCE ROW LEVEL SECURITY would be wrong here`,
		"a create table with an on update": `CREATE TABLE t (a int REFERENCES agents ON UPDATE CASCADE);`,
	} {
		t.Run("allows "+name, func(t *testing.T) {
			assert.Empty(t, lintRLSBackfills("fixture.sql", sql, forced))
		})
	}
}

// The helper backfills every tenant's rows, restores FORCE on exactly the
// tables it lifted, and does so as the role production migrates as.
func TestRLSHelperBackfillsAcrossTenantsAndRestoresForce(t *testing.T) {
	ctx := context.Background()
	rig := dbtest.NewMigrationRig(t, schema.Migrations)
	require.NoError(t, rig.MigrateTo(ctx, baselineVersion))

	_, err := rig.Owner.Global().Exec(ctx, `
		CREATE TABLE lift_notes (id bigserial PRIMARY KEY, tenant_id uuid NOT NULL DEFAULT current_tenant(), body text, shout text);
		SELECT go_db_apply_tenant_rls('lift_notes');
		CREATE TABLE lift_other (id bigserial PRIMARY KEY, tenant_id uuid NOT NULL DEFAULT current_tenant());
		SELECT go_db_apply_tenant_rls('lift_other');
		ALTER TABLE lift_other NO FORCE ROW LEVEL SECURITY;`)
	require.NoError(t, err)

	tenants := []uuid.UUID{uuid.New(), uuid.New()}
	for _, tenant := range tenants {
		_, err := rig.App.Exec(db.WithTenant(ctx, tenant), `INSERT INTO lift_notes (body) VALUES ('hello')`)
		require.NoError(t, err)
	}
	forcedState := func() (notes, other bool) {
		require.NoError(t, rig.Admin.Global().QueryRow(ctx, `
			SELECT (SELECT relforcerowsecurity FROM pg_class WHERE relname = 'lift_notes'),
			       (SELECT relforcerowsecurity FROM pg_class WHERE relname = 'lift_other')`).Scan(&notes, &other))
		return
	}
	filled := func() (n int) {
		require.NoError(t, rig.Admin.Global().QueryRow(ctx, `SELECT count(shout) FROM lift_notes`).Scan(&n))
		return n
	}

	// Without the helper the owner sees nothing: the bug it exists for.
	tag, err := rig.Owner.Global().Exec(ctx, `UPDATE lift_notes SET shout = upper(body)`)
	require.NoError(t, err)
	require.Zero(t, tag.RowsAffected())

	// A body that fails leaves FORCE as it was.
	_, err = rig.Owner.Global().Exec(ctx, `SELECT go_db_with_rls_lifted(ARRAY['lift_notes'], $b$
		UPDATE lift_notes SET shout = upper(body); SELECT 1 / 0 $b$)`)
	require.Error(t, err)
	notes, other := forcedState()
	assert.True(t, notes, "a failed body restores FORCE")
	assert.False(t, other)
	assert.Zero(t, filled(), "and rolls the body back")

	_, err = rig.Owner.Global().Exec(ctx, `SELECT go_db_with_rls_lifted(ARRAY['lift_notes', 'lift_other'], $b$
		UPDATE lift_notes SET shout = upper(body);
		INSERT INTO lift_other (tenant_id) SELECT tenant_id FROM lift_notes $b$)`)
	require.NoError(t, err)
	assert.Equal(t, 2, filled(), "both tenants' rows were backfilled")
	var copied int
	require.NoError(t, rig.Admin.Global().QueryRow(ctx, `SELECT count(*) FROM lift_other`).Scan(&copied))
	assert.Equal(t, 2, copied, "and the body's later statements ran too")
	notes, other = forcedState()
	assert.True(t, notes, "FORCE is back")
	assert.False(t, other, "a table that was not forced is not forced afterwards")

	tag, err = rig.Owner.Global().Exec(ctx, `UPDATE lift_notes SET shout = 'again'`)
	require.NoError(t, err)
	assert.Zero(t, tag.RowsAffected(), "the owner is bound again")

	_, err = rig.Owner.Global().Exec(ctx, `SELECT go_db_with_rls_lifted(ARRAY['no_such_table'], 'SELECT 1')`)
	assert.ErrorContains(t, err, "no table")
}
