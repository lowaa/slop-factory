package agentmsg_test

import (
	"testing"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/schema"
)

// One Postgres container per test binary, migrated once into a template
// database that each test clones. See internal/db/dbtest for why cloning
// beats wrapping each test in a rolled-back transaction here.
func TestMain(m *testing.M) {
	dbtest.Main(m, schema.Migrations)
}
