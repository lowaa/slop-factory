package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/pendingaction"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/usersettings"
)

func TestEveryAutoApprovableKindHasAnAutoApproveSettingThatIsOffByDefault(t *testing.T) {
	t.Parallel()
	tenantScoped := usersettings.DefaultRegistry().ForScope(usersettings.ScopeTenant)
	for _, kind := range pendingaction.AutoApprovableKinds() {
		name, ok := autoApproveSettingFor[kind]
		require.True(t, ok, "kind %q has no auto-approve setting", kind)
		assert.Equal(t, "approvals.auto."+string(kind), name)
		def, ok := tenantScoped.Lookup(name)
		require.True(t, ok, "%s is not a tenant-scoped setting", name)
		assert.Equal(t, usersettings.BoolValue(false), def.Default)
	}
	assert.Len(t, autoApproveSettingFor, len(pendingaction.AutoApprovableKinds()))
}

// A kind that must never be applied by a standing choice has no setting to
// switch on, so no stored value can reach it either.
func TestAKindThatIsNeverAutoApprovedHasNoAutoApproveSetting(t *testing.T) {
	t.Parallel()
	_, ok := autoApproveSettingFor[pendingaction.KindManifestEdit]
	assert.False(t, ok)
	_, found := usersettings.DefaultRegistry().ForScope(usersettings.ScopeTenant).Lookup("approvals.auto.manifest_edit")
	assert.False(t, found)
}
