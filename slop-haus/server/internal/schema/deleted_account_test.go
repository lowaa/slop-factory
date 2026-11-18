package schema_test

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/schema/seeddata"
)

// Columns that reference global_users and are not "who acted": the user's own
// rows, which go with them, and the consent record, which refuses the
// deletion outright. Everything else must null itself and say why.
var nonActorUserReferences = map[string]string{
	"global_auth_tokens.user_id":        "c",
	"global_former_memberships.user_id": "c",
	"global_invites.invited_by":         "c",
	"global_tenant_memberships.user_id": "c",
	"global_user_identities.user_id":    "c",
	"global_user_settings.user_id":      "c",
	"user_tenant_settings.user_id":      "c",
	"global_consent_events.user_id":     "r",
}

// "Who acted" columns with no foreign key to global_users, which the
// deletion clears itself. actor_id holds an agent's id for an agent actor, so
// it cannot have one; its row only names a user when actor_kind says so.
var unkeyedActorColumns = []actorColumn{
	{table: "ticket_status_changes", column: "actor_id", flag: "actor_id_deleted", only: "actor_kind = 'user'"},
	{table: "attempt_status_changes", column: "actor_id", flag: "actor_id_deleted", only: "actor_kind = 'user'"},
	{table: "harness_config", column: "updated_by", flag: "updated_by_deleted"},
}

type actorColumn struct {
	table, column, flag string
	// only narrows the rows that name a user, for a column shared with other kinds of id.
	only string
}

func (c actorColumn) String() string { return c.table + "." + c.column }

type userReference struct {
	table, column, deleteRule string
	nullable                  bool
}

func (r userReference) String() string { return r.table + "." + r.column }

func userReferences(t *testing.T, q db.Querier) []userReference {
	t.Helper()
	rows, err := q.Query(context.Background(), `
		SELECT c.conrelid::regclass::text, a.attname, c.confdeltype::text, NOT a.attnotnull
		  FROM pg_constraint c
		  JOIN pg_attribute a ON a.attrelid = c.conrelid AND a.attnum = ANY (c.conkey)
		 WHERE c.contype = 'f' AND c.confrelid = 'global_users'::regclass
		 ORDER BY 1, 2`)
	require.NoError(t, err)
	defer rows.Close()
	var refs []userReference
	for rows.Next() {
		var r userReference
		require.NoError(t, rows.Scan(&r.table, &r.column, &r.deleteRule, &r.nullable))
		refs = append(refs, r)
	}
	require.NoError(t, rows.Err())
	return refs
}

func actorColumns(t *testing.T, q db.Querier) []actorColumn {
	t.Helper()
	var cols []actorColumn
	for _, r := range userReferences(t, q) {
		if _, excluded := nonActorUserReferences[r.String()]; excluded {
			continue
		}
		cols = append(cols, actorColumn{table: r.table, column: r.column, flag: r.column + "_deleted"})
	}
	return append(cols, unkeyedActorColumns...)
}

// A new column referencing global_users cannot skip the deletion handling:
// it must null on delete, be flagged, and have the trigger that sets the flag,
// or be named above as not being "who acted".
func TestEveryUserReferenceNullsOnDeleteAndIsFlagged(t *testing.T) {
	database := dbtest.WithDB(t)
	ctx := context.Background()

	seen := map[string]bool{}
	for _, ref := range userReferences(t, database.Global()) {
		seen[ref.String()] = true
		if want, excluded := nonActorUserReferences[ref.String()]; excluded {
			assert.Equal(t, want, ref.deleteRule, "%s: an excluded column keeps its ON DELETE rule", ref)
			continue
		}
		assert.Equal(t, "n", ref.deleteRule, "%s must be ON DELETE SET NULL (or be listed in nonActorUserReferences)", ref)
		assert.True(t, ref.nullable, "%s must be nullable", ref)

		flag := ref.column + "_deleted"
		var flagged bool
		require.NoError(t, database.Global().QueryRow(ctx, `
			SELECT EXISTS (SELECT 1 FROM information_schema.columns
			                WHERE table_name = $1 AND column_name = $2
			                  AND data_type = 'boolean' AND is_nullable = 'NO' AND column_default = 'false')`,
			ref.table, flag).Scan(&flagged))
		assert.True(t, flagged, "%s needs a companion %s boolean NOT NULL DEFAULT false", ref, flag)

		var checked bool
		require.NoError(t, database.Global().QueryRow(ctx, `
			SELECT EXISTS (SELECT 1 FROM pg_constraint
			                WHERE conrelid = $1::regclass AND contype = 'c'
			                  AND pg_get_constraintdef(oid) ~ ('NOT ' || $2 || E'\\)? OR \\(?' || $3 || ' IS NULL'))`,
			ref.table, flag, ref.column).Scan(&checked))
		assert.True(t, checked, "%s needs CHECK (NOT %s OR %s IS NULL)", ref, flag, ref.column)

		var triggered bool
		require.NoError(t, database.Global().QueryRow(ctx, `
			SELECT EXISTS (SELECT 1 FROM pg_trigger
			                WHERE tgrelid = $1::regclass AND NOT tgisinternal
			                  AND tgfoid = 'mark_deleted_actors'::regproc
			                  AND pg_get_triggerdef(oid) LIKE '%' || quote_literal($2) || '%')`,
			ref.table, ref.column).Scan(&triggered))
		assert.True(t, triggered, "%s needs the mark_deleted_actors trigger naming it", ref)
	}
	for name := range nonActorUserReferences {
		assert.True(t, seen[name], "%s is listed as excluded but no longer references global_users", name)
	}
	for _, col := range unkeyedActorColumns {
		var exists bool
		require.NoError(t, database.Global().QueryRow(ctx, `
			SELECT EXISTS (SELECT 1 FROM information_schema.columns
			                WHERE table_name = $1 AND column_name = $2 AND data_type = 'boolean')`,
			col.table, col.flag).Scan(&exists))
		assert.True(t, exists, "%s needs its %s flag", col, col.flag)
	}
}

// Deleting a user nulls and flags every actor column in every tenant, and
// leaves their id nowhere in the database -- in any column, of any type, so a
// column added later is caught if the seeders fill it.
func TestDeletingAUserLeavesNoTraceOfThemInAnyTenant(t *testing.T) {
	ctx := context.Background()
	app, owner := dbtest.WithOwnerDB(t)
	tenants := []uuid.UUID{uuid.New(), uuid.New(), uuid.New()}
	version, err := seeddata.SchemaVersion(ctx, app.Global())
	require.NoError(t, err)
	require.NoError(t, seeddata.NewSeeding(app, seeddata.Seeders, tenants).Round(ctx, version, 0))

	var victim, bystander uuid.UUID
	require.NoError(t, owner.Global().QueryRow(ctx,
		`SELECT id FROM global_users WHERE email LIKE 'user0%'`).Scan(&victim))
	require.NoError(t, owner.Global().QueryRow(ctx,
		`SELECT id FROM global_users WHERE email LIKE 'user1%'`).Scan(&bystander))

	columns := actorColumns(t, owner.Global())
	require.NotEmpty(t, columns)

	// Each seeded user acted in their own tenant. Make the victim the actor of
	// every row the seeders gave an actor, in every tenant, as the superuser
	// with triggers off so the rules about changing an owner do not stand in
	// the way of building the fixture.
	require.NoError(t, owner.InGlobalTx(ctx, func(q db.Querier) error {
		if _, err := q.Exec(ctx, `SET LOCAL session_replication_role = replica`); err != nil {
			return err
		}
		for _, c := range columns {
			set, where := c.column+" = $1", c.column+" IS NOT NULL"
			if c.only != "" {
				set, where = "actor_kind = 'user', actor_id = $1", "true"
			}
			if c.table == "harness_config" {
				where = "true"
			}
			if _, err := q.Exec(ctx, fmt.Sprintf(`UPDATE %s SET %s WHERE %s`, c.table, set, where), victim); err != nil {
				return fmt.Errorf("%s: %w", c, err)
			}
		}
		// One status change per history table stays an agent's, which no
		// deletion of a user may touch.
		for _, table := range []string{"ticket_status_changes", "attempt_status_changes"} {
			if _, err := q.Exec(ctx, fmt.Sprintf(
				`UPDATE %[1]s SET actor_kind = 'agent', actor_id = gen_random_uuid() WHERE ctid = (SELECT min(ctid) FROM %[1]s)`, table)); err != nil {
				return err
			}
		}
		if _, err := q.Exec(ctx, `UPDATE codehost_setup_states SET user_id = $1`, victim); err != nil {
			return err
		}
		// Consent records refuse deletion by design; whatever builds account
		// deletion has to settle them first, so the fixture does the same.
		_, err := q.Exec(ctx, `DELETE FROM global_consent_events WHERE user_id = $1`, victim)
		return err
	}))

	holding := func(c actorColumn, user uuid.UUID) map[string]int {
		return countByTenant(t, owner, c.table, fmt.Sprintf("%s = '%s'", c.column, user), c.only)
	}
	flagged := func(c actorColumn) map[string]int {
		return countByTenant(t, owner, c.table, c.flag, "")
	}
	before := map[string]map[string]int{}
	bystanders := map[string]map[string]int{}
	for _, c := range columns {
		before[c.String()] = holding(c, victim)
		bystanders[c.String()] = holding(c, bystander)
		for _, tenant := range tenants {
			if isGlobal(c.table) {
				continue
			}
			assert.NotZero(t, before[c.String()][tenant.String()],
				"%s: the seeders leave no row for the victim in tenant %s, so nothing proves it is nulled there", c, tenant)
		}
		assert.Zero(t, sum(flagged(c)), "%s: flags start unset", c)
	}
	require.NotEmpty(t, scanForUser(t, owner, victim), "the scan finds an id that is there")
	var setups int
	require.NoError(t, owner.Global().QueryRow(ctx, `SELECT count(*) FROM codehost_setup_states WHERE user_id = $1`, victim).Scan(&setups))
	require.Equal(t, len(tenants), setups)

	_, err = app.Global().Exec(ctx, `DELETE FROM global_users WHERE id = $1`, victim)
	require.NoError(t, err)

	assert.Empty(t, scanForUser(t, owner, victim), "columns still holding the deleted user's id")
	for _, c := range columns {
		assert.Zero(t, sum(holding(c, victim)), "%s still holds the id", c)
		assert.Equal(t, before[c.String()], flagged(c), "%s: exactly the rows that named the victim, in every tenant, are flagged", c)
		assert.Equal(t, bystanders[c.String()], holding(c, bystander), "%s: someone else's rows are left alone", c)
	}
	require.NoError(t, owner.Global().QueryRow(ctx, `SELECT count(*) FROM codehost_setup_states WHERE user_id = $1`, victim).Scan(&setups))
	assert.Zero(t, setups)

	for _, table := range []string{"ticket_status_changes", "attempt_status_changes"} {
		var agentRows int
		require.NoError(t, owner.Global().QueryRow(ctx, fmt.Sprintf(
			`SELECT count(*) FROM %s WHERE actor_kind = 'agent' AND actor_id IS NOT NULL AND NOT actor_id_deleted`, table)).Scan(&agentRows))
		assert.Equal(t, 1, agentRows, "%s: an agent's history row is left as it was", table)
	}

	// What the deletion leaves must still be held to the rules: a deleted
	// person is only a valid state while the flag says so.
	for _, c := range []struct{ name, sql string }{
		{"a comment's author flag cleared", `UPDATE ticket_comments SET author_user_id_deleted = false WHERE author_user_id_deleted`},
		{"a revision's editor flag cleared", `UPDATE ticket_revisions SET editor_user_id_deleted = false WHERE editor_user_id_deleted`},
		{"a post-review's flag cleared", `UPDATE tickets SET post_reviewed_by_user_id_deleted = false WHERE post_reviewed_by_user_id_deleted`},
		{"a conductor's owner flag cleared", `UPDATE agents SET owner_user_id_deleted = false WHERE owner_user_id_deleted`},
		{"a message's author flag cleared", `UPDATE agent_messages SET author_user_id_deleted = false WHERE author_user_id_deleted`},
		{"a conversation's owner flag cleared", `UPDATE conductor_conversations SET owner_user_id_deleted = false WHERE owner_user_id_deleted`},
		{"a config save's author flag cleared", `UPDATE harness_config SET updated_by_deleted = false WHERE updated_by_deleted`},
		{"a flag set on a row that still names someone", `UPDATE tickets SET created_by_user_id = $1, created_by_user_id_deleted = true WHERE NOT created_by_user_id_deleted`},
		{"a version's body edited", `UPDATE fleet_instruction_versions SET body = 'edited'`},
		{"a variant version's author swapped", `UPDATE variant_versions SET created_by = $1`},
		{"a conductor given a new owner", `UPDATE agents SET owner_user_id = $1 WHERE owner_user_id_deleted`},
	} {
		args := []any{}
		if strings.Contains(c.sql, "$1") {
			args = append(args, bystander)
		}
		_, err := owner.Global().Exec(ctx, c.sql, args...)
		assert.Error(t, err, c.name)
	}

	var disabled int
	require.NoError(t, owner.Global().QueryRow(ctx,
		`SELECT count(*) FROM agents WHERE kind = 'conductor' AND owner_user_id_deleted AND disabled_at IS NOT NULL`).Scan(&disabled))
	assert.NotZero(t, disabled, "a conductor whose owner was a member is still switched off by the membership going")
}

func isGlobal(table string) bool { return strings.HasPrefix(table, "global_") }

func sum(m map[string]int) (n int) {
	for _, v := range m {
		n += v
	}
	return n
}

// countByTenant counts the rows matching where, by tenant (everything under
// "" for a global table). only narrows the rows to those a shared column names
// a user in.
func countByTenant(t *testing.T, database *db.DB, table, where, only string) map[string]int {
	t.Helper()
	scope := "''"
	if !isGlobal(table) {
		scope = "tenant_id::text"
	}
	if only != "" {
		where = "(" + where + ") AND " + only
	}
	rows, err := database.Global().Query(context.Background(),
		fmt.Sprintf(`SELECT %s, count(*) FROM %s WHERE %s GROUP BY 1`, scope, table, where))
	require.NoError(t, err)
	defer rows.Close()
	counts := map[string]int{}
	for rows.Next() {
		var scope string
		var n int
		require.NoError(t, rows.Scan(&scope, &n))
		counts[scope] = n
	}
	require.NoError(t, rows.Err())
	return counts
}

// scanForUser is every table.column, of any type, whose text holds the id.
func scanForUser(t *testing.T, database *db.DB, user uuid.UUID) []string {
	t.Helper()
	ctx := context.Background()
	tables, err := seeddata.Tables(ctx, database.Global())
	require.NoError(t, err)
	var found []string
	for _, table := range tables {
		rows, err := database.Global().Query(ctx, fmt.Sprintf(
			`SELECT DISTINCT e.key FROM %s r, jsonb_each_text(to_jsonb(r)) AS e WHERE e.value LIKE '%%' || $1 || '%%'`, table), user.String())
		require.NoError(t, err, table)
		for rows.Next() {
			var column string
			require.NoError(t, rows.Scan(&column))
			found = append(found, table+"."+column)
		}
		require.NoError(t, rows.Err())
		rows.Close()
	}
	sort.Strings(found)
	return found
}
