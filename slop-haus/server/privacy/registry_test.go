package privacy_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-privacy/consent"
	"github.com/lowaa/slop-factory/go-privacy/inventory"

	"github.com/lowaa/slop-factory/slop-haus/server/privacy"
)

// The Go registry is what runs and the inventory is what is published, so
// they have to list the same purposes (go-privacy/README.md, section 4).
func TestRegistryAndInventoryAgreeOnConsentPurposes(t *testing.T) {
	reg, err := privacy.Registry()
	require.NoError(t, err)
	inv, err := privacy.Inventory()
	require.NoError(t, err)

	declared := map[string]consent.Kind{}
	for _, p := range reg.Purposes() {
		declared[p.ID] = p.Kind
	}
	listed := map[string]bool{}
	for _, p := range inv.Purposes {
		if p.Kind == inventory.Operational {
			continue
		}
		listed[p.ID] = true
		kind, ok := declared[p.ID]
		if assert.True(t, ok, "the inventory lists consent purpose %q the registry does not declare", p.ID) {
			assert.EqualValues(t, kind, p.Kind, p.ID)
		}
	}
	for id := range declared {
		assert.True(t, listed[id], "the registry declares %q, missing from the inventory", id)
	}
}

func TestRegistryHasNoProcessingPurposesAndMarketingIsASale(t *testing.T) {
	reg, err := privacy.Registry()
	require.NoError(t, err)
	for _, p := range reg.Purposes() {
		assert.Equal(t, consent.CookieCategory, p.Kind, p.ID)
		assert.Empty(t, p.Capability, p.ID)
	}
	assert.True(t, reg.Get(privacy.PurposeMarketing).SaleOrShare, "GPC must deny marketing")
	assert.False(t, reg.Get(privacy.PurposeAnalytics).SaleOrShare)
}

// slop-haus's cookies are necessary only. The one storage key under another
// purpose is the logger's random session id, which the logging package writes
// to sessionStorage only after analytics is granted (and keeps in memory
// otherwise). Any other key under a purpose that needs consent is a decision
// (a vendor, the script gate, a cookie policy version), so it can't be added
// by editing the inventory alone.
func TestInventoryStorageIsNecessaryExceptTheConsentedSessionID(t *testing.T) {
	inv, err := privacy.Inventory()
	require.NoError(t, err)
	for _, s := range inv.Storage {
		if s.Type == inventory.SessionStorage && s.Key == "slop-factory-logging:session-id" {
			assert.Equal(t, privacy.PurposeAnalytics, s.Purpose)
			continue
		}
		if s.Type == inventory.Cookie {
			assert.Equal(t, privacy.PurposeNecessary, s.Purpose, "cookie %s: slop-haus sets necessary cookies only", s.Key)
			continue
		}
		assert.Equal(t, privacy.PurposeNecessary, s.Purpose, "%s %s: see go-privacy/README.md section 12 before adding storage under a purpose that needs consent", s.Type, s.Key)
	}
}
