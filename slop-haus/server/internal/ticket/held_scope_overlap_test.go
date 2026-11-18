package ticket_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

func (f *fixture) heldOverlaps(t *testing.T, changed ...string) []ticket.HeldScopeOverlap {
	t.Helper()
	overlaps, err := f.svc.HeldScopeOverlaps(f.ctx, changed)
	require.NoError(t, err)
	return overlaps
}

func TestHeldScopeOverlaps_ChangedPathsInsideAScopeAnAgentHoldsAreListed(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	held := f.ticket(t, "Held", "app/src/", "docs/plan.md")
	f.claim(t, f.agent(t, "worker"))

	got := f.heldOverlaps(t, "app/src/panel/a.go", "docs/plan.md", "elsewhere/x.go")

	assert.Equal(t, []ticket.HeldScopeOverlap{
		{TicketID: held.ID, Title: "Held", Status: ticket.StatusInProgress, Path: "app/src/panel/a.go", Held: "app/src/"},
		{TicketID: held.ID, Title: "Held", Status: ticket.StatusInProgress, Path: "docs/plan.md", Held: "docs/plan.md"},
	}, got)
}

func TestHeldScopeOverlaps_PrefixLookalikesAndUnheldScopesAreNotListed(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.ticket(t, "Held", "app/core/")
	f.claim(t, f.agent(t, "worker"))
	f.ticket(t, "Waiting, held by nobody", "app/waiting/")

	assert.Empty(t, f.heldOverlaps(t, "app/coreutil/x.go", "app/waiting/x.go", "app/core"),
		"a file named like the directory, a lookalike and a scope no one holds do not conflict")
}

func TestHeldScopeOverlaps_ATicketInReviewStillHoldsItsScope(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	agentID := f.agent(t, "worker")
	f.ticket(t, "In review", "app/")
	f.claim(t, agentID)
	_, err := f.svc.Review(f.ctx, agentID, &ticket.ReviewParams{Reason: "reviewed by a person", PRURL: "https://github.com/acme/widgets/pull/12", Handoff: "PR open."})
	require.NoError(t, err)

	got := f.heldOverlaps(t, "app/x.go")

	require.Len(t, got, 1)
	assert.Equal(t, ticket.StatusReview, got[0].Status)
}

func TestHeldScopeOverlaps_AFinishedTicketHoldsNothing(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	finished := f.ticket(t, "Finished", "app/")
	f.forceStatus(t, finished.ID, ticket.StatusDone)

	assert.Empty(t, f.heldOverlaps(t, "app/x.go"))
}

func TestHeldScopeOverlaps_AnExpiredOrReleasedLeaseHoldsNothing(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.ticket(t, "Expired", "app/expired/")
	expiredAgent := f.agent(t, "expired-worker")
	f.claim(t, expiredAgent)
	f.ticket(t, "Released", "app/released/")
	releasedAgent := f.agent(t, "released-worker")
	f.claim(t, releasedAgent)
	f.ticket(t, "Still held", "app/held/")
	heldAgent := f.agent(t, "held-worker")
	f.claim(t, heldAgent)

	f.expire(t, expiredAgent)
	_, err := f.database.Exec(f.ctx, `UPDATE leases SET released_at = now(), release_reason = 'test' WHERE agent_id = $1`, releasedAgent)
	require.NoError(t, err)

	got := f.heldOverlaps(t, "app/expired/a.go", "app/released/b.go", "app/held/c.go")
	require.Len(t, got, 1, "only the lease that is still live")
	assert.Equal(t, "app/held/c.go", got[0].Path)
}

func TestHeldScopeOverlaps_NothingChangedIsNoOverlap(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.ticket(t, "Held", "app/")
	f.claim(t, f.agent(t, "worker"))
	assert.Empty(t, f.heldOverlaps(t))
}

func TestHeldScopeOverlaps_AnotherTenantsScopesAreNeverListed(t *testing.T) {
	t.Parallel()
	a, b := newTwoTenants(t)
	b.ticket(t, "B's", "app/")
	_, err := b.svc.Claim(b.ctx, b.agentID, nil)
	require.NoError(t, err)

	got, err := a.svc.HeldScopeOverlaps(a.ctx, []string{"app/x.go"})
	require.NoError(t, err)
	assert.Empty(t, got)
}
