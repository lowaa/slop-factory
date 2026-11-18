// Package smoke provisions what the deploy pipeline's smoke test signs in
// as: a read-only client for an agent in a tenant of its own, holding one
// ticket to read.
//
// A tenant of its own, with no members, so the credential CI holds can read
// nothing anyone else wrote -- the worst a leak of it gives away is this
// package's fixture ticket.
package smoke

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/agent"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/oauth"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// TenantID is fixed so provisioning is idempotent: a second run finds the
// tenant, agent and ticket the first made rather than making more.
var TenantID = uuid.MustParse("5a0be7e5-0000-4000-8000-000000000001")

const (
	tenantName  = "slop-haus smoke test"
	agentName   = "smoke-test"
	ticketTitle = "Smoke test fixture"
	ticketBody  = "Read by the deploy pipeline's smoke test to check a signed-in MCP read works end to end. Leave it be."
)

// Credential is what the smoke test needs: the client's id and secret,
// and the ticket to read.
type Credential struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	TicketID     int64  `json:"ticket_id"`
}

type Provisioner struct {
	db      *db.DB
	agents  *agent.Service
	tickets *ticket.Service
	clients *oauth.ClientAdmin
	logger  *slog.Logger
}

type Deps struct {
	DB      *db.DB
	Agents  *agent.Service
	Tickets *ticket.Service
	Clients *oauth.ClientAdmin
}

func NewProvisioner(deps *Deps, logHandler slog.Handler) *Provisioner {
	return &Provisioner{db: deps.DB, agents: deps.Agents, tickets: deps.Tickets, clients: deps.Clients, logger: slog.New(logHandler)}
}

// Create makes a new read-only client, first making the tenant, agent and
// ticket it reads if they don't exist yet -- found again on a later run,
// not made twice. Runs are an operator's, one at a time: two at once could
// each make a fixture ticket (harmless), or one fail on the agent's name
// (rerun it). Each call is a new client with
// a new secret; the old ones stay valid until revoked, so a secret can be
// rotated without a window where the smoke test has none.
func (p *Provisioner) Create(ctx context.Context, clientName string) (*Credential, error) {
	if err := p.ensureTenant(ctx); err != nil {
		return nil, err
	}
	ctx = db.WithTenant(ctx, TenantID)
	a, err := p.ensureAgent(ctx)
	if err != nil {
		return nil, err
	}
	t, err := p.ensureTicket(ctx, a.ID)
	if err != nil {
		return nil, err
	}
	id, secret, err := p.clients.CreateReadOnlyClient(ctx, TenantID, a.ID, clientName)
	if err != nil {
		return nil, fmt.Errorf("smoke: create client: %w", err)
	}
	return &Credential{ClientID: id, ClientSecret: secret, TicketID: t.ID}, nil
}

// Revoke ends a client Create made.
func (p *Provisioner) Revoke(ctx context.Context, clientID string) error {
	return p.clients.RevokeClient(ctx, clientID)
}

func (p *Provisioner) ensureTenant(ctx context.Context) error {
	if _, err := p.db.Global().Exec(ctx, `
		INSERT INTO global_tenants (id, name) VALUES ($1, $2) ON CONFLICT (id) DO NOTHING`,
		TenantID, tenantName); err != nil {
		return fmt.Errorf("smoke: ensure tenant: %w", err)
	}
	return nil
}

func (p *Provisioner) ensureAgent(ctx context.Context) (*agent.Agent, error) {
	agents, err := p.agents.List(ctx, true)
	if err != nil {
		return nil, fmt.Errorf("smoke: list agents: %w", err)
	}
	for i := range agents {
		if agents[i].Name == agentName {
			return &agents[i], nil
		}
	}
	a, err := p.agents.Create(ctx, agentName)
	if err != nil {
		return nil, fmt.Errorf("smoke: create agent: %w", err)
	}
	p.logger.Info("created smoke agent", "agent_id", a.ID)
	return a, nil
}

func (p *Provisioner) ensureTicket(ctx context.Context, agentID uuid.UUID) (*ticket.Ticket, error) {
	tickets, err := p.tickets.List(ctx, &ticket.ListFilter{})
	if err != nil {
		return nil, fmt.Errorf("smoke: list tickets: %w", err)
	}
	for i := range tickets {
		if tickets[i].Title == ticketTitle {
			return &tickets[i], nil
		}
	}
	// Preparing, not open: nothing should ever claim it. Nothing can --
	// the tenant has no members to sign a working agent in -- but a
	// fixture that reads as work waiting would mislead anyone who looks.
	t, err := p.tickets.Create(ctx, &ticket.CreateParams{
		Title: ticketTitle, Body: ticketBody, Status: ticket.StatusPreparing, CreatedByAgentID: &agentID,
	})
	if err != nil {
		return nil, fmt.Errorf("smoke: create ticket: %w", err)
	}
	p.logger.Info("created smoke ticket", "ticket_id", t.ID)
	return t, nil
}
