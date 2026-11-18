package main

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/oauth"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/user"
)

type noOwners struct{}

func (noOwners) ResolveOwner(context.Context, uuid.UUID) (*ticket.OwnerBound, error) { return nil, nil }

type noGate struct{}

func (noGate) MayChangeConfig(context.Context, *authn.User, uuid.UUID) (bool, error) {
	return false, nil
}

func TestLateDeps_VerifyNamesWhatIsUnbound(t *testing.T) {
	t.Parallel()
	bindExcept := func(skip string) *lateDeps {
		l := newLateDeps()
		if skip != "oauth service" {
			l.oauth.Bind(&oauth.Service{})
		}
		if skip != "owner resolver" {
			l.owners.Bind(noOwners{})
		}
		if skip != "harness override gate" {
			l.gate.cell.Bind(noGate{})
		}
		return l
	}
	require.NoError(t, bindExcept("").verify())

	for _, name := range []string{"oauth service", "owner resolver", "harness override gate"} {
		t.Run(name, func(t *testing.T) {
			err := bindExcept(name).verify()
			require.Error(t, err)
			assert.ErrorContains(t, err, name)
			for _, other := range []string{"oauth service", "owner resolver", "harness override gate"} {
				if other != name {
					assert.NotContains(t, err.Error(), other)
				}
			}
		})
	}
}

func TestLate_UnboundGetPanicsAndRebindPanics(t *testing.T) {
	t.Parallel()
	var cells lateCells
	l := newLate[int](&cells, "n")
	assert.Panics(t, func() { l.Get() })
	l.Bind(1)
	assert.Equal(t, 1, l.Get())
	assert.Panics(t, func() { l.Bind(2) })
	assert.NoError(t, cells.verifyAll())
}

type fakeGrants struct {
	oauthGrants
	has   bool
	err   error
	asked [2]uuid.UUID
}

func (f *fakeGrants) HasFullAccessGrant(_ context.Context, tenantID, agentID uuid.UUID) (bool, error) {
	f.asked = [2]uuid.UUID{tenantID, agentID}
	return f.has, f.err
}

func TestApprovalGrants_Delegates(t *testing.T) {
	t.Parallel()
	fake := &fakeGrants{has: true, err: errors.New("boom")}
	var cells lateCells
	cell := newLate[oauthGrants](&cells, "g")
	cell.Bind(fake)
	g := &approvalGrants{oauth: cell}
	tenantID, agentID := uuid.New(), uuid.New()

	has, err := g.HasFullAccessGrant(context.Background(), tenantID, agentID)

	assert.True(t, has)
	assert.EqualError(t, err, "boom")
	assert.Equal(t, [2]uuid.UUID{tenantID, agentID}, fake.asked)
}

type fakeOwners struct {
	bound *ticket.OwnerBound
	err   error
	asked uuid.UUID
}

func (f *fakeOwners) ResolveOwner(_ context.Context, agentID uuid.UUID) (*ticket.OwnerBound, error) {
	f.asked = agentID
	return f.bound, f.err
}

func TestOwnerResolver_Delegates(t *testing.T) {
	t.Parallel()
	fake := &fakeOwners{bound: &ticket.OwnerBound{}, err: ticket.ErrOwnerBoundRefused}
	var cells lateCells
	cell := newLate[ticket.OwnerResolver](&cells, "o")
	cell.Bind(fake)
	o := ownerResolver{resolver: cell}
	agentID := uuid.New()

	bound, err := o.ResolveOwner(context.Background(), agentID)

	assert.Same(t, fake.bound, bound)
	assert.ErrorIs(t, err, ticket.ErrOwnerBoundRefused)
	assert.Equal(t, agentID, fake.asked)
}

type recordingOAuth struct {
	oauthGrants
	revoked [2]uuid.UUID
}

func (r *recordingOAuth) RevokeAgentGrants(_ context.Context, tenantID, agentID uuid.UUID) error {
	r.revoked = [2]uuid.UUID{tenantID, agentID}
	return nil
}

func TestAgentGrants_ForwardToTheBoundOAuthService(t *testing.T) {
	t.Parallel()
	var cells lateCells
	cell := newLate[oauthGrants](&cells, "o")
	g := &agentGrants{oauth: cell}
	assert.Panics(t, func() { _ = g.RevokeAgentGrants(context.Background(), uuid.New(), uuid.New()) })
	rec := &recordingOAuth{}
	cell.Bind(rec)
	tenantID, agentID := uuid.New(), uuid.New()

	require.NoError(t, g.RevokeAgentGrants(context.Background(), tenantID, agentID))

	assert.Equal(t, [2]uuid.UUID{tenantID, agentID}, rec.revoked)
}

type recordingHook struct{ got []*ticket.Approval }

func (h *recordingHook) Approved(_ context.Context, a *ticket.Approval) error {
	h.got = append(h.got, a)
	return nil
}

type recordingTenantAgents struct{ got []uuid.UUID }

func (r *recordingTenantAgents) DisableTenantAgents(_ context.Context, tenantID uuid.UUID) error {
	r.got = append(r.got, tenantID)
	return nil
}

type recordingGate struct{ asked []uuid.UUID }

func (g *recordingGate) MayChangeConfig(_ context.Context, _ *authn.User, agentID uuid.UUID) (bool, error) {
	g.asked = append(g.asked, agentID)
	return true, nil
}

func TestLateAdapters_ForwardToWhatWasBound(t *testing.T) {
	t.Parallel()
	var cells lateCells
	ctx := context.Background()

	t.Run("approval hook", func(t *testing.T) {
		cell := newLate[ticket.ApprovalHook](&cells, "hook")
		adapter := lateApprovalHook{cell}
		a := &ticket.Approval{TicketID: 7}
		assert.Panics(t, func() { _ = adapter.Approved(ctx, a) }, "unbound is a wiring bug, not a silent no-op")
		hook := &recordingHook{}
		cell.Bind(hook)
		require.NoError(t, adapter.Approved(ctx, a))
		assert.Equal(t, []*ticket.Approval{a}, hook.got)
	})
	t.Run("tenant agents", func(t *testing.T) {
		cell := newLate[user.TenantAgents](&cells, "agents")
		agents := &recordingTenantAgents{}
		cell.Bind(agents)
		tenant := uuid.New()
		require.NoError(t, lateTenantAgents{cell}.DisableTenantAgents(ctx, tenant))
		assert.Equal(t, []uuid.UUID{tenant}, agents.got)
	})
	t.Run("override gate", func(t *testing.T) {
		gate := &recordingGate{}
		l := newLateDeps()
		l.gate.cell.Bind(gate)
		id := uuid.New()
		ok, err := l.gate.MayChangeConfig(ctx, nil, id)
		require.NoError(t, err)
		assert.True(t, ok)
		assert.Equal(t, []uuid.UUID{id}, gate.asked)
	})
}
