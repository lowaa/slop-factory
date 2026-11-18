// Command seed_scale fills an existing account's tenant with the scale
// seed (seeddata.SeedScale), for the browser render guard
// (e2e_playwright_ts/tests/lists_at_scale.spec.ts) and for looking at the
// lists at size by hand. The mix has one definition, in seeddata; this only
// points it at a tenant.
//
// Run it once, into a tenant with no tickets: SeedScale places blocking
// links and proposals by position.
package main

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"os"

	"github.com/google/uuid"
	"github.com/spf13/pflag"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/schema/seeddata"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		log.Fatal(err)
	}
}

func run(args []string) error {
	fs := pflag.NewFlagSet("seed_scale", pflag.ContinueOnError)
	email := fs.String("email", "", "the account whose tenant is filled (add_user makes it)")
	tickets := fs.Int("tickets", seeddata.DefaultScale.Tickets, "how many tickets to write")
	if err := fs.Parse(args); err != nil {
		return err
	}
	// The owner's DSN, as add_user: SeedScale writes tenant tables directly.
	databaseURL := os.Getenv("DATABASE_URL")
	if *email == "" || databaseURL == "" {
		return fmt.Errorf("usage: DATABASE_URL=... seed_scale --email=... [--tickets=N]")
	}

	ctx := context.Background()
	database, err := db.New(ctx, databaseURL)
	if err != nil {
		return err
	}
	defer database.Close()

	tenant, err := tenantOf(ctx, database, *email)
	if err != nil {
		return err
	}
	sum, err := seeddata.SeedScale(ctx, database, tenant, seeddata.Scale{Tickets: *tickets})
	if err != nil {
		return err
	}
	slog.Info("seeded scale", "email", *email, "tenant", tenant, "tickets", sum.Tickets, "by_status", sum.ByStatus)
	return nil
}

func tenantOf(ctx context.Context, database *db.DB, email string) (uuid.UUID, error) {
	var tenant uuid.UUID
	err := database.Global().QueryRow(ctx, `
		SELECT m.tenant FROM global_tenant_memberships m
		JOIN global_users u ON u.id = m.user_id
		WHERE u.email = $1
		ORDER BY m.created_at LIMIT 1`, email).Scan(&tenant)
	if err != nil {
		return uuid.Nil, fmt.Errorf("find %s's tenant: %w", email, err)
	}
	return tenant, nil
}
