package change_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/change"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
)

// The background loops that visit every tenant must not visit an archived one.
func TestStore_TenantsLeavesOutAnArchivedTenant(t *testing.T) {
	database := dbtest.WithDB(t)
	live, archived := dbtest.NewTenant(t, database), dbtest.NewTenant(t, database)
	dbtest.ArchiveTenant(t, database, archived)

	ids, err := change.NewPostgresStore(database).Tenants(context.Background())
	require.NoError(t, err)
	assert.Contains(t, ids, live)
	assert.NotContains(t, ids, archived)
}
