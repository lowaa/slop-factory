package settings_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/settings"
)

// The processing switch is one tenant's setting, not the server's: pausing
// one tenant's fleet must never pause another's. Two tenants
// share one database (rather than two separate dbtest.WithDB databases, which
// would prove nothing about RLS).
func TestTenancy_ProcessingSwitchInOneTenantDoesNotAffectAnother(t *testing.T) {
	database := dbtest.WithDB(t)
	ctxA := db.WithTenant(context.Background(), dbtest.NewTenant(t, database))
	ctxB := db.WithTenant(context.Background(), dbtest.NewTenant(t, database))
	user := dbtest.NewUser(t, database)

	svcA := newService(settings.NewPostgresStore(database))
	svcB := newService(settings.NewPostgresStore(database))

	enabledA, err := svcA.TicketProcessingEnabled(ctxA)
	require.NoError(t, err)
	assert.True(t, enabledA, "nothing stored yet: the declared default")

	_, err = svcA.Set(ctxA, settings.TicketProcessingEnabled, settings.BoolValue(false), user)
	require.NoError(t, err)

	enabledA, err = svcA.TicketProcessingEnabled(ctxA)
	require.NoError(t, err)
	assert.False(t, enabledA, "tenant A's own pause must take")

	enabledB, err := svcB.TicketProcessingEnabled(ctxB)
	require.NoError(t, err)
	assert.True(t, enabledB, "tenant B must still see the default; A's pause is not the server's")

	// And the reverse: writing B does not disturb A's row.
	_, err = svcB.Set(ctxB, settings.TicketProcessingEnabled, settings.BoolValue(false), user)
	require.NoError(t, err)
	_, err = svcB.Set(ctxB, settings.TicketProcessingEnabled, settings.BoolValue(true), user)
	require.NoError(t, err)

	enabledA, err = svcA.TicketProcessingEnabled(ctxA)
	require.NoError(t, err)
	assert.False(t, enabledA, "tenant B's writes must not touch tenant A's setting")
}
