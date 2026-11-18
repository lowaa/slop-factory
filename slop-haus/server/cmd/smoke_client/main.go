// Command smoke_client makes and revokes the read-only credential the
// deploy pipeline's smoke test signs in with (see internal/smoke).
//
//	smoke_client create --name=ci   prints {client_id, client_secret, ticket_id}
//	smoke_client revoke --client-id=ro-...
//
// Run against a database the server has already migrated, with the
// server's own DATABASE_URL and PUBLIC_BASE_URL -- in the cluster, by
// exec'ing into the api pod. The secret is printed once and never stored:
// pipe it straight to where it is kept.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"log/slog"
	"os"
	"strings"

	"github.com/spf13/pflag"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/agent"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/oauth"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/smoke"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

const usage = "usage: DATABASE_URL=... PUBLIC_BASE_URL=... smoke_client create --name=<name> | revoke --client-id=<id>"

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout); err != nil {
		log.Fatal(err)
	}
}

func run(ctx context.Context, args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("%s", usage)
	}
	fs := pflag.NewFlagSet("smoke_client "+args[0], pflag.ContinueOnError)
	name := fs.String("name", "", "create: what the client is for, shown in the audit log")
	clientID := fs.String("client-id", "", "revoke: the client to revoke")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	databaseURL, publicBaseURL := os.Getenv("DATABASE_URL"), os.Getenv("PUBLIC_BASE_URL")
	valid := (args[0] == "create" && *name != "") || (args[0] == "revoke" && *clientID != "")
	if !valid || databaseURL == "" || publicBaseURL == "" {
		return fmt.Errorf("%s", usage)
	}

	database, err := db.New(ctx, databaseURL)
	if err != nil {
		return err
	}
	defer database.Close()
	// Logs to stderr, so stdout is only the credential.
	logHandler := slog.NewTextHandler(os.Stderr, nil)
	// An operator's action, audited like the server's own.
	events := audit.New(logHandler, nil)
	provisioner := smoke.NewProvisioner(&smoke.Deps{
		DB:      database,
		Agents:  agent.NewService(agent.NewPostgresStore(database), nil, nil, logHandler),
		Tickets: ticket.NewService(ticket.NewPostgresStore(database), logHandler),
		Clients: oauth.NewClientAdmin(oauth.NewPostgresStore(database), oauth.ResourceFor(strings.TrimRight(publicBaseURL, "/")), events, logHandler),
	}, logHandler)

	switch args[0] {
	case "create":
		c, err := provisioner.Create(ctx, *name)
		if err != nil {
			return err
		}
		return json.NewEncoder(stdout).Encode(c)
	case "revoke":
		return provisioner.Revoke(ctx, *clientID)
	default:
		return fmt.Errorf("%s", usage)
	}
}
