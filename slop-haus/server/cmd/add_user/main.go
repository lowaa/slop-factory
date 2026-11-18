// Command add_user creates an account that is active from the start, with
// a tenant of its own -- the default account development and the e2e
// suite sign in as, without an emailed link to follow first.
//
// Idempotent: an address that already has an account keeps its password
// and name, so it can run on every environment start. --admin is the one
// thing applied to an existing account too, so an environment's first
// account can be made the admin who invites everyone else.
package main

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"os"

	"github.com/google/uuid"
	"github.com/spf13/pflag"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/settings"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/user"
)

// tenantOwners is settings.Owners over the user store.
type tenantOwners struct{ store *user.PostgresStore }

func (o tenantOwners) IsTenantOwner(ctx context.Context, userID, tenantID uuid.UUID) (bool, error) {
	return o.store.IsOwner(ctx, userID, tenantID)
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		log.Fatal(err)
	}
}

func run(args []string) error {
	fs := pflag.NewFlagSet("add_user", pflag.ContinueOnError)
	email := fs.String("email", "", "the account's email address")
	displayName := fs.String("display-name", "", "the name shown on comments")
	admin := fs.Bool("admin", false, "make the account a server admin (may invite people to start tenants); applied to an existing account too")
	if err := fs.Parse(args); err != nil {
		return err
	}
	// From the environment, not a flag: a flag is visible to every user
	// on the machine through the process list.
	password := os.Getenv("ADD_USER_PASSWORD")
	databaseURL := os.Getenv("DATABASE_URL")
	if *email == "" || *displayName == "" || password == "" || databaseURL == "" {
		return fmt.Errorf("usage: DATABASE_URL=... ADD_USER_PASSWORD=... add_user --email=... --display-name=...")
	}

	ctx := context.Background()
	// Migrations are the server's job; this runs against a database the
	// server has already brought up to date. The account and tenant are
	// global_ tables, which RLS does not cover; the tenant's starting
	// settings are tenant-scoped, written as the app role would.
	database, err := db.New(ctx, databaseURL)
	if err != nil {
		return err
	}
	defer database.Close()

	// Without this, the seeded tenant would be the one tenant that does not
	// start with provision_from_board on. No import hook: a tenant this new
	// has no reported slots.
	logHandler := slog.Default().Handler()
	store := user.NewPostgresStore(database)
	startingSettings := settings.NewTenantStartingSettings(
		settings.NewService(settings.NewPostgresStore(database), audit.New(logHandler, nil), logHandler, settings.WithOwners(tenantOwners{store})))
	store = user.NewPostgresStore(database, user.WithTenantCreatedHook(startingSettings, logHandler))
	seeder := user.NewSeeder(store)
	created, err := seeder.EnsureActive(ctx, &user.RegisterInput{
		Email: *email, Password: password, DisplayName: *displayName,
	})
	if err != nil {
		return fmt.Errorf("add user: %w", err)
	}
	if created {
		slog.Info("created active user", "email", *email)
	} else {
		slog.Info("user already exists; left unchanged", "email", *email)
	}
	if *admin {
		if err := seeder.MakeAdmin(ctx, *email); err != nil {
			return fmt.Errorf("make admin: %w", err)
		}
		slog.Info("user is an admin", "email", *email)
	}
	return nil
}
