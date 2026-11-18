package harnessconfig_test

import (
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/harnessconfig"
)

// Policy is the service's, so a caller that skips the HTTP handler (and its
// route markers) is refused all the same.
func TestService_AConductorsOverrideIsItsOwnersAlone(t *testing.T) {
	w := newWire(t)
	values := harnessconfig.Values{"claude": {"model": "haiku"}}

	for name, who := range map[string]*authn.User{"another member": w.otherMember, "the tenant's owner": w.tenantOwner} {
		_, err := w.svc.Save(w.tenantCtx, &w.conductor.ID, values, who)
		assert.ErrorIs(t, err, harnessconfig.ErrNotConfigOwner, "save by %s", name)
		assert.ErrorIs(t, w.svc.RemoveOverride(w.tenantCtx, w.conductor.ID, who), harnessconfig.ErrNotConfigOwner, "remove by %s", name)
	}
	tenant, err := w.svc.Load(w.tenantCtx)
	require.NoError(t, err)
	assert.Empty(t, tenant.Overrides, "a refused save wrote nothing")

	_, err = w.svc.Save(w.tenantCtx, &w.conductor.ID, values, w.conductorOwner)
	require.NoError(t, err, "its owner")
	tenant, err = w.svc.Load(w.tenantCtx)
	require.NoError(t, err)
	require.NotEmpty(t, tenant.Overrides)

	for _, who := range []*authn.User{w.otherMember, w.tenantOwner} {
		require.ErrorIs(t, w.svc.RemoveOverride(w.tenantCtx, w.conductor.ID, who), harnessconfig.ErrNotConfigOwner)
	}
	tenant, err = w.svc.Load(w.tenantCtx)
	require.NoError(t, err)
	assert.NotEmpty(t, tenant.Overrides, "a refused removal kept the override")
	require.NoError(t, w.svc.RemoveOverride(w.tenantCtx, w.conductor.ID, w.conductorOwner))
}

func TestService_WithoutAGateNoOverrideCanBeChanged(t *testing.T) {
	w := newWire(t)
	svc := harnessconfig.NewService(harnessconfig.NewPostgresStore(w.database), nil, slog.DiscardHandler)
	_, err := svc.Save(w.tenantCtx, &w.conductor.ID, harnessconfig.Values{}, w.conductorOwner)
	assert.ErrorIs(t, err, harnessconfig.ErrNotConfigOwner)
}

func TestService_OnlyASignedInPersonCanChangeConfig(t *testing.T) {
	w := newWire(t)
	values := harnessconfig.Values{"claude": {"model": "haiku"}}

	_, err := w.svc.Save(w.tenantCtx, nil, values, nil)
	assert.ErrorIs(t, err, authn.ErrNotPerson, "defaults")
	_, err = w.svc.Save(w.tenantCtx, &w.conductor.ID, values, nil)
	assert.ErrorIs(t, err, authn.ErrNotPerson, "override")
	assert.ErrorIs(t, w.svc.RemoveOverride(w.tenantCtx, w.conductor.ID, nil), authn.ErrNotPerson)

	tenant, err := w.svc.Load(w.tenantCtx)
	require.NoError(t, err)
	assert.Nil(t, tenant.Defaults, "a refused save wrote nothing")

	// Refused as not-a-person, not as not-the-owner, whatever the gate says.
	ungated := harnessconfig.NewService(harnessconfig.NewPostgresStore(w.database), nil, slog.DiscardHandler)
	assert.ErrorIs(t, ungated.RemoveOverride(w.tenantCtx, w.conductor.ID, nil), authn.ErrNotPerson)
}
