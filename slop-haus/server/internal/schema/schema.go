// Package schema embeds slop-haus's own database migrations, so the module
// is self-contained -- go-db's migration helpers take the source as an
// fs.FS rather than embedding one themselves.
package schema

import (
	"embed"
	"io/fs"
)

//go:embed migrations
var migrationsFS embed.FS

// Migrations is rooted directly at the migration SQL files, matching what
// migrations.Up/Down/Version expect.
var Migrations = mustSub(migrationsFS, "migrations")

func mustSub(fsys embed.FS, dir string) fs.FS {
	sub, err := fs.Sub(fsys, dir)
	if err != nil {
		panic(err)
	}
	return sub
}
