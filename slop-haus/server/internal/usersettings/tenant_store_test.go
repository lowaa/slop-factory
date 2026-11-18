package usersettings_test

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/usersettings"
)

const tenantSetting = "test.auto_approve"

type tenantFixture struct {
	t      *testing.T
	ctx    context.Context
	db     *db.DB
	owner  *db.DB
	svc    *usersettings.TenantService
	tenant uuid.UUID
}

func newTenantRegistry(t *testing.T) *usersettings.Registry {
	t.Helper()
	reg, err := usersettings.NewRegistry(
		usersettings.Definition{Name: tenantSetting, Scope: usersettings.ScopeTenant, Default: usersettings.BoolValue(false)},
		usersettings.Definition{Name: "test.label", Scope: usersettings.ScopeTenant, Default: usersettings.StrValue("none")},
		usersettings.Definition{Name: "test.global", Default: usersettings.BoolValue(false)},
	)
	require.NoError(t, err)
	return reg
}

func newTenantFixture(t *testing.T) *tenantFixture {
	t.Helper()
	app, owner := dbtest.WithOwnerDB(t)
	f := &tenantFixture{t: t, db: app, owner: owner}
	f.svc = usersettings.NewTenantService(usersettings.NewPostgresTenantStore(app), newTenantRegistry(t), slog.DiscardHandler)
	f.tenant = dbtest.NewTenant(t, app)
	f.ctx = db.WithTenant(context.Background(), f.tenant)
	return f
}

func (f *tenantFixture) newTenant() context.Context {
	f.t.Helper()
	return db.WithTenant(context.Background(), dbtest.NewTenant(f.t, f.db))
}

// join makes the user a member of the context's tenant, from the given time
// (the zero time meaning now).
func (f *tenantFixture) join(ctx context.Context, user uuid.UUID, since time.Time) {
	f.t.Helper()
	tenant, err := db.TenantFrom(ctx)
	require.NoError(f.t, err)
	if since.IsZero() {
		since = time.Now()
	}
	_, err = f.db.Global().Exec(ctx,
		`INSERT INTO global_tenant_memberships (id, tenant, user_id, role, created_at) VALUES ($1, $2, $3, 'member', $4)`,
		uuid.New(), tenant, user, since)
	require.NoError(f.t, err)
}

func (f *tenantFixture) member() uuid.UUID {
	f.t.Helper()
	u := dbtest.NewUser(f.t, f.db)
	f.join(f.ctx, u, time.Time{})
	return u
}

func (f *tenantFixture) setBool(ctx context.Context, user uuid.UUID, v bool) {
	f.t.Helper()
	_, err := f.svc.Set(ctx, user, tenantSetting, usersettings.BoolValue(v))
	require.NoError(f.t, err)
}

func (f *tenantFixture) current(ctx context.Context, user uuid.UUID) usersettings.Setting {
	f.t.Helper()
	got, err := f.svc.Get(ctx, user, tenantSetting)
	require.NoError(f.t, err)
	return *got
}

func (f *tenantFixture) rowCount(where string, args ...any) int {
	f.t.Helper()
	var n int
	require.NoError(f.t, f.owner.Global().QueryRow(context.Background(),
		`SELECT count(*) FROM user_tenant_settings WHERE `+where, args...).Scan(&n))
	return n
}

func TestTenantSettings_DefaultsUntilSet(t *testing.T) {
	f := newTenantFixture(t)
	u := f.member()
	s := f.current(f.ctx, u)
	assert.Equal(t, usersettings.BoolValue(false), s.Value)
	assert.Nil(t, s.UpdatedAt)

	all, err := f.svc.List(f.ctx, u)
	require.NoError(t, err)
	require.Len(t, all, 2, "the global name is not on the tenant route")
	assert.Equal(t, "test.auto_approve", all[0].Name)
	assert.Equal(t, "test.label", all[1].Name)
}

func TestTenantSettings_NewerValueWinsAndHistoryIsNewestFirst(t *testing.T) {
	f := newTenantFixture(t)
	u := f.member()
	for _, v := range []bool{true, false, true} {
		f.setBool(f.ctx, u, v)
	}
	s := f.current(f.ctx, u)
	assert.Equal(t, usersettings.BoolValue(true), s.Value)
	assert.NotNil(t, s.UpdatedAt)

	changes, next, err := f.svc.History(f.ctx, u, tenantSetting, 0, 0)
	require.NoError(t, err)
	assert.Zero(t, next)
	require.Len(t, changes, 3)
	got := []bool{changes[0].Value.Bool, changes[1].Value.Bool, changes[2].Value.Bool}
	assert.Equal(t, []bool{true, false, true}, got)
	assert.Greater(t, changes[0].ID, changes[1].ID)
	assert.Greater(t, changes[1].ID, changes[2].ID)
	for _, c := range changes {
		assert.False(t, c.EarlierMembership)
	}
}

func TestTenantSettings_HistoryPages(t *testing.T) {
	f := newTenantFixture(t)
	u := f.member()
	for _, v := range []bool{true, false, true, false, true} {
		f.setBool(f.ctx, u, v)
	}
	first, next, err := f.svc.History(f.ctx, u, tenantSetting, 0, 2)
	require.NoError(t, err)
	require.Len(t, first, 2)
	require.NotZero(t, next)
	second, next, err := f.svc.History(f.ctx, u, tenantSetting, next, 2)
	require.NoError(t, err)
	require.Len(t, second, 2)
	require.NotZero(t, next)
	third, next, err := f.svc.History(f.ctx, u, tenantSetting, next, 2)
	require.NoError(t, err)
	assert.Len(t, third, 1)
	assert.Zero(t, next)
	assert.Less(t, second[0].ID, first[1].ID, "pages do not overlap")
}

func TestTenantSettings_SettingsAreIndependent(t *testing.T) {
	f := newTenantFixture(t)
	u := f.member()
	f.setBool(f.ctx, u, true)
	_, err := f.svc.Set(f.ctx, u, "test.label", usersettings.StrValue("x"))
	require.NoError(t, err)
	assert.Equal(t, usersettings.BoolValue(true), f.current(f.ctx, u).Value)
	changes, _, err := f.svc.History(f.ctx, u, tenantSetting, 0, 0)
	require.NoError(t, err)
	assert.Len(t, changes, 1)
}

func TestTenantSettings_RefusesUnknownWrongTypeAndGlobalNames(t *testing.T) {
	f := newTenantFixture(t)
	u := f.member()
	_, err := f.svc.Set(f.ctx, u, "test.nope", usersettings.BoolValue(true))
	assert.ErrorIs(t, err, usersettings.ErrUnknownSetting)
	_, err = f.svc.Set(f.ctx, u, tenantSetting, usersettings.StrValue("yes"))
	assert.ErrorIs(t, err, usersettings.ErrTypeMismatch)
	_, err = f.svc.Set(f.ctx, u, "test.global", usersettings.BoolValue(true))
	assert.ErrorIs(t, err, usersettings.ErrUnknownSetting)
	_, err = f.svc.Get(f.ctx, u, "test.global")
	assert.ErrorIs(t, err, usersettings.ErrUnknownSetting)
	_, _, err = f.svc.History(f.ctx, u, "test.global", 0, 0)
	assert.ErrorIs(t, err, usersettings.ErrUnknownSetting)
	assert.Zero(t, f.rowCount(`true`))
}

func TestTenantSettings_NonMemberCannotReadOrWrite(t *testing.T) {
	f := newTenantFixture(t)
	stranger := dbtest.NewUser(t, f.db)
	_, err := f.svc.List(f.ctx, stranger)
	assert.ErrorIs(t, err, usersettings.ErrNotMember)
	_, err = f.svc.Get(f.ctx, stranger, tenantSetting)
	assert.ErrorIs(t, err, usersettings.ErrNotMember)
	_, _, err = f.svc.History(f.ctx, stranger, tenantSetting, 0, 0)
	assert.ErrorIs(t, err, usersettings.ErrNotMember)
	_, err = f.svc.Set(f.ctx, stranger, tenantSetting, usersettings.BoolValue(true))
	assert.ErrorIs(t, err, usersettings.ErrNotMember)
	assert.Zero(t, f.rowCount(`true`))
}

func TestTenantSettings_OneUsersValueIsInvisibleToAnother(t *testing.T) {
	f := newTenantFixture(t)
	a, b := f.member(), f.member()
	f.setBool(f.ctx, a, true)
	assert.Equal(t, usersettings.BoolValue(true), f.current(f.ctx, a).Value)
	assert.Equal(t, usersettings.BoolValue(false), f.current(f.ctx, b).Value)
	assert.True(t, f.current(f.ctx, b).UpdatedAt == nil)
	changes, _, err := f.svc.History(f.ctx, b, tenantSetting, 0, 0)
	require.NoError(t, err)
	assert.Empty(t, changes)
}

func TestTenantSettings_OneTenantsValueIsInvisibleInAnother(t *testing.T) {
	f := newTenantFixture(t)
	u := f.member()
	other := f.newTenant()
	f.join(other, u, time.Time{})
	f.setBool(f.ctx, u, true)
	assert.Equal(t, usersettings.BoolValue(true), f.current(f.ctx, u).Value)
	assert.Equal(t, usersettings.BoolValue(false), f.current(other, u).Value, "same user, other tenant")
	f.setBool(other, u, false)
	assert.Equal(t, usersettings.BoolValue(true), f.current(f.ctx, u).Value)
}

// A query that forgets its user filter is the case the restrictive policy
// exists for.
func TestTenantSettings_RawQueryWithoutUserFilterSeesOnlyTheCallersRows(t *testing.T) {
	f := newTenantFixture(t)
	a, b := f.member(), f.member()
	f.setBool(f.ctx, a, true)
	f.setBool(f.ctx, b, false)
	f.setBool(f.ctx, b, true)

	count := func(asUser uuid.UUID) (n int) {
		require.NoError(t, f.db.InTx(f.ctx, func(q db.Querier) error {
			if asUser != uuid.Nil {
				require.NoError(t, usersettings.AsUser(f.ctx, q, asUser))
			}
			return q.QueryRow(f.ctx, `SELECT count(*) FROM user_tenant_settings`).Scan(&n)
		}))
		return n
	}
	assert.Equal(t, 1, count(a))
	assert.Equal(t, 2, count(b))
	assert.Equal(t, 0, count(uuid.Nil), "no user set sees nothing")
	assert.Equal(t, 3, f.rowCount(`true`), "all three rows exist")
}

func TestTenantSettings_CannotWriteAnotherUsersRow(t *testing.T) {
	f := newTenantFixture(t)
	a, b := f.member(), f.member()
	err := f.db.InTx(f.ctx, func(q db.Querier) error {
		require.NoError(t, usersettings.AsUser(f.ctx, q, a))
		_, err := q.Exec(f.ctx,
			`INSERT INTO user_tenant_settings (user_id, setting_name, setting_type, bool_value) VALUES ($1, 'x', 'bool', true)`, b)
		return err
	})
	assert.Error(t, err)
	assert.Zero(t, f.rowCount(`true`))
}

func TestTenantSettings_LeavingKeepsRowsAndRejoiningStartsFromDefaults(t *testing.T) {
	f := newTenantFixture(t)
	u := dbtest.NewUser(t, f.db)
	f.join(f.ctx, u, time.Now().Add(-time.Hour))
	f.setBool(f.ctx, u, true)

	_, err := f.owner.Global().Exec(context.Background(),
		`DELETE FROM global_tenant_memberships WHERE tenant = $1 AND user_id = $2`, f.tenant, u)
	require.NoError(t, err)
	assert.Equal(t, 1, f.rowCount(`user_id = $1`, u), "leaving keeps the rows")
	_, _, err = f.svc.History(f.ctx, u, tenantSetting, 0, 0)
	assert.ErrorIs(t, err, usersettings.ErrNotMember, "but they are not served to a former member")

	_, err = f.owner.Global().Exec(context.Background(), `
		INSERT INTO global_tenant_memberships (id, tenant, user_id, role, created_at) VALUES ($1, $2, $3, 'member', clock_timestamp())`,
		uuid.New(), f.tenant, u)
	require.NoError(t, err)

	s := f.current(f.ctx, u)
	assert.Equal(t, usersettings.BoolValue(false), s.Value, "the old value must not return")
	assert.Nil(t, s.UpdatedAt)

	changes, _, err := f.svc.History(f.ctx, u, tenantSetting, 0, 0)
	require.NoError(t, err)
	require.Len(t, changes, 1)
	assert.True(t, changes[0].EarlierMembership)

	f.setBool(f.ctx, u, false)
	changes, _, err = f.svc.History(f.ctx, u, tenantSetting, 0, 0)
	require.NoError(t, err)
	require.Len(t, changes, 2)
	assert.False(t, changes[0].EarlierMembership)
	assert.True(t, changes[1].EarlierMembership)
}

func TestTenantSettings_AccountDeletionRemovesTheRows(t *testing.T) {
	f := newTenantFixture(t)
	a, b := f.member(), f.member()
	f.setBool(f.ctx, a, true)
	f.setBool(f.ctx, b, true)
	_, err := f.owner.Global().Exec(context.Background(), `DELETE FROM global_users WHERE id = $1`, a)
	require.NoError(t, err)
	assert.Zero(t, f.rowCount(`user_id = $1`, a))
	assert.Equal(t, 1, f.rowCount(`user_id = $1`, b))
}
