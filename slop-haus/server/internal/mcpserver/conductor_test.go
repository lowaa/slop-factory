package mcpserver_test

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-auth/ratelimit"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/agent"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/oauth"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/pendingaction"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/user"
)

// conductorSession is an MCP session signed in as a conductor owned by a
// member of the rig's tenant. It is not a console: nobody flagged it.
func (r *realRig) conductorSession(name string) *consoleSession {
	r.t.Helper()
	owner := uuid.New()
	_, err := r.database.Global().Exec(r.ctx,
		`INSERT INTO global_users (id, email, password_hash) VALUES ($1, $2, 'unused')`, owner, name+"@example.invalid")
	require.NoError(r.t, err)
	_, err = r.database.Global().Exec(r.ctx,
		`INSERT INTO global_tenant_memberships (id, tenant, user_id, role) VALUES (gen_random_uuid(), $1, $2, 'member')`, r.tenantID, owner)
	require.NoError(r.t, err)
	id := uuid.New()
	_, err = r.database.Exec(r.ctx,
		`INSERT INTO agents (id, name, kind, owner_user_id) VALUES ($1, $2, 'conductor', $3)`, id, name, owner)
	require.NoError(r.t, err)
	return r.connectAs(name, id)
}

// A conductor makes the same proposals a console does, and they wait for a
// person like any other.
func TestConductor_ProposesLikeAConsoleWithoutBeingOne(t *testing.T) {
	r := newRealRig(t)
	tk := r.forceStatus(ticket.StatusPreparing)
	c := r.conductorSession("conductor")

	for _, name := range []string{"propose_ready", "list_proposals"} {
		res := c.call(t, name, consoleToolCalls(tk.ID)[name])
		assert.False(t, res.IsError, "%s: %s", name, resultText(t, res))
	}
	got, err := r.pending.List(r.ctx, &pendingaction.ListFilter{AgentID: &c.agentID})
	require.NoError(t, err)
	require.NotEmpty(t, got)
	for _, p := range got {
		assert.Equal(t, pendingaction.StatePending, p.State)
	}
	after, err := r.tickets.Get(r.ctx, tk.ID)
	require.NoError(t, err)
	assert.Equal(t, ticket.StatusPreparing, after.Status, "a proposal changes nothing until a person approves it")
}

// Closing a ticket as done is the owner's call: a conductor can only ask, and
// auto-approve does not reach it.
func TestConductor_ProposesCloseMergedButNothingCloses(t *testing.T) {
	r := newRealRig(t)
	tk := r.forceStatus(ticket.StatusOpen)
	c := r.conductorSession("conductor")

	res := c.call(t, "propose_close_merged", consoleToolCalls(tk.ID)["propose_close_merged"])

	assert.False(t, res.IsError, resultText(t, res))
	got, err := r.pending.List(r.ctx, &pendingaction.ListFilter{AgentID: &c.agentID})
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, pendingaction.KindCloseMerged, got[0].Kind)
	assert.Equal(t, pendingaction.StatePending, got[0].State)
	after, err := r.tickets.Get(r.ctx, tk.ID)
	require.NoError(t, err)
	assert.Equal(t, ticket.StatusOpen, after.Status)
}

func TestConductor_IsNeverHandedATicket(t *testing.T) {
	r := newRealRig(t)
	r.openTicket("work for a worker")
	c := r.conductorSession("conductor")

	res := c.call(t, "ticket_next", map[string]any{})

	assert.NotContains(t, resultText(t, res), "Granted")
	var held int
	require.NoError(t, r.database.QueryRow(r.ctx, `SELECT count(*) FROM leases WHERE agent_id = $1`, c.agentID).Scan(&held))
	assert.Zero(t, held)

	// The same call from a worker is granted it, so the words above mean
	// something.
	worker := r.agentSession("worker", false)
	assert.Contains(t, resultText(t, worker.call(t, "ticket_next", map[string]any{})), "Granted")
}

func TestConductor_DisabledProposesNothing(t *testing.T) {
	r := newRealRig(t)
	tk := r.forceStatus(ticket.StatusPreparing)
	c := r.conductorSession("conductor")
	_, err := r.database.Exec(r.ctx, `UPDATE agents SET disabled_at = now() WHERE id = $1`, c.agentID)
	require.NoError(t, err)

	res := c.call(t, "propose_ready", map[string]any{"ticket_id": tk.ID})

	assert.True(t, res.IsError)
	assert.Zero(t, r.proposalCount())
}

type membership struct{ store *user.PostgresStore }

func (m membership) IsTenantOwner(ctx context.Context, userID, tenantID uuid.UUID) (bool, error) {
	return m.store.IsOwner(ctx, userID, tenantID)
}

func (m membership) IsTenantMember(ctx context.Context, userID, tenantID uuid.UUID) (bool, error) {
	return m.store.IsMember(ctx, userID, tenantID)
}

type labels struct{ store *user.PostgresStore }

func (l labels) DisplayName(ctx context.Context, id uuid.UUID) (string, error) {
	u, err := l.store.FindByID(ctx, id)
	if err != nil {
		return "", err
	}
	return u.Label(), nil
}

// ownerBound installs the real owner resolver, as the server wires it.
func (r *realRig) ownerBound() {
	r.t.Helper()
	store := user.NewPostgresStore(r.database)
	generous := &ratelimit.Config{Limit: 1000, Window: time.Minute, MaxKeys: 1000}
	grants := oauth.NewService(oauth.NewPostgresStore(r.database), r.agents, &oauth.Config{
		Issuer: "http://x", RegisterLimit: generous, DeviceLookupLimit: generous, DeviceStartLimit: generous,
	}, nil, slog.DiscardHandler)
	conductors := agent.NewConductorService(r.agents, membership{store}, slog.DiscardHandler)
	r.owners.resolver = agent.NewOwnerResolver(r.agents, conductors, membership{store}, grants, labels{store})
}

// A conductor's direct power over another agent's ticket is its blockers and
// nothing else: the tools that edit, rescope or finish a ticket still need the
// ticket to be its own, which a conductor never is.
func TestConductor_BlockersDirectlyOnAnotherAgentsTicketAndNothingElse(t *testing.T) {
	r := newRealRig(t)
	r.ownerBound()
	held := r.openTicket("Held by the builder")
	claimed := r.call("ticket_next", map[string]any{"ticket_id": held.ID})
	require.False(t, claimed.IsError, resultText(t, claimed))
	blocker := r.openTicket("Blocker")
	c := r.conductorSession("conductor")

	res := c.call(t, "ticket_blocker_add", map[string]any{"ticket_id": held.ID, "blocker_id": blocker.ID})
	require.False(t, res.IsError, resultText(t, res))
	assert.Contains(t, resultText(t, res), "now waits on")
	res = c.call(t, "ticket_blocker_remove", map[string]any{"ticket_id": held.ID, "blocker_id": blocker.ID})
	require.False(t, res.IsError, resultText(t, res))

	for name, args := range map[string]map[string]any{
		"ticket_edit":         {"title": "Hijacked", "revision": 1},
		"ticket_scope_extend": {"paths": []string{"x/"}},
		"ticket_scope_narrow": {"remove": []string{"x/"}},
		"ticket_finish":       {"handoff": "x"},
		"ticket_release":      {"reason": "x"},
		"integration_lock":    {},
	} {
		res := c.call(t, name, args)
		assert.True(t, res.IsError, "%s must stay refused for a session that holds nothing: %s", name, resultText(t, res))
	}
	after, err := r.tickets.Get(r.ctx, held.ID)
	require.NoError(t, err)
	assert.Equal(t, "Held by the builder", after.Title)
	assert.Equal(t, ticket.StatusInProgress, after.Status)
}

// A worker over MCP is unchanged by it.
func TestConductor_AWorkerStillCannotTouchAnotherAgentsTicket(t *testing.T) {
	r := newRealRig(t)
	r.ownerBound()
	held := r.openTicket("Held by the builder")
	require.False(t, r.call("ticket_next", map[string]any{"ticket_id": held.ID}).IsError)
	blocker := r.openTicket("Blocker")
	w := r.agentSession("other-worker", false)

	res := w.call(t, "ticket_blocker_add", map[string]any{"ticket_id": held.ID, "blocker_id": blocker.ID})

	assert.True(t, res.IsError)
	assert.Contains(t, resultText(t, res), "comment on it")
}
