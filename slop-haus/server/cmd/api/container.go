package main

import (
	"context"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/landing"
	"log/slog"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/config"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/manifest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/secretbox"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/tenantstate"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/user"
)

// container holds what more than one module is built from: the platform
// pieces and the stores shared between services. Each is built once here, so
// two modules never hold two copies of what should be one.
type container struct {
	cfg        *config.Config
	database   *db.DB
	events     *audit.Recorder
	limits     *limiters
	users      *user.Service
	logHandler slog.Handler

	// tenants answers whether a tenant is archived.
	tenants *tenantstate.Checker
	// keyring seals the tenants' credentials; nil unless
	// CREDENTIAL_ENCRYPTION_KEY is set.
	keyring *secretbox.Keyring

	// cells are the late-bound dependencies built from the container's
	// pieces; services.go verifies them once wiring is done.
	cells *lateCells
	// tenantAgents switches off a tenant's agents on archive; bound to the
	// agent service, which is built after the user service.
	tenantAgents *Late[user.TenantAgents]
	// tenantCreated is bound to the new-tenant settings once settings exists.
	tenantCreated *Late[user.TenantCreatedHook]
	// hostChanges is GitHub's change operations, which need the credential
	// service (see newCodeHostModule).
	hostChanges *Late[codehost.Changes]
	// workspace is the manifest policy the ticket store and service read.
	workspace ticket.Workspace
	// settled is the change flow's closing rule, which the ticket store
	// reports to but which is built from the ticket service.
	settled *Late[ticket.SettledHook]

	ticketStore   *ticket.PostgresStore
	manifestStore *manifest.PostgresStore
}

func newContainer(cfg *config.Config, database *db.DB, events *audit.Recorder, limits *limiters, users *user.Service, late *userLate, logHandler slog.Handler) (*container, error) {
	c := &container{
		cfg: cfg, database: database, events: events, limits: limits, users: users, logHandler: logHandler,
		tenants:       tenantstate.NewChecker(database),
		manifestStore: manifest.NewPostgresStore(database),
		cells:         late.cells,
		tenantAgents:  late.tenantAgents,
		tenantCreated: late.tenantCreated,
	}
	c.workspace = manifest.NewPolicy(c.manifestStore)
	c.hostChanges = newLate[codehost.Changes](c.cells, "github change operations")
	c.settled = newLate[ticket.SettledHook](c.cells, "ticket settled hook")
	c.ticketStore = ticket.NewPostgresStore(database, ticket.WithStoreWorkspace(c.workspace), ticket.WithMergeQueue(landing.NewDequeuer()), ticket.WithSettledHook(func(ctx context.Context, ids []int64) {
		c.settled.Get()(ctx, ids)
	}))
	if cfg.Credentials != nil {
		var err error
		if c.keyring, err = secretbox.NewKeyring(cfg.Credentials.Key, cfg.Credentials.PreviousKey); err != nil {
			return nil, err
		}
	}
	return c, nil
}
