package manifest_test

import (
	"testing"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/schema"
)

func TestMain(m *testing.M) {
	dbtest.Main(m, schema.Migrations)
}
