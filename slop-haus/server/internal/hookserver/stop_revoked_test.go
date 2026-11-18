package hookserver_test

import (
	"context"
	"log/slog"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/agent"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/hookserver"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

type tenantIdentity struct {
	id     uuid.UUID
	tenant uuid.UUID
}

func (i tenantIdentity) AgentID() uuid.UUID  { return i.id }
func (i tenantIdentity) AgentName() string   { return "builder-1" }
func (i tenantIdentity) TenantID() uuid.UUID { return i.tenant }
func (i tenantIdentity) ReadOnly() bool      { return false }

// After a person moves an agent's ticket to Preparing, the Stop hook must
// see an agent holding nothing: no "you still hold #N, finish it", which
// would send it straight back to the work it was just told to drop. From
// there it is an ordinary idle agent and is let stop.
func TestStop_AfterAHumanMovesTheHeldTicketToPreparing(t *testing.T) {
	database := dbtest.WithDB(t)
	tenantID := dbtest.NewTenant(t, database)
	ctx := db.WithTenant(context.Background(), tenantID)
	tickets := ticket.NewService(ticket.NewPostgresStore(database), slog.DiscardHandler)
	agents := agent.NewService(agent.NewPostgresStore(database), tickets, nil, slog.DiscardHandler)
	builder, err := agents.Create(ctx, "builder-1")
	require.NoError(t, err)

	sessions := &fakeSessions{}
	handler := hookserver.NewHandler(hookserver.Deps{
		Tickets: tickets, Sessions: sessions,
	}, slog.DiscardHandler)
	hookCtx := authn.NewContext(context.Background(), tenantIdentity{id: builder.ID, tenant: tenantID})

	held, err := tickets.Create(ctx, &ticket.CreateParams{Title: "Being worked", Paths: []model.Path{{Path: "server/"}}})
	require.NoError(t, err)
	_, err = tickets.Claim(ctx, builder.ID, nil)
	require.NoError(t, err)

	decision, err := handler.Stop(hookCtx, &hookserver.StopInput{SessionID: "s1"})
	require.NoError(t, err)
	require.Contains(t, decision.Reason, "You still hold ticket", "precondition: holding blocks the stop")

	status := ticket.StatusPreparing
	_, err = tickets.Update(ctx, held.ID, &ticket.UpdateParams{Status: &status})
	require.NoError(t, err)

	decision, err = handler.Stop(hookCtx, &hookserver.StopInput{SessionID: "s1"})
	require.NoError(t, err)
	assert.Empty(t, decision.Decision, "holding nothing, it is let stop")

	// The hook only reads: the notice is still there for the agent's next
	// tool call to deliver.
	assert.ErrorIs(t, tickets.Heartbeat(ctx, builder.ID), ticket.ErrLeaseRevoked)
}
