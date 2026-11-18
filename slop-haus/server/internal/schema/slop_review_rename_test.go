package schema_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/schema"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/schema/seeddata"
)

const slopReviewRename = 175

// The rename moves every stored "self-review" usage row and every recorded
// self_review_complete check, in every tenant, as the migrating role that RLS
// binds -- and nothing else. The seeded walk cannot show it: no seeder writes
// the old names, which only reporters and the steward did.
func TestSlopReviewRenameMovesTheStoredNames(t *testing.T) {
	ctx := context.Background()
	rig := dbtest.NewMigrationRig(t, schema.Migrations)
	require.NoError(t, rig.MigrateTo(ctx, slopReviewRename-1))
	renamed, untouched := uuid.New(), uuid.New()
	sd := seeddata.NewSeeding(rig.App, seeddata.Seeders, []uuid.UUID{renamed, untouched})
	require.NoError(t, sd.Round(ctx, slopReviewRename-1, 0))

	admin := rig.Admin.Global()
	// llm_tool_usage references llm_usage by a key that includes harness, so
	// both move at once with the reference switched off.
	_, err := admin.Exec(ctx, `
		SET session_replication_role = replica;
		UPDATE llm_usage SET harness = 'self-review' WHERE tenant_id = '`+renamed.String()+`';
		UPDATE llm_tool_usage SET harness = 'self-review' WHERE tenant_id = '`+renamed.String()+`';
		UPDATE llm_usage_marks SET harness = 'self-review' WHERE tenant_id = '`+renamed.String()+`';
		UPDATE llm_guidance_usage SET harness = 'self-review' WHERE tenant_id = '`+renamed.String()+`';
		SET session_replication_role = origin;`)
	require.NoError(t, err)
	const oldEvidence = `[{"check": "ci_green_on_head", "ok": true}, {"check": "self_review_complete", "ok": false, "detail": "standard: not approved"}, {"check": "no_protected_path", "ok": true}]`
	const newEvidence = `[{"check": "ci_green_on_head", "ok": true}, {"check": "slop_review_complete", "ok": false, "detail": "standard: not approved"}, {"check": "no_protected_path", "ok": true}]`
	for _, table := range []string{"steward_assessments", "steward_drafts"} {
		_, err := admin.Exec(ctx, `UPDATE `+table+` SET evidence = $1::jsonb WHERE tenant_id = $2`, oldEvidence, renamed)
		require.NoError(t, err)
	}
	harnesses := func(table string, tenant uuid.UUID) []string {
		var got []string
		require.NoError(t, admin.QueryRow(ctx, `SELECT array_agg(DISTINCT harness) FROM `+table+` WHERE tenant_id = $1`, tenant).Scan(&got))
		return got
	}
	usageTables := []string{"llm_usage", "llm_tool_usage", "llm_usage_marks", "llm_guidance_usage"}
	before := map[string][]string{}
	for _, table := range usageTables {
		require.Equal(t, []string{"self-review"}, harnesses(table, renamed), "the test's own setup: %s", table)
		before[table] = harnesses(table, untouched)
	}

	require.NoError(t, rig.MigrateTo(ctx, slopReviewRename))

	for _, table := range usageTables {
		assert.Equal(t, []string{"slop-review"}, harnesses(table, renamed), table)
		assert.Equal(t, before[table], harnesses(table, untouched), "%s: other harnesses are left alone", table)
	}
	for _, table := range []string{"steward_assessments", "steward_drafts"} {
		var got string
		require.NoError(t, admin.QueryRow(ctx, `SELECT evidence::text FROM `+table+` WHERE tenant_id = $1`, renamed).Scan(&got))
		assert.JSONEq(t, newEvidence, got, "%s: the check is renamed in place, the rest kept in order", table)
		require.NoError(t, admin.QueryRow(ctx, `SELECT evidence::text FROM `+table+` WHERE tenant_id = $1`, untouched).Scan(&got))
		assert.JSONEq(t, `[{"check": "ci_green_on_head", "ok": true}]`, got, table)
	}

	var forced, referenced bool
	require.NoError(t, admin.QueryRow(ctx, `
		SELECT (SELECT bool_and(relforcerowsecurity) FROM pg_class
		         WHERE relname IN ('llm_usage', 'llm_tool_usage', 'llm_usage_marks', 'llm_guidance_usage', 'steward_assessments', 'steward_drafts')),
		       EXISTS (SELECT 1 FROM pg_constraint
		                WHERE conname = 'llm_tool_usage_tenant_id_agent_id_transcript_key_provider__fkey' AND contype = 'f'
		                  AND confrelid = 'llm_usage'::regclass AND confdeltype = 'c')`).Scan(&forced, &referenced))
	assert.True(t, forced, "row level security is forced again")
	assert.True(t, referenced, "the tool rows reference their usage row again, still cascading on delete")
}
