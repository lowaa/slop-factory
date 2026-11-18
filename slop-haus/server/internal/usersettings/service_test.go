package usersettings_test

import (
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/usersettings"
)

func newService(t *testing.T, f *fixture, defs ...usersettings.Definition) *usersettings.Service {
	t.Helper()
	reg, err := usersettings.NewRegistry(defs...)
	require.NoError(t, err)
	return usersettings.NewService(f.store, reg, slog.DiscardHandler)
}

func byName(settings []usersettings.Setting) map[string]usersettings.Setting {
	out := map[string]usersettings.Setting{}
	for _, s := range settings {
		out[s.Name] = s
	}
	return out
}

func TestRegistry_SessionSwitchExplainedIsKnownAndDefaultsOff(t *testing.T) {
	d, ok := usersettings.DefaultRegistry().Lookup(usersettings.RemoteControlSessionSwitchExplained)
	require.True(t, ok)
	assert.Equal(t, "remote_control.session_switch_explained", d.Name)
	assert.Equal(t, usersettings.BoolValue(false), d.Default)
}

func TestRegistry_RetiredEnterSendsIsUnknown(t *testing.T) {
	_, ok := usersettings.DefaultRegistry().Lookup("remote_control.enter_sends")
	assert.False(t, ok)
}

func TestRegistry_RefusesBadDeclarations(t *testing.T) {
	_, err := usersettings.NewRegistry(usersettings.Definition{Name: "a", Default: usersettings.BoolValue(true)}, usersettings.Definition{Name: "a", Default: usersettings.BoolValue(true)})
	assert.Error(t, err, "duplicate")
	_, err = usersettings.NewRegistry(usersettings.Definition{Name: "", Default: usersettings.BoolValue(true)})
	assert.Error(t, err, "nameless")
	_, err = usersettings.NewRegistry(usersettings.Definition{Name: "a"})
	assert.Error(t, err, "typeless")
}

func TestService_ReturnsTheDefaultWhenThereIsNoRow(t *testing.T) {
	f := newFixture(t)
	svc := newService(t, f, usersettings.Definition{Name: "a", Default: usersettings.BoolValue(true)})

	got, err := svc.List(f.ctx, f.newUser())
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, usersettings.BoolValue(true), got[0].Value)
	assert.Nil(t, got[0].UpdatedAt)
}

func TestService_SetThenListReturnsTheNewValue(t *testing.T) {
	f := newFixture(t)
	svc := newService(t, f, usersettings.Definition{Name: "a", Default: usersettings.BoolValue(true)})
	u := f.newUser()

	set, err := svc.Set(f.ctx, u, "a", usersettings.BoolValue(false))
	require.NoError(t, err)
	assert.NotNil(t, set.UpdatedAt)

	got, err := svc.List(f.ctx, u)
	require.NoError(t, err)
	assert.Equal(t, usersettings.BoolValue(false), byName(got)["a"].Value)
	assert.NotNil(t, byName(got)["a"].UpdatedAt)
}

func TestService_RefusesUnknownNamesAndWrongTypes(t *testing.T) {
	f := newFixture(t)
	svc := newService(t, f, usersettings.Definition{Name: "a", Default: usersettings.BoolValue(true)})
	u := f.newUser()

	_, err := svc.Set(f.ctx, u, "nope", usersettings.BoolValue(true))
	assert.ErrorIs(t, err, usersettings.ErrUnknownSetting)
	_, err = svc.Set(f.ctx, u, "a", usersettings.StrValue("yes"))
	assert.ErrorIs(t, err, usersettings.ErrTypeMismatch)

	var n int
	require.NoError(t, f.db.Global().QueryRow(f.ctx, `SELECT count(*) FROM global_user_settings`).Scan(&n))
	assert.Zero(t, n, "a refused write leaves no row")
}

func TestService_IgnoresRowsForSettingsNoLongerKnown(t *testing.T) {
	f := newFixture(t)
	u := f.newUser()
	both := newService(t, f,
		usersettings.Definition{Name: "a", Default: usersettings.BoolValue(true)},
		usersettings.Definition{Name: "retired", Default: usersettings.BoolValue(true)})
	_, err := both.Set(f.ctx, u, "retired", usersettings.BoolValue(false))
	require.NoError(t, err)

	got, err := newService(t, f, usersettings.Definition{Name: "a", Default: usersettings.BoolValue(true)}).List(f.ctx, u)
	require.NoError(t, err)
	assert.Len(t, got, 1)
	assert.Contains(t, byName(got), "a")
}

func TestService_IgnoresAStoredValueWhoseTypeNoLongerMatches(t *testing.T) {
	f := newFixture(t)
	u := f.newUser()
	_, err := newService(t, f, usersettings.Definition{Name: "a", Default: usersettings.StrValue("x")}).Set(f.ctx, u, "a", usersettings.StrValue("stored"))
	require.NoError(t, err)

	got, err := newService(t, f, usersettings.Definition{Name: "a", Default: usersettings.BoolValue(true)}).List(f.ctx, u)
	require.NoError(t, err)
	assert.Equal(t, usersettings.BoolValue(true), got[0].Value)
	assert.Nil(t, got[0].UpdatedAt)
}

func TestService_UsersAreIsolated(t *testing.T) {
	f := newFixture(t)
	svc := newService(t, f, usersettings.Definition{Name: "a", Default: usersettings.BoolValue(true)})
	alice, bob := f.newUser(), f.newUser()
	_, err := svc.Set(f.ctx, alice, "a", usersettings.BoolValue(false))
	require.NoError(t, err)

	got, err := svc.List(f.ctx, bob)
	require.NoError(t, err)
	assert.Equal(t, usersettings.BoolValue(true), got[0].Value)
}

func TestRegistry_ANameCannotBeBothGlobalAndTenantScoped(t *testing.T) {
	_, err := usersettings.NewRegistry(
		usersettings.Definition{Name: "a", Default: usersettings.BoolValue(true)},
		usersettings.Definition{Name: "a", Scope: usersettings.ScopeTenant, Default: usersettings.BoolValue(true)},
	)
	assert.Error(t, err)
	_, err = usersettings.NewRegistry(usersettings.Definition{Name: "a", Scope: "nope", Default: usersettings.BoolValue(true)})
	assert.Error(t, err)
}

func TestRegistry_ForScopeHoldsOnlyThatScope(t *testing.T) {
	reg, err := usersettings.NewRegistry(
		usersettings.Definition{Name: "g", Default: usersettings.BoolValue(true)},
		usersettings.Definition{Name: "t", Scope: usersettings.ScopeTenant, Default: usersettings.BoolValue(true)},
	)
	require.NoError(t, err)
	assert.Equal(t, []string{"g"}, reg.ForScope(usersettings.ScopeGlobal).Names())
	assert.Equal(t, []string{"t"}, reg.ForScope(usersettings.ScopeTenant).Names())
	_, ok := reg.ForScope(usersettings.ScopeGlobal).Lookup("t")
	assert.False(t, ok)
}
