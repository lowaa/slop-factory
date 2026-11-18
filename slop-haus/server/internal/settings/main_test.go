package settings_test

import (
	"testing"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/schema"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/settings"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/steward"
)

func TestMain(m *testing.M) {
	// The steward declares its own settings; the tests exercise them here.
	if err := settings.Register(steward.SettingSpecs()...); err != nil {
		panic(err)
	}
	dbtest.Main(m, schema.Migrations)
}
