// Command upgrade_data fills a slop-haus database with rows in every table,
// across several tenants, and later checks that they survived a migration.
// An upgrade test runs
// it either side of the upgrade, from the image being upgraded to, which
// is the one that knows today's tables:
//
//	upgrade_data seed > state.json            rows written to the database as it is now
//	upgrade_data check [state.json]  the database after the upgrade holds them
//	                                 (the state on stdin without a file)
//
// It reads the server's own DATABASE_URL (the owner's), APP_DB_ROLE and
// APP_DB_PASSWORD, and writes as the app role, as the server does: the rows
// pass through row level security, which the owner's connection would not.
// The state is what was written, so the check needs nothing else.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/schema/seeddata"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

const usage = "usage: DATABASE_URL=... APP_DB_PASSWORD=... [APP_DB_ROLE=slophaus_app] upgrade_data seed >state.json | upgrade_data check [state.json]"

// state is what seed leaves for check.
type state struct {
	Version  int64              `json:"version"`
	Tenants  []uuid.UUID        `json:"tenants"`
	Snapshot *seeddata.Snapshot `json:"snapshot"`
}

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		log.Fatal(err)
	}
}

func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) == 0 || (args[0] != "seed" && args[0] != "check") || (args[0] == "seed" && len(args) != 1) || len(args) > 2 {
		return fmt.Errorf("%s", usage)
	}
	app, err := openApp()
	if err != nil {
		return err
	}
	defer app.Close()

	if args[0] == "seed" {
		return seed(ctx, app, stdout, stderr)
	}
	if len(args) == 2 {
		f, err := os.Open(args[1])
		if err != nil {
			return err
		}
		defer func() { _ = f.Close() }()
		stdin = f
	}
	return check(ctx, app, stdin, stderr)
}

func openApp() (*db.DB, error) {
	databaseURL, password := os.Getenv("DATABASE_URL"), os.Getenv("APP_DB_PASSWORD")
	role := os.Getenv("APP_DB_ROLE")
	if role == "" {
		role = "slophaus_app"
	}
	if databaseURL == "" || password == "" {
		return nil, fmt.Errorf("%s", usage)
	}
	dsn, err := db.WithCredentials(databaseURL, role, password)
	if err != nil {
		return nil, err
	}
	return db.New(context.Background(), dsn)
}

func seed(ctx context.Context, app *db.DB, stdout, stderr io.Writer) error {
	version, err := seeddata.SchemaVersion(ctx, app.Global())
	if err != nil {
		return err
	}
	tenants := []uuid.UUID{uuid.New(), uuid.New(), uuid.New()}
	seeding := seeddata.NewSeeding(app, seeddata.Seeders, tenants)
	// The database is the revision being upgraded from, which may have tables
	// this one does not know. That is not this test's to fail: the walk over
	// every migration in the package tests is what requires a seeder for each.
	seeding.SkipUnseeded = true
	seeding.Epoch = liveEpoch()
	if err := seeding.Round(ctx, version, 0); err != nil {
		return fmt.Errorf("seed at schema version %d: %w", version, err)
	}
	if len(seeding.Skipped) > 0 {
		fmt.Fprintf(stderr, "no seeder for %s; left empty\n", strings.Join(seeding.Skipped, ", "))
	}
	if err := registerServerAgents(ctx, app, tenants); err != nil {
		return err
	}
	snap, err := seeddata.TakeSnapshot(ctx, app, tenants)
	if err != nil {
		return err
	}
	seeding.OnlySeededGlobalRows(snap)
	fmt.Fprintf(stderr, "seeded %d tenants at schema version %d\n", len(tenants), version)
	return json.NewEncoder(stdout).Encode(&state{Version: version, Tenants: tenants, Snapshot: snap})
}

// registerServerAgents registers each tenant's server agent before the
// snapshot. The upgraded api's landing runner registers it on its first tick
// (EnsureServerAgent), racing the check; done here, the rows it adds are in
// the snapshot and the runner's call changes nothing.
func registerServerAgents(ctx context.Context, app *db.DB, tenants []uuid.UUID) error {
	store := ticket.NewPostgresStore(app)
	for _, tenant := range tenants {
		if _, err := store.EnsureServerAgent(db.WithTenant(ctx, tenant)); err != nil {
			return fmt.Errorf("register the server agent of tenant %s: %w", tenant, err)
		}
	}
	return nil
}

func check(ctx context.Context, app *db.DB, stdin io.Reader, stderr io.Writer) error {
	var before state
	if err := json.NewDecoder(stdin).Decode(&before); err != nil {
		return fmt.Errorf("read the state seed wrote: %w", err)
	}
	version, err := seeddata.SchemaVersion(ctx, app.Global())
	if err != nil {
		return err
	}
	after, err := seeddata.TakeSnapshot(ctx, app, before.Tenants)
	if err != nil {
		return err
	}
	if problems := seeddata.Compare(before.Snapshot, after, seeddata.NotesBetween(before.Version, version)); len(problems) > 0 {
		return fmt.Errorf("the seeded rows did not survive the upgrade from schema version %d to %d:\n%s",
			before.Version, version, strings.Join(problems, "\n"))
	}
	fmt.Fprintf(stderr, "ok: the rows seeded at schema version %d read back at %d\n", before.Version, version)

	// The upgraded schema takes new rows in every table: its defaults,
	// constraints and grants work for what the app writes next.
	seeding := seeddata.NewSeeding(app, seeddata.Seeders, before.Tenants)
	seeding.SkipUnseeded = true
	seeding.Epoch = liveEpoch()
	if err := seeding.Round(ctx, version, 1); err != nil {
		return fmt.Errorf("seed at schema version %d: %w", version, err)
	}
	fmt.Fprintf(stderr, "ok: every table takes new rows at schema version %d\n", version)
	return nil
}

// liveEpoch starts the rows' timestamps ahead of the clock, so that nothing
// seeded has expired or aged out for the app's pruner and sweepers to take
// while the upgrade test runs.
func liveEpoch() time.Time { return time.Now().UTC().Add(30 * 24 * time.Hour).Truncate(time.Hour) }
