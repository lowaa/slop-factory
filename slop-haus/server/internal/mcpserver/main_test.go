package mcpserver_test

import (
	"testing"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/schema"
)

// Most of this package's tests drive the tools against fakes; the few that
// follow a change through the real ticket store need a database.
func TestMain(m *testing.M) {
	dbtest.Main(m, schema.Migrations)
}
