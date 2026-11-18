package pgstore_test

import (
	"embed"
	"io/fs"
	"testing"

	"github.com/lowaa/slop-factory/go-db/dbtest"
)

//go:embed testdata/migrations
var migrationsFS embed.FS

func testMigrations() fs.FS {
	sub, err := fs.Sub(migrationsFS, "testdata/migrations")
	if err != nil {
		panic(err)
	}
	return sub
}

func TestMain(m *testing.M) {
	dbtest.Main(m, testMigrations())
}
