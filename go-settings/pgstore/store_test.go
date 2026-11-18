package pgstore_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	settings "github.com/lowaa/slop-factory/go-settings"
	"github.com/lowaa/slop-factory/go-settings/pgstore"
	"github.com/lowaa/slop-factory/go-settings/settingstest"
)

func TestConformance(t *testing.T) {
	settingstest.Run(t, settingstest.Fixture{
		Global:     globalTable,
		UserTenant: userTenantTable,
		Tenant:     tenantTable,
		Open:       openEnv,
	})
}

func TestConformance_WithoutAMembershipRuleOrActor(t *testing.T) {
	// A table the app declares without a membership rule skips those checks
	// rather than failing them.
	settingstest.Run(t, settingstest.Fixture{
		UserTenant: pgstore.UserTenantTable("user_tenant_settings", nil),
		Open:       openEnv,
	})
}

func TestStore_RunsEachCallInTheRunnersScope(t *testing.T) {
	env := openEnv(t)
	me := env.Members[0]
	store := pgstore.NewStore(tenantTable, func(ctx context.Context, fn func(q pgstore.Querier) error) error {
		return env.As(ctx, me, func(_ context.Context, q pgstore.Querier) error { return fn(q) })
	})
	ctx := context.Background()

	_, err := store.AppendBy(ctx, me.Tenant, "a", settings.StrValue("one"), me.User)
	require.NoError(t, err)
	_, err = store.Append(ctx, me.Tenant, "a", settings.StrValue("two"))
	require.NoError(t, err)

	latest, err := store.Latest(ctx, me.Tenant, []string{"a"})
	require.NoError(t, err)
	require.Len(t, latest, 1)
	assert.Equal(t, settings.StrValue("two"), latest[0].Value)

	history, err := store.History(ctx, me.Tenant, "a", 0, 10)
	require.NoError(t, err)
	require.Len(t, history, 2)
	require.NotNil(t, history[1].ChangedBy)
	assert.Equal(t, me.User, *history[1].ChangedBy)
}

func TestTable_RefusesANameThatIsNotAnIdentifier(t *testing.T) {
	for _, name := range []string{"", "a b", "t; DROP TABLE x", "Upper", "1table", "a-b"} {
		assert.Panics(t, func() { pgstore.TenantTable(name) }, name)
	}
	assert.Panics(t, func() { pgstore.UserTenantTable("ok", &pgstore.Membership{Table: "x y"}) })
}
