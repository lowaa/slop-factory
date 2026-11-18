// Package demo holds the schema used to demonstrate go-db's tenancy model
// end-to-end: a global tenant registry (global_tenants) and reference
// currency table (global_currencies), plus a tenant-scoped accounts/
// account_items pair showing the composite (tenant_id, parent_id) foreign
// key pattern.
//
// go-db is a shared library (engine/tenancy/migrations/dbtest); this schema
// is demo content built on top of it, backing cmd/demo-api and the e2e
// suite, not something the library packages themselves know about.
package demo

import (
	"embed"
	"io/fs"
)

//go:embed migrations
var migrationsFS embed.FS

// Migrations is rooted directly at the migration SQL files (no
// "migrations/" path prefix), matching what migrations.Up/Down/Version and
// migrations.LintGlobalTenantInvariants expect.
var Migrations = mustSub(migrationsFS, "migrations")

func mustSub(fsys embed.FS, dir string) fs.FS {
	sub, err := fs.Sub(fsys, dir)
	if err != nil {
		panic(err)
	}
	return sub
}
