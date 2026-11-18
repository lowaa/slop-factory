package user_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/user"
)

// ownerWithTwoTenants is a signed-in owner of their first tenant and of a
// second, "Second", which they are not in the session of.
func ownerWithTwoTenants(t *testing.T, f *fixture) (a *authn.User, second uuid.UUID) {
	t.Helper()
	f.activeUser(t, "a@example.com")
	a = f.signIn(t, "a@example.com")
	session, err := f.svc.CreateTenant(f.ctx, a, "Second")
	require.NoError(t, err)
	return a, session.Profile.TenantID
}

func inTenant(who *authn.User, tenantID uuid.UUID) *authn.User {
	moved := *who
	moved.TenantID = tenantID
	return &moved
}

func archivedAt(t *testing.T, f *fixture, tenantID uuid.UUID) (at *string, by *uuid.UUID) {
	t.Helper()
	var s *string
	require.NoError(t, f.db.Global().QueryRow(f.ctx,
		`SELECT archived_at::text, archived_by FROM global_tenants WHERE id = $1`, tenantID).Scan(&s, &by))
	return s, by
}

func activeTenantIDs(t *testing.T, f *fixture, who *authn.User) []uuid.UUID {
	t.Helper()
	return tenantIDs(t, f, who)
}

func TestArchivingSwitchesOffEveryAgentReleasesWhatTheyHoldAndKeepsEverything(t *testing.T) {
	f := newFixture(t)
	a, second := ownerWithTwoTenants(t, f)
	ctx := db.WithTenant(f.ctx, second)

	worker, err := f.agents.Create(ctx, "worker")
	require.NoError(t, err)
	console, err := f.agents.CreateKind(ctx, "console", "console")
	require.NoError(t, err)
	tk, err := f.tickets.Create(ctx, &ticket.CreateParams{Title: "t", Status: ticket.StatusOpen})
	require.NoError(t, err)
	_, err = f.tickets.Claim(ctx, worker.ID, &tk.ID)
	require.NoError(t, err)
	require.NoError(t, f.tickets.AcquireIntegrationLock(ctx, worker.ID))
	// A pending invite into the tenant, and the first tenant's agent.
	_, err = f.svc.InviteToTenant(f.ctx, inTenant(a, second), "b@example.com")
	require.NoError(t, err)
	firstTenantAgent, err := f.agents.Create(db.WithTenant(f.ctx, a.TenantID), "stays")
	require.NoError(t, err)
	before := map[string]int{
		"tenants":     f.count(t, `SELECT count(*) FROM global_tenants`),
		"memberships": f.count(t, `SELECT count(*) FROM global_tenant_memberships`),
		"agents":      f.count(t, `SELECT count(*) FROM agents`),
		"tickets":     f.count(t, `SELECT count(*) FROM tickets`),
	}

	require.NoError(t, f.svc.ArchiveTenant(f.ctx, a, second))

	at, by := archivedAt(t, f, second)
	require.NotNil(t, at)
	require.NotNil(t, by)
	assert.Equal(t, a.ID, *by)
	for _, id := range []uuid.UUID{worker.ID, console.ID} {
		got, err := f.agents.Get(ctx, id)
		require.NoError(t, err)
		assert.NotNil(t, got.DisabledAt)
	}
	_, err = f.tickets.LeaseFor(ctx, worker.ID)
	assert.ErrorIs(t, err, ticket.ErrNoLease, "the lease is released")
	state, err := f.tickets.IntegrationLockState(ctx)
	require.NoError(t, err)
	assert.Nil(t, state.Holder, "the integration lock is freed")
	assert.Zero(t, f.count(t, `SELECT count(*) FROM global_invites WHERE tenant = $1 AND revoked_at IS NULL`, second), "invites revoked")
	other, err := f.agents.Get(db.WithTenant(f.ctx, a.TenantID), firstTenantAgent.ID)
	require.NoError(t, err)
	assert.Nil(t, other.DisabledAt, "another tenant's agent")
	assert.Equal(t, 1, f.events.Count(audit.TenantArchived))
	for table, n := range before {
		assert.Equal(t, n, f.count(t, `SELECT count(*) FROM `+map[string]string{
			"tenants": "global_tenants", "memberships": "global_tenant_memberships", "agents": "agents", "tickets": "tickets"}[table]), "%s unchanged", table)
	}
}

func TestOnlyAnOwnerMayArchiveOrUnarchive(t *testing.T) {
	f := newFixture(t)
	a, second := ownerWithTwoTenants(t, f)
	f.activeUser(t, "b@example.com")
	b := f.signIn(t, "b@example.com")
	_, err := f.svc.InviteToTenant(f.ctx, inTenant(a, second), "b@example.com")
	require.NoError(t, err)
	_, err = f.svc.AcceptInvite(f.ctx, b, tokenFrom(t, f.mail.last(t, "b@example.com")), "")
	require.NoError(t, err)
	stranger := f.signIn(t, "b@example.com")

	require.ErrorIs(t, f.svc.ArchiveTenant(f.ctx, stranger, second), user.ErrNotOwner, "a member")
	require.ErrorIs(t, f.svc.ArchiveTenant(f.ctx, stranger, uuid.New()), user.ErrNotOwner, "no such tenant")
	at, _ := archivedAt(t, f, second)
	assert.Nil(t, at)

	require.NoError(t, f.svc.ArchiveTenant(f.ctx, a, second))
	require.ErrorIs(t, f.svc.UnarchiveTenant(f.ctx, stranger, second), user.ErrNotOwner)
	at, _ = archivedAt(t, f, second)
	assert.NotNil(t, at)
	assert.Equal(t, 3, f.events.Count(audit.TenantArchiveRefused), "each refusal recorded")
}

func TestTheLastActiveTenantCannotBeArchived(t *testing.T) {
	f := newFixture(t)
	a, second := ownerWithTwoTenants(t, f)

	require.NoError(t, f.svc.ArchiveTenant(f.ctx, a, second))
	err := f.svc.ArchiveTenant(f.ctx, a, a.TenantID)

	require.ErrorIs(t, err, user.ErrLastActiveTenant, "an archived one doesn't count as somewhere to go")
	at, _ := archivedAt(t, f, a.TenantID)
	assert.Nil(t, at)
}

func TestArchivingTwiceChangesAndRecordsNothingTheSecondTime(t *testing.T) {
	f := newFixture(t)
	a, second := ownerWithTwoTenants(t, f)

	require.NoError(t, f.svc.ArchiveTenant(f.ctx, a, second))
	first, _ := archivedAt(t, f, second)
	require.NoError(t, f.svc.ArchiveTenant(f.ctx, a, second))
	again, _ := archivedAt(t, f, second)

	assert.Equal(t, first, again)
	assert.Equal(t, 1, f.events.Count(audit.TenantArchived))
}

// Archiving is archive-then-disable, so a failed disable must leave the way
// to finish: archiving again runs it again.
type failingOnceAgents struct {
	calls []uuid.UUID
	fail  bool
}

func (a *failingOnceAgents) DisableTenantAgents(_ context.Context, tenantID uuid.UUID) error {
	a.calls = append(a.calls, tenantID)
	if a.fail {
		a.fail = false
		return errors.New("boom")
	}
	return nil
}

func TestAFailedDisableLeavesTheTenantArchivedAndArchivingAgainRetriesIt(t *testing.T) {
	agents := &failingOnceAgents{fail: true}
	f := newFixtureWith(t, &fixtureOpts{agents: agents})
	a, second := ownerWithTwoTenants(t, f)

	require.Error(t, f.svc.ArchiveTenant(f.ctx, a, second))
	at, _ := archivedAt(t, f, second)
	assert.NotNil(t, at)
	require.NoError(t, f.svc.ArchiveTenant(f.ctx, a, second))

	assert.Equal(t, []uuid.UUID{second, second}, agents.calls)
}

func TestArchivingWithoutAWayToDisableAgentsIsRefusedBeforeAnythingChanges(t *testing.T) {
	f := newFixtureWith(t, &fixtureOpts{noAgents: true})
	a, second := ownerWithTwoTenants(t, f)

	require.ErrorIs(t, f.svc.ArchiveTenant(f.ctx, a, second), user.ErrAgentsUnavailable)
	at, _ := archivedAt(t, f, second)
	assert.Nil(t, at)
}

func TestTheOwnerCapCountsOnlyActiveTenants(t *testing.T) {
	f := newFixture(t, func(c *user.Config) { c.TenantOwnerLimit = 2; c.TenantOwnerTotalLimit = 10 })
	a, second := ownerWithTwoTenants(t, f)
	_, err := f.svc.CreateTenant(f.ctx, a, "Third")
	require.ErrorIs(t, err, user.ErrTenantLimit, "two active is the cap")

	require.NoError(t, f.svc.ArchiveTenant(f.ctx, a, second))
	_, err = f.svc.CreateTenant(f.ctx, a, "Third")

	require.NoError(t, err, "archiving made room")
}

func TestTheTotalOwnedCapCountsArchivedTenantsToo(t *testing.T) {
	f := newFixture(t, func(c *user.Config) { c.TenantOwnerLimit = 2; c.TenantOwnerTotalLimit = 3 })
	a, second := ownerWithTwoTenants(t, f)
	require.NoError(t, f.svc.ArchiveTenant(f.ctx, a, second))
	third, err := f.svc.CreateTenant(f.ctx, a, "Third")
	require.NoError(t, err, "the third owned, two active")
	require.NoError(t, f.svc.ArchiveTenant(f.ctx, a, third.Profile.TenantID))

	_, err = f.svc.CreateTenant(f.ctx, a, "Fourth")

	require.ErrorIs(t, err, user.ErrTenantLimit, "three owned in all, though one is active")
}

func TestUnarchivingOverTheActiveCapIsRefused(t *testing.T) {
	f := newFixture(t, func(c *user.Config) { c.TenantOwnerLimit = 2; c.TenantOwnerTotalLimit = 10 })
	a, second := ownerWithTwoTenants(t, f)
	require.NoError(t, f.svc.ArchiveTenant(f.ctx, a, second))
	_, err := f.svc.CreateTenant(f.ctx, a, "Third")
	require.NoError(t, err)

	err = f.svc.UnarchiveTenant(f.ctx, a, second)

	require.ErrorIs(t, err, user.ErrUnarchiveOverLimit)
	at, _ := archivedAt(t, f, second)
	assert.NotNil(t, at)
	assert.Zero(t, f.events.Count(audit.TenantUnarchived))
	assert.Equal(t, 1, f.events.Count(audit.TenantArchiveRefused))
}

func TestUnarchivingRestoresTheTenantAndLeavesItsAgentsDisabled(t *testing.T) {
	f := newFixture(t)
	a, second := ownerWithTwoTenants(t, f)
	ctx := db.WithTenant(f.ctx, second)
	worker, err := f.agents.Create(ctx, "worker")
	require.NoError(t, err)
	require.NoError(t, f.svc.ArchiveTenant(f.ctx, a, second))

	require.NoError(t, f.svc.UnarchiveTenant(f.ctx, a, second))

	at, by := archivedAt(t, f, second)
	assert.Nil(t, at)
	assert.Nil(t, by)
	got, err := f.agents.Get(ctx, worker.ID)
	require.NoError(t, err)
	assert.NotNil(t, got.DisabledAt, "the owner turns agents back on themselves")
	assert.Contains(t, activeTenantIDs(t, f, a), second)
	assert.Equal(t, 1, f.events.Count(audit.TenantUnarchived))

	require.NoError(t, f.svc.UnarchiveTenant(f.ctx, a, second), "not archived: nothing to do")
	assert.Equal(t, 1, f.events.Count(audit.TenantUnarchived))
}

func TestListingLeavesOutArchivedTenantsUnlessAnOwnerAsks(t *testing.T) {
	f := newFixture(t)
	a, second := ownerWithTwoTenants(t, f)
	f.activeUser(t, "b@example.com")
	b := f.signIn(t, "b@example.com")
	_, err := f.svc.InviteToTenant(f.ctx, inTenant(a, second), "b@example.com")
	require.NoError(t, err)
	_, err = f.svc.AcceptInvite(f.ctx, b, tokenFrom(t, f.mail.last(t, "b@example.com")), "")
	require.NoError(t, err)
	require.NoError(t, f.svc.ArchiveTenant(f.ctx, a, second))

	assert.NotContains(t, tenantIDs(t, f, a), second)
	all, err := f.svc.ListTenants(f.ctx, a, true)
	require.NoError(t, err)
	var found *user.TenantSummary
	for i := range all {
		if all[i].ID == second {
			found = &all[i]
		}
	}
	require.NotNil(t, found)
	assert.NotNil(t, found.ArchivedAt)
	assert.True(t, found.IsOwner)

	member, err := f.svc.ListTenants(f.ctx, b, true)
	require.NoError(t, err)
	for _, tn := range member {
		assert.NotEqual(t, second, tn.ID, "a member never sees it, even asking for archived")
	}
}

func TestSignInAndOtherActiveTenantNeverPickAnArchivedTenant(t *testing.T) {
	f := newFixture(t)
	a, second := ownerWithTwoTenants(t, f)
	require.NoError(t, f.svc.ArchiveTenant(f.ctx, a, a.TenantID), "the oldest tenant")

	session, err := f.svc.Login(f.ctx, "a@example.com", password)
	require.NoError(t, err)
	assert.Equal(t, second, session.Profile.TenantID)

	other, err := f.svc.OtherActiveTenant(f.ctx, a, second)
	assert.ErrorIs(t, err, user.ErrNotFound, "the only other one is archived")
	assert.Equal(t, uuid.Nil, other)
	other, err = f.svc.OtherActiveTenant(f.ctx, a, a.TenantID)
	require.NoError(t, err)
	assert.Equal(t, second, other)
}

func TestAnInviteIntoAnArchivedTenantCannotBeAccepted(t *testing.T) {
	f := newFixture(t)
	a, second := ownerWithTwoTenants(t, f)
	_, err := f.svc.InviteToTenant(f.ctx, inTenant(a, second), "b@example.com")
	require.NoError(t, err)
	token := tokenFrom(t, f.mail.last(t, "b@example.com"))

	require.NoError(t, f.svc.ArchiveTenant(f.ctx, a, second))

	_, err = f.svc.PreviewInvite(f.ctx, token)
	assert.ErrorIs(t, err, user.ErrInvalidToken)
	_, err = f.svc.AcceptInviteAsNewAccount(f.ctx, token, "Bee", password, "")
	assert.ErrorIs(t, err, user.ErrInvalidToken)

	// Refusing new invites is a later part's; the guard here is that one made
	// anyway cannot be spent.
	_, err = f.svc.InviteToTenant(f.ctx, inTenant(a, second), "c@example.com")
	require.NoError(t, err)
	late := tokenFrom(t, f.mail.last(t, "c@example.com"))
	_, err = f.svc.PreviewInvite(f.ctx, late)
	assert.ErrorIs(t, err, user.ErrInvalidToken)
	_, err = f.svc.AcceptInviteAsNewAccount(f.ctx, late, "Cee", password, "")
	assert.ErrorIs(t, err, user.ErrInvalidToken)
}

func TestArchivingKeepsTheArchiverRecordedWhenTheirAccountIsDeleted(t *testing.T) {
	f := newFixture(t)
	a, second := ownerWithTwoTenants(t, f)
	require.NoError(t, f.svc.ArchiveTenant(f.ctx, a, second))

	_, err := f.db.Global().Exec(f.ctx, `DELETE FROM global_users WHERE id = $1`, a.ID)
	require.NoError(t, err)

	at, by := archivedAt(t, f, second)
	assert.NotNil(t, at, "still archived")
	assert.Nil(t, by)
	assert.Equal(t, 1, f.count(t, `SELECT count(*) FROM global_tenants WHERE id = $1 AND archived_by_deleted`, second))
}
