package attachment_test

import (
	"fmt"
	"os"
	"testing"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/attachment"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/schema"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/settings"
)

func TestMain(m *testing.M) {
	if err := settings.Register(attachment.SettingSpecs()...); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	dbtest.Main(m, schema.Migrations)
}
