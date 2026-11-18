// Command go-db is go-db's own CLI for developer-facing tasks that don't
// belong inside the library itself -- currently just scaffolding new
// migration folders (see internal/migrationgen), with room to grow more
// subcommands later.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/lowaa/slop-factory/go-db/internal/migrationgen"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		printUsage()
		return fmt.Errorf("go-db: no command given")
	}

	switch cmd := args[0]; cmd {
	case "create-migration":
		return runCreateMigration(args[1:])
	case "-h", "--help", "help":
		printUsage()
		return nil
	default:
		printUsage()
		return fmt.Errorf("go-db: unknown command %q", cmd)
	}
}

func printUsage() {
	fmt.Fprintln(os.Stderr, "usage: go-db <command> [flags]")
	fmt.Fprintln(os.Stderr, "commands:")
	fmt.Fprintln(os.Stderr, "  create-migration -f <folder> -n <name> [--tenant-scoped]")
	fmt.Fprintln(os.Stderr, "      scaffold a new migration file under <folder>, with the next")
	fmt.Fprintln(os.Stderr, "      sequential version number worked out automatically.")
}

func runCreateMigration(args []string) error {
	fs := flag.NewFlagSet("create-migration", flag.ContinueOnError)
	folder := fs.String("f", "", "migrations folder to add the new migration to (required)")
	name := fs.String("n", "", "migration name, lowercase snake_case (required)")
	tenantScoped := fs.Bool("tenant-scoped", false, "scaffold a tenant-scoped table (tenant_id column, index, go_db_apply_tenant_rls)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *folder == "" || *name == "" {
		fs.Usage()
		return fmt.Errorf("create-migration: -f and -n are both required")
	}

	created, err := migrationgen.Create(*folder, *name, *tenantScoped)
	if err != nil {
		return err
	}

	fmt.Println("created", created)
	return nil
}
