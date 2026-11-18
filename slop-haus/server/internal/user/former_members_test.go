package user_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/user"
)

// formerFixture has ada and bob each in their own tenant, and bob also in
// ada's, so removing bob from it is the case under test.
type formerFixture struct {
	*fixture
	ada, bob *authn.User
}

func newFormerFixture(t *testing.T) *formerFixture {
	t.Helper()
	f := newFixture(t)
	f.activeUser(t, "ada@example.com")
	f.activeUser(t, "bob@example.com")
	ff := &formerFixture{fixture: f, ada: f.signIn(t, "ada@example.com"), bob: f.signIn(t, "bob@example.com")}
	ff.exec(t, `UPDATE global_users SET display_name = 'Ada' WHERE id = $1`, ff.ada.ID)
	ff.exec(t, `UPDATE global_users SET display_name = 'Bob' WHERE id = $1`, ff.bob.ID)
	ff.join(t, ff.ada.TenantID, ff.bob.ID)
	return ff
}

func (f *formerFixture) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	_, err := f.db.Global().Exec(f.ctx, sql, args...)
	require.NoError(t, err)
}

func (f *formerFixture) join(t *testing.T, tenant, userID uuid.UUID) {
	t.Helper()
	f.exec(t, `INSERT INTO global_tenant_memberships (id, tenant, user_id) VALUES ($1, $2, $3)`, uuid.New(), tenant, userID)
}

func (f *formerFixture) remove(t *testing.T, tenant, userID uuid.UUID) {
	t.Helper()
	f.exec(t, `DELETE FROM global_tenant_memberships WHERE tenant = $1 AND user_id = $2`, tenant, userID)
}

func (f *formerFixture) formerNames(t *testing.T, who *authn.User) []string {
	t.Helper()
	dir, err := f.svc.MemberDirectory(f.ctx, who)
	require.NoError(t, err)
	names := []string{}
	for _, m := range dir.Former {
		names = append(names, m.DisplayName)
	}
	return names
}

func (f *formerFixture) recorded(t *testing.T) int {
	t.Helper()
	return f.count(t, `SELECT count(*) FROM global_former_memberships`)
}

func TestRemovingAMemberRecordsThemAsFormer(t *testing.T) {
	f := newFormerFixture(t)
	assert.Empty(t, f.formerNames(t, f.ada), "bob is a member, not a former one")

	f.remove(t, f.ada.TenantID, f.bob.ID)

	assert.Equal(t, []string{"Bob"}, f.formerNames(t, f.ada))
	dir, err := f.svc.MemberDirectory(f.ctx, f.ada)
	require.NoError(t, err)
	require.Len(t, dir.Members, 1)
	assert.Equal(t, f.ada.ID, dir.Members[0].ID, "bob is no longer listed as a member")
}

func TestAFormerMemberIsInvisibleToOtherTenants(t *testing.T) {
	f := newFormerFixture(t)
	f.remove(t, f.ada.TenantID, f.bob.ID)
	require.Equal(t, []string{"Bob"}, f.formerNames(t, f.ada))

	assert.Empty(t, f.formerNames(t, f.bob), "bob's own tenant has no former members")

	f.activeUser(t, "cy@example.com")
	cy := f.signIn(t, "cy@example.com")
	assert.Empty(t, f.formerNames(t, cy), "a third tenant never sees tenant A's leavers")
	dir, err := f.svc.MemberDirectory(f.ctx, cy)
	require.NoError(t, err)
	for _, m := range dir.Members {
		assert.NotEqual(t, f.bob.ID, m.ID)
	}
}

func TestRejoiningClearsTheFormerRecord(t *testing.T) {
	f := newFormerFixture(t)
	f.remove(t, f.ada.TenantID, f.bob.ID)
	require.Equal(t, 1, f.recorded(t))

	f.join(t, f.ada.TenantID, f.bob.ID)

	assert.Zero(t, f.recorded(t))
	assert.Empty(t, f.formerNames(t, f.ada))
}

func TestLeavingTwiceKeepsOneRecord(t *testing.T) {
	f := newFormerFixture(t)
	f.remove(t, f.ada.TenantID, f.bob.ID)
	f.join(t, f.ada.TenantID, f.bob.ID)
	f.remove(t, f.ada.TenantID, f.bob.ID)
	assert.Equal(t, 1, f.recorded(t))
}

func TestDeletingAUserRecordsNoFormerMembership(t *testing.T) {
	f := newFormerFixture(t)
	f.exec(t, `DELETE FROM global_users WHERE id = $1`, f.bob.ID)

	assert.Zero(t, f.recorded(t), "the cascade from the user is not a member leaving")
	assert.Empty(t, f.formerNames(t, f.ada))
}

func TestDeletingATenantRecordsNoFormerMembership(t *testing.T) {
	f := newFormerFixture(t)
	f.exec(t, `DELETE FROM global_tenants WHERE id = $1`, f.ada.TenantID)

	assert.Zero(t, f.recorded(t), "the cascade from the tenant is not a member leaving")
}

func TestDeletingAFormerMembersAccountRemovesTheRecord(t *testing.T) {
	f := newFormerFixture(t)
	f.remove(t, f.ada.TenantID, f.bob.ID)
	f.exec(t, `DELETE FROM global_users WHERE id = $1`, f.bob.ID)
	assert.Zero(t, f.recorded(t))
}

func TestFormerMembersAreOrderedByName(t *testing.T) {
	f := newFormerFixture(t)
	f.activeUser(t, "cy@example.com")
	f.activeUser(t, "di@example.com")
	cy, di := f.signIn(t, "cy@example.com"), f.signIn(t, "di@example.com")
	f.exec(t, `UPDATE global_users SET display_name = 'Zed' WHERE id = $1`, cy.ID)
	f.exec(t, `UPDATE global_users SET display_name = 'Amy' WHERE id = $1`, di.ID)
	for _, u := range []*authn.User{cy, di} {
		f.join(t, f.ada.TenantID, u.ID)
		f.remove(t, f.ada.TenantID, u.ID)
	}
	f.remove(t, f.ada.TenantID, f.bob.ID)

	assert.Equal(t, []string{"Amy", "Bob", "Zed"}, f.formerNames(t, f.ada))
}

func TestARemovedMemberCannotReadTheDirectoryWithAStaleSession(t *testing.T) {
	f := newFormerFixture(t)
	f.remove(t, f.ada.TenantID, f.bob.ID)

	_, err := f.svc.MemberDirectory(f.ctx, &authn.User{ID: f.bob.ID, TenantID: f.ada.TenantID})
	require.ErrorIs(t, err, user.ErrNotMember)
}
