// Package settingstest is the conformance suite every app's settings tables
// must pass. The app supplies its own tables and a way to run in its own
// request scoping (Fixture), and the suite checks what pgstore promises on
// top of them: the newest row wins, history pages, the scopes never read one
// another's rows, and the tenant, user and membership rules hold in the
// database rather than only in the app's code in front of it.
package settingstest

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	settings "github.com/lowaa/slop-factory/go-settings"
	"github.com/lowaa/slop-factory/go-settings/pgstore"
)

// Actor is a user acting in a tenant.
type Actor struct {
	Tenant uuid.UUID
	User   uuid.UUID
}

// Env is a freshly migrated database with people in it.
type Env struct {
	// Members holds two members of one tenant and a member of another:
	// Members[0].Tenant == Members[1].Tenant != Members[2].Tenant. Each has
	// been a member since before the suite starts.
	Members [3]Actor
	// Stranger is a user who is no member of Members[0].Tenant, acting in it.
	Stranger Actor
	// As runs fn in one transaction scoped as the app scopes a request made by
	// the actor -- the actor's tenant on the context fn receives and on the
	// transaction, and the actor as the transaction's current user -- and
	// commits if fn returns nil. Transactions are separate, so a later one
	// sees what an earlier one committed.
	As func(ctx context.Context, a Actor, fn func(ctx context.Context, q pgstore.Querier) error) error
	// Rejoin ends the actor's membership and starts a new one now, as leaving
	// and being invited back does. Required for a table with a membership
	// rule.
	Rejoin func(ctx context.Context, a Actor) error
}

// Fixture wires an app's tables into the suite. Leave a scope's table nil
// when the app has none; the suite skips it.
type Fixture struct {
	Global     *pgstore.Table
	UserTenant *pgstore.Table
	Tenant     *pgstore.Table
	// Open prepares an Env, and cleans it up with t.Cleanup.
	Open func(t *testing.T) *Env
}

type scope struct {
	name  string
	table *pgstore.Table
	// key is the owner a row of this scope is keyed by.
	key func(Actor) uuid.UUID
}

func (f Fixture) scopes() []scope {
	var out []scope
	user := func(a Actor) uuid.UUID { return a.User }
	if f.Global != nil {
		out = append(out, scope{"global", f.Global, user})
	}
	if f.UserTenant != nil {
		out = append(out, scope{"user_tenant", f.UserTenant, user})
	}
	if f.Tenant != nil {
		out = append(out, scope{"tenant", f.Tenant, func(a Actor) uuid.UUID { return a.Tenant }})
	}
	return out
}

// Run runs the suite.
func Run(t *testing.T, f Fixture) {
	require.NotEmpty(t, f.scopes(), "settingstest: the fixture has no tables")
	t.Run("per scope", func(t *testing.T) {
		for _, sc := range f.scopes() {
			t.Run(sc.name, func(t *testing.T) {
				for _, c := range scopeCases {
					t.Run(c.name, func(t *testing.T) {
						env := f.Open(t)
						me := env.Members[0]
						require.NoError(t, env.As(context.Background(), me, func(ctx context.Context, q pgstore.Querier) error {
							c.fn(t, env, sc, ctx, q, sc.key(me))
							return nil
						}))
					})
				}
			})
		}
	})
	t.Run("isolation", func(t *testing.T) { isolation(t, f) })
	if f.UserTenant != nil && f.UserTenant.HasMembershipRule() {
		t.Run("membership", func(t *testing.T) { membership(t, f) })
	}
	if f.Tenant != nil && f.Tenant.RecordsActor() {
		t.Run("actor", func(t *testing.T) { actor(t, f) })
	}
}

type scopeCase struct {
	name string
	fn   func(t *testing.T, env *Env, sc scope, ctx context.Context, q pgstore.Querier, key uuid.UUID)
}

func latestOf(t *testing.T, sc scope, ctx context.Context, q pgstore.Querier, key uuid.UUID, names ...string) map[string]settings.Stored {
	t.Helper()
	rows, err := sc.table.Latest(ctx, q, key, names)
	require.NoError(t, err)
	out := map[string]settings.Stored{}
	for _, r := range rows {
		out[r.Name] = r
	}
	return out
}

var scopeCases = []scopeCase{
	{"round trips every type", func(t *testing.T, _ *Env, sc scope, ctx context.Context, q pgstore.Querier, key uuid.UUID) {
		values := map[string]settings.Value{
			"s": settings.StrValue(""), "i": settings.IntValue(0), "b": settings.BoolValue(false), "f": settings.FloatValue(0),
			"s2": settings.StrValue("x"), "i2": settings.IntValue(-7), "b2": settings.BoolValue(true), "f2": settings.FloatValue(1.5),
		}
		names := make([]string, 0, len(values))
		for name, v := range values {
			_, err := sc.table.Append(ctx, q, key, name, v)
			require.NoError(t, err)
			names = append(names, name)
		}
		got := latestOf(t, sc, ctx, q, key, names...)
		for name, v := range values {
			assert.Equal(t, v, got[name].Value, name)
		}
	}},
	{"newest wins and names are independent", func(t *testing.T, _ *Env, sc scope, ctx context.Context, q pgstore.Querier, key uuid.UUID) {
		for _, b := range []bool{true, false, true, false} {
			_, err := sc.table.Append(ctx, q, key, "a", settings.BoolValue(b))
			require.NoError(t, err)
		}
		_, err := sc.table.Append(ctx, q, key, "b", settings.StrValue("other"))
		require.NoError(t, err)

		got := latestOf(t, sc, ctx, q, key, "a", "b", "missing")
		assert.Equal(t, settings.BoolValue(false), got["a"].Value)
		assert.Equal(t, settings.StrValue("other"), got["b"].Value)
		assert.NotContains(t, got, "missing")
	}},
	// now() is the transaction's start, so rows written in one transaction
	// share created_at and only id says which came last.
	{"id breaks a tie between writes in one transaction", func(t *testing.T, _ *Env, sc scope, ctx context.Context, q pgstore.Querier, key uuid.UUID) {
		for _, b := range []bool{false, true} {
			_, err := sc.table.Append(ctx, q, key, "a", settings.BoolValue(b))
			require.NoError(t, err)
		}
		var distinct int
		require.NoError(t, q.QueryRow(ctx, `SELECT count(DISTINCT created_at) FROM `+sc.table.Name()).Scan(&distinct))
		require.Equal(t, 1, distinct, "the rows must tie on created_at for this test to mean anything")
		assert.Equal(t, settings.BoolValue(true), latestOf(t, sc, ctx, q, key, "a")["a"].Value)

		// An index that happens to hold ties in id order would hide a query
		// that forgot to order by id, so read again without it.
		for _, guc := range []string{"enable_indexscan", "enable_bitmapscan", "enable_indexonlyscan"} {
			var off string
			require.NoError(t, q.QueryRow(ctx, `SELECT set_config($1, 'off', true)`, guc).Scan(&off))
		}
		assert.Equal(t, settings.BoolValue(true), latestOf(t, sc, ctx, q, key, "a")["a"].Value, "without an index")
	}},
	{"history is newest first and pages", func(t *testing.T, _ *Env, sc scope, ctx context.Context, q pgstore.Querier, key uuid.UUID) {
		for i := range 5 {
			_, err := sc.table.Append(ctx, q, key, "n", settings.IntValue(int64(i)))
			require.NoError(t, err)
		}
		_, err := sc.table.Append(ctx, q, key, "other", settings.IntValue(99))
		require.NoError(t, err)

		all, err := sc.table.History(ctx, q, key, "n", 0, 10)
		require.NoError(t, err)
		require.Len(t, all, 5)
		for i, ch := range all {
			assert.Equal(t, settings.IntValue(int64(4-i)), ch.Value)
			assert.False(t, ch.EarlierMembership)
		}

		page, err := sc.table.History(ctx, q, key, "n", all[1].ID, 2)
		require.NoError(t, err)
		require.Len(t, page, 2)
		assert.Equal(t, all[2].ID, page[0].ID)
		assert.Equal(t, all[3].ID, page[1].ID)
	}},
	{"refuses a type it cannot store", func(t *testing.T, _ *Env, sc scope, ctx context.Context, q pgstore.Querier, key uuid.UUID) {
		_, err := sc.table.Append(ctx, q, key, "a", settings.Value{Type: "jsonb"})
		require.Error(t, err)
		assert.Empty(t, latestOf(t, sc, ctx, q, key, "a"))
	}},
	{"an unknown key has no rows", func(t *testing.T, _ *Env, sc scope, ctx context.Context, q pgstore.Querier, key uuid.UUID) {
		_, err := sc.table.Append(ctx, q, key, "a", settings.BoolValue(true))
		require.NoError(t, err)

		other := uuid.New()
		assert.Empty(t, latestOf(t, sc, ctx, q, other, "a"))
		history, err := sc.table.History(ctx, q, other, "a", 0, 10)
		require.NoError(t, err)
		assert.Empty(t, history)
	}},
}

// isolation writes the same name in every scope, and in every actor's, and
// checks that each read finds only its own.
func isolation(t *testing.T, f Fixture) {
	ctx := context.Background()
	env := f.Open(t)
	scopes := f.scopes()

	// Each owner writes "shared" in each scope, with a value naming the scope
	// and the owner. Two members of a tenant share its tenant-wide key, so the
	// first of them writes it.
	value := func(sc scope, key uuid.UUID) settings.Value {
		return settings.StrValue(sc.name + "/" + key.String())
	}
	written := map[string]bool{}
	for _, a := range env.Members {
		require.NoError(t, env.As(ctx, a, func(ctx context.Context, q pgstore.Querier) error {
			for _, sc := range scopes {
				key := sc.key(a)
				if written[sc.name+key.String()] {
					continue
				}
				written[sc.name+key.String()] = true
				if _, err := sc.table.Append(ctx, q, key, "shared", value(sc, key)); err != nil {
					return err
				}
			}
			return nil
		}))
	}

	for who, a := range env.Members {
		require.NoError(t, env.As(ctx, a, func(ctx context.Context, q pgstore.Querier) error {
			for _, sc := range scopes {
				// Its own key reads its own value, and only the scope's own table
				// is read: the value names the scope it was written in.
				own := sc.key(a)
				assert.Equal(t, value(sc, own), latestOf(t, sc, ctx, q, own, "shared")["shared"].Value, "%s, member %d", sc.name, who)
				history, err := sc.table.History(ctx, q, own, "shared", 0, 10)
				require.NoError(t, err)
				require.Len(t, history, 1, "%s, member %d", sc.name, who)
				assert.Equal(t, value(sc, own), history[0].Value)

				// Another owner's key reads nothing, whether it is in the same
				// tenant or another: tenant row-level security for a tenant's,
				// and the per-user policy for a user's. A global table has no
				// such policy, as the user's id is its only guard, so another
				// user's key there is the app's to withhold.
				if sc.table == f.Global {
					continue
				}
				for other, o := range env.Members {
					if sc.key(o) == own {
						continue
					}
					assert.Empty(t, latestOf(t, sc, ctx, q, sc.key(o), "shared"), "%s: member %d reading member %d", sc.name, who, other)
					history, err := sc.table.History(ctx, q, sc.key(o), "shared", 0, 10)
					require.NoError(t, err)
					assert.Empty(t, history, "%s: member %d reading member %d", sc.name, who, other)
				}
			}
			return nil
		}))
	}
}

func membership(t *testing.T, f Fixture) {
	env := f.Open(t)
	// The rule is in the shared SQL, so a non-member is refused here and not
	// only by the app's guard in front of it.
	ctx := context.Background()
	err := env.As(ctx, env.Stranger, func(ctx context.Context, q pgstore.Querier) error {
		_, err := f.UserTenant.Append(ctx, q, env.Stranger.User, "a", settings.BoolValue(true))
		return err
	})
	require.ErrorIs(t, err, pgx.ErrNoRows)

	// Someone who left and rejoined starts again from the defaults: what they
	// set before is history, never the current value.
	require.NotNil(t, env.Rejoin, "a table with a membership rule needs Env.Rejoin")
	me := env.Members[0]
	table := f.UserTenant
	require.NoError(t, env.As(ctx, me, func(ctx context.Context, q pgstore.Querier) error {
		_, err := table.Append(ctx, q, me.User, "a", settings.StrValue("before"))
		return err
	}))
	require.NoError(t, env.Rejoin(ctx, me))
	require.NoError(t, env.As(ctx, me, func(ctx context.Context, q pgstore.Querier) error {
		rows, err := table.Latest(ctx, q, me.User, []string{"a"})
		require.NoError(t, err)
		assert.Empty(t, rows, "a value from an earlier membership must not be current")

		_, err = table.Append(ctx, q, me.User, "a", settings.StrValue("after"))
		require.NoError(t, err)
		rows, err = table.Latest(ctx, q, me.User, []string{"a"})
		require.NoError(t, err)
		require.Len(t, rows, 1)
		assert.Equal(t, settings.StrValue("after"), rows[0].Value)

		history, err := table.History(ctx, q, me.User, "a", 0, 10)
		require.NoError(t, err)
		require.Len(t, history, 2)
		assert.False(t, history[0].EarlierMembership)
		assert.True(t, history[1].EarlierMembership)
		return nil
	}))
}

func actor(t *testing.T, f Fixture) {
	ctx := context.Background()
	env := f.Open(t)
	me := env.Members[0]
	require.NoError(t, env.As(ctx, me, func(ctx context.Context, q pgstore.Querier) error {
		table := f.Tenant
		_, err := table.AppendBy(ctx, q, me.Tenant, "a", settings.BoolValue(true), me.User)
		require.NoError(t, err)
		_, err = table.Append(ctx, q, me.Tenant, "b", settings.BoolValue(true))
		require.NoError(t, err)

		changes := func(name string) []settings.Change {
			c, err := table.History(ctx, q, me.Tenant, name, 0, 10)
			require.NoError(t, err)
			require.Len(t, c, 1)
			return c
		}
		by := changes("a")[0]
		require.NotNil(t, by.ChangedBy)
		assert.Equal(t, me.User, *by.ChangedBy)
		assert.False(t, by.ChangedByDeleted)
		assert.Nil(t, changes("b")[0].ChangedBy, "Append records no one")

		_, err = table.AppendBy(ctx, q, me.Tenant, "c", settings.BoolValue(true), uuid.Nil)
		assert.Error(t, err, "a write by no one is refused")
		if f.Global != nil {
			_, err = f.Global.AppendBy(ctx, q, me.User, "c", settings.BoolValue(true), me.User)
			assert.Error(t, err, "a table without an actor column cannot record one")
		}
		return nil
	}))
}
