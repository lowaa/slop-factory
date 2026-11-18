// Command demo-api is a minimal HTTP server over the module's demo schema
// (global_tenants/global_currencies/accounts/account_items). It exists so
// docker-compose-dev.yaml has something runnable to manually verify a
// simple setup against, and so the e2e_test suite has an HTTP surface to
// drive -- it is deliberately not part of the library's public API.
package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/lowaa/slop-factory/go-db/engine"
	"github.com/lowaa/slop-factory/go-db/internal/demo"
	"github.com/lowaa/slop-factory/go-db/migrations"
)

// appRole is the NOSUPERUSER/NOBYPASSRLS role this server queries through.
// Migrations run as whatever DATABASE_URL's role is (expected to be a
// superuser/owner, since creating tables/policies/roles needs that), but
// serving requests through that same role would silently bypass RLS -- see
// migrations.EnsureRole.
const appRole = "demo_api"

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	adminDSN := os.Getenv("DATABASE_URL")
	if adminDSN == "" {
		return fmt.Errorf("DATABASE_URL is required")
	}
	appPassword := os.Getenv("DEMO_API_DB_PASSWORD")
	if appPassword == "" {
		appPassword = "demo_api_password"
	}
	addr := os.Getenv("LISTEN_ADDR")
	if addr == "" {
		addr = ":8080"
	}

	ctx := context.Background()

	adminEng := engine.NewPostgres(adminDSN)
	if err := adminEng.Connect(ctx); err != nil {
		return fmt.Errorf("connect admin engine: %w", err)
	}
	defer func() { _ = adminEng.Close() }()

	log.Println("running migrations...")
	if err := migrations.Up(ctx, adminEng, demo.Migrations); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}

	log.Println("ensuring app role...")
	if err := migrations.EnsureRole(ctx, adminEng, appRole, appPassword); err != nil {
		return fmt.Errorf("ensure app role: %w", err)
	}

	appDSN, err := withCredentials(adminDSN, appRole, appPassword)
	if err != nil {
		return err
	}

	appEng := engine.NewPostgres(appDSN)
	if err := appEng.Connect(ctx); err != nil {
		return fmt.Errorf("connect app engine: %w", err)
	}
	defer func() { _ = appEng.Close() }()

	mux := http.NewServeMux()
	registerRoutes(mux, appEng)

	server := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	log.Printf("demo-api listening on %s", addr)
	return server.ListenAndServe()
}

// withCredentials returns dsn with its user/password replaced.
func withCredentials(dsn, user, password string) (string, error) {
	u, err := url.Parse(dsn)
	if err != nil {
		return "", fmt.Errorf("parse DATABASE_URL: %w", err)
	}
	u.User = url.UserPassword(user, password)
	return u.String(), nil
}
