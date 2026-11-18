package pgstore_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-db/dbtest"
	"github.com/lowaa/slop-factory/go-db/engine"
	"github.com/lowaa/slop-factory/go-db/tenancy"
	"github.com/lowaa/slop-factory/go-settings/pgstore"
	"github.com/lowaa/slop-factory/go-settings/settingstest"
)

var (
	globalTable     = pgstore.GlobalUserTable("global_user_settings")
	userTenantTable = pgstore.UserTenantTable("user_tenant_settings", &pgstore.Membership{Table: "global_tenant_memberships"})
	tenantTable     = pgstore.TenantTable("tenant_settings")
)

// asActor runs fn in a transaction scoped as an app scopes a request: the
// tenant through go-db's tenancy, the user as app.current_user_id.
func asActor(eng engine.Engine) func(context.Context, settingstest.Actor, func(context.Context, pgstore.Querier) error) error {
	return func(ctx context.Context, a settingstest.Actor, fn func(context.Context, pgstore.Querier) error) error {
		tx, err := tenancy.NewTenantTx(ctx, eng, a.Tenant)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `SELECT set_config('app.current_user_id', $1, true)`, a.User.String()); err != nil {
			_ = tx.Rollback(ctx)
			return err
		}
		// A failed require inside fn exits the goroutine, not just fn, so the
		// rollback is deferred or the open transaction would hang the cleanup.
		committed := false
		defer func() {
			if !committed {
				_ = tx.Rollback(ctx)
			}
		}()
		if err := fn(tenancy.ContextWithTenantID(ctx, a.Tenant), pgstore.FromEngine(tx)); err != nil {
			return err
		}
		committed = true
		return tx.Commit(ctx)
	}
}

func openEnv(t *testing.T) *settingstest.Env {
	t.Helper()
	ctx := context.Background()
	eng := dbtest.WithCommittedDB(t)

	tenants := dbtest.SeedTenants(t, eng, 2)
	newUser := func() uuid.UUID {
		id := uuid.New()
		_, err := eng.ExecContext(ctx, `INSERT INTO global_users (id) VALUES ($1)`, id)
		require.NoError(t, err)
		return id
	}
	member := func(tenant uuid.UUID) settingstest.Actor {
		a := settingstest.Actor{Tenant: tenant, User: newUser()}
		_, err := eng.ExecContext(ctx,
			`INSERT INTO global_tenant_memberships (tenant, user_id, created_at) VALUES ($1, $2, $3)`,
			a.Tenant, a.User, time.Now().Add(-time.Hour))
		require.NoError(t, err)
		return a
	}
	return &settingstest.Env{
		Members:  [3]settingstest.Actor{member(tenants[0]), member(tenants[0]), member(tenants[1])},
		Stranger: settingstest.Actor{Tenant: tenants[0], User: newUser()},
		As:       asActor(eng),
		Rejoin: func(ctx context.Context, a settingstest.Actor) error {
			_, err := eng.ExecContext(ctx,
				`UPDATE global_tenant_memberships SET created_at = now() WHERE tenant = $1 AND user_id = $2`, a.Tenant, a.User)
			return err
		},
	}
}
