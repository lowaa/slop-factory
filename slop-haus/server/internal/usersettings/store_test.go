package usersettings_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/usersettings"
)

type fixture struct {
	t     *testing.T
	ctx   context.Context
	db    *db.DB
	store *usersettings.PostgresStore
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	database := dbtest.WithDB(t)
	return &fixture{t: t, ctx: context.Background(), db: database, store: usersettings.NewPostgresStore(database)}
}

func (f *fixture) newUser() uuid.UUID {
	f.t.Helper()
	id := uuid.New()
	_, err := f.db.Global().Exec(f.ctx,
		`INSERT INTO global_users (id, email, password_hash) VALUES ($1, $2, 'unused')`, id, id.String()+"@example.invalid")
	require.NoError(f.t, err)
	return id
}

func (f *fixture) latest(user uuid.UUID, names ...string) map[string]usersettings.Stored {
	f.t.Helper()
	rows, err := f.store.Latest(f.ctx, user, names)
	require.NoError(f.t, err)
	out := map[string]usersettings.Stored{}
	for _, r := range rows {
		out[r.Name] = r
	}
	return out
}

func TestStore_LatestWins(t *testing.T) {
	f := newFixture(t)
	u := f.newUser()
	for _, b := range []bool{true, false, true, false} {
		_, err := f.store.Append(f.ctx, u, "a", usersettings.BoolValue(b))
		require.NoError(t, err)
	}
	got := f.latest(u, "a")
	require.Contains(t, got, "a")
	assert.Equal(t, usersettings.BoolValue(false), got["a"].Value)
}

// now() is the transaction's start, so two writes in one transaction share
// created_at and only id says which came last.
func TestStore_IDBreaksATieBetweenWritesInOneTransaction(t *testing.T) {
	f := newFixture(t)
	u := f.newUser()
	require.NoError(t, f.db.InGlobalTx(f.ctx, func(q db.Querier) error {
		for _, b := range []bool{false, true} {
			if _, err := q.Exec(f.ctx,
				`INSERT INTO global_user_settings (user_id, setting_name, setting_type, bool_value) VALUES ($1, 'a', 'bool', $2)`, u, b); err != nil {
				return err
			}
		}
		return nil
	}))
	var distinct int
	require.NoError(t, f.db.Global().QueryRow(f.ctx,
		`SELECT count(DISTINCT created_at) FROM global_user_settings WHERE user_id = $1`, u).Scan(&distinct))
	require.Equal(t, 1, distinct, "the two rows must tie on created_at for this test to mean anything")

	assert.Equal(t, usersettings.BoolValue(true), f.latest(u, "a")["a"].Value)
}

func TestStore_SettingsAreIndependent(t *testing.T) {
	f := newFixture(t)
	u := f.newUser()
	_, err := f.store.Append(f.ctx, u, "a", usersettings.BoolValue(false))
	require.NoError(t, err)
	_, err = f.store.Append(f.ctx, u, "b", usersettings.StrValue("x"))
	require.NoError(t, err)
	_, err = f.store.Append(f.ctx, u, "a", usersettings.BoolValue(true))
	require.NoError(t, err)

	got := f.latest(u, "a", "b", "never_written")
	assert.Len(t, got, 2, "a name with no row is absent, for the service to default")
	assert.Equal(t, usersettings.BoolValue(true), got["a"].Value)
	assert.Equal(t, usersettings.StrValue("x"), got["b"].Value)
}

func TestStore_RoundTripsEveryType(t *testing.T) {
	f := newFixture(t)
	u := f.newUser()
	values := map[string]usersettings.Value{
		"s": usersettings.StrValue(""), "i": usersettings.IntValue(0), "b": usersettings.BoolValue(false), "f": usersettings.FloatValue(0),
	}
	for name, v := range values {
		_, err := f.store.Append(f.ctx, u, name, v)
		require.NoError(t, err)
	}
	got := f.latest(u, "s", "i", "b", "f")
	for name, v := range values {
		assert.Equal(t, v, got[name].Value, name)
	}
}

func TestStore_OneUsersSettingsAreNeverReturnedForAnother(t *testing.T) {
	f := newFixture(t)
	alice, bob := f.newUser(), f.newUser()
	_, err := f.store.Append(f.ctx, alice, "a", usersettings.BoolValue(false))
	require.NoError(t, err)

	assert.Empty(t, f.latest(bob, "a"))
	assert.Contains(t, f.latest(alice, "a"), "a")
}

func TestStore_DeletingTheUserRemovesTheirSettings(t *testing.T) {
	f := newFixture(t)
	u := f.newUser()
	_, err := f.store.Append(f.ctx, u, "a", usersettings.BoolValue(false))
	require.NoError(t, err)
	_, err = f.db.Global().Exec(f.ctx, `DELETE FROM global_users WHERE id = $1`, u)
	require.NoError(t, err)

	var n int
	require.NoError(t, f.db.Global().QueryRow(f.ctx, `SELECT count(*) FROM global_user_settings`).Scan(&n))
	assert.Zero(t, n)
}

func TestTable_ChecksRefuseBadRows(t *testing.T) {
	f := newFixture(t)
	u := f.newUser()
	cases := map[string]string{
		"no value at all":            `'a', 'bool', NULL, NULL, NULL, NULL, NULL`,
		"two values":                 `'a', 'bool', NULL, NULL, true, 1.5, NULL`,
		"a value of another type":    `'a', 'bool', 'text', NULL, NULL, NULL, NULL`,
		"a str type with a bool":     `'a', 'str', NULL, NULL, true, NULL, NULL`,
		"an int type with a float":   `'a', 'int', NULL, NULL, NULL, 1.5, NULL`,
		"a float type with an int":   `'a', 'float', NULL, 1, NULL, NULL, NULL`,
		"a jsonb type with a string": `'a', 'jsonb', 'x', NULL, NULL, NULL, NULL`,
		"a jsonb type with no value": `'a', 'jsonb', NULL, NULL, NULL, NULL, NULL`,
		"an unknown type":            `'a', 'blob', 'x', NULL, NULL, NULL, NULL`,
	}
	for name, values := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := f.db.Global().Exec(f.ctx, `INSERT INTO global_user_settings
				(user_id, setting_name, setting_type, str_value, int_value, bool_value, float_value, jsonb_value)
				VALUES ($1, `+values+`)`, u)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "check constraint")
		})
	}

	t.Run("a well-formed jsonb row is accepted", func(t *testing.T) {
		_, err := f.db.Global().Exec(f.ctx, `INSERT INTO global_user_settings
			(user_id, setting_name, setting_type, jsonb_value) VALUES ($1, 'a', 'jsonb', '{"k": 1}')`, u)
		require.NoError(t, err)
	})
}

// planNode is the part of EXPLAIN's JSON the index-seek check reads.
type planNode struct {
	NodeType    string     `json:"Node Type"`
	IndexName   string     `json:"Index Name"`
	ActualRows  float64    `json:"Actual Rows"`
	ActualLoops float64    `json:"Actual Loops"`
	Plans       []planNode `json:"Plans"`
}

func (n planNode) walk(visit func(planNode)) {
	visit(n)
	for _, c := range n.Plans {
		c.walk(visit)
	}
}

// Reading the latest values must cost the seeks for the known names, not a
// pass over a user's whole history, which DISTINCT ON over their rows would
// be: that gets the same plan shape (an index scan, no sort), so the check is
// on the rows the scan actually read.
func TestStore_LatestSeeksTheIndexPerName(t *testing.T) {
	f := newFixture(t)
	u := f.newUser()
	_, err := f.db.Global().Exec(f.ctx, `
		INSERT INTO global_user_settings (user_id, setting_name, setting_type, bool_value, created_at)
		SELECT $1, 'name_' || (g % 3), 'bool', g % 2 = 0, now() - make_interval(secs => g)
		  FROM generate_series(1, 5000) AS g`, u)
	require.NoError(t, err)
	_, err = f.db.Global().Exec(f.ctx, `ANALYZE global_user_settings`)
	require.NoError(t, err)

	names := []string{"name_0", "name_1", "name_2"}
	var raw string
	require.NoError(t, f.db.Global().QueryRow(f.ctx,
		`EXPLAIN (ANALYZE, COSTS OFF, TIMING OFF, SUMMARY OFF, FORMAT JSON) `+usersettings.LatestSQL(f.store), u, names).Scan(&raw))
	var plans []struct {
		Plan planNode `json:"Plan"`
	}
	require.NoError(t, json.Unmarshal([]byte(raw), &plans), raw)
	require.Len(t, plans, 1)

	var indexScans int
	var rowsRead float64
	plans[0].Plan.walk(func(n planNode) {
		assert.NotContains(t, n.NodeType, "Seq Scan", raw)
		assert.NotContains(t, n.NodeType, "Sort", raw)
		if strings.HasPrefix(n.NodeType, "Index") {
			indexScans++
			assert.Equal(t, "global_user_settings_latest", n.IndexName, raw)
			assert.EqualValues(t, len(names), n.ActualLoops, "one seek per name: %s", raw)
			rowsRead += n.ActualRows * n.ActualLoops
		}
	})
	assert.Equal(t, 1, indexScans, raw)
	assert.LessOrEqual(t, rowsRead, float64(len(names)), "the scan must read one row per name, not the history: %s", raw)
}
