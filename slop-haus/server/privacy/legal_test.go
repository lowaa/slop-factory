package privacy_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-privacy/legaldocs"
	"github.com/lowaa/slop-factory/go-privacy/legaldocs/legaldocstest"

	"github.com/lowaa/slop-factory/slop-haus/server/privacy"
)

const lockFile = "legal/versions.lock"

func loadVersions(t *testing.T) []*legaldocs.Version {
	t.Helper()
	inv, err := privacy.Inventory()
	require.NoError(t, err)
	versions, err := privacy.LoadLegalDocuments(inv)
	require.NoError(t, err)
	return versions
}

// A published version is immutable (go-privacy/README.md, section 10), and
// this is that check without a database, so an edit fails the PR rather than
// a deploy. Every version's source is locked, and the rendered text of the
// version in force: see legaldocs.Lock for why an older version's render is
// not. UPDATE_VERSIONS_LOCK=1 adds the lines for new versions.
func TestVersionsLock(t *testing.T) {
	legaldocstest.CheckLock(t, lockFile, loadVersions(t))
}

func TestPlaceholderDocumentsAreDraftsWithMaterialDecided(t *testing.T) {
	versions := loadVersions(t)
	kinds := map[string]bool{}
	for _, v := range versions {
		kinds[v.Kind] = true
		assert.False(t, v.Reviewed, "%s/%s is a placeholder until a qualified person reviews it", v.Kind, v.Version)
		assert.NotContains(t, v.Body, "{{inventory:", "%s/%s has an unfilled generated section", v.Kind, v.Version)
		assert.NotContains(t, v.Body, legaldocs.PendingInventory, "%s/%s has a pending generated section", v.Kind, v.Version)
	}
	for _, k := range privacy.LegalKinds {
		assert.True(t, kinds[k], "no document for %s", k)
	}
	assert.NotContains(t, kinds, legaldocs.HealthDataNotice, "slop-haus holds no health data")
}

// The cookie policy has to say what the inventory says is stored, so a
// storage key added to the inventory shows up in the published text.
func TestCookiePolicyListsTheInventorysStorage(t *testing.T) {
	inv, err := privacy.Inventory()
	require.NoError(t, err)
	var body string
	for _, v := range loadVersions(t) {
		if v.Kind == legaldocs.CookiePolicy {
			body = v.Body
		}
	}
	require.NotEmpty(t, body)
	for _, s := range inv.Storage {
		assert.Contains(t, body, s.Key)
	}
}

// The reason a version in force is locked on what it renders: a change to the
// inventory's retention classes shows in the documents, and has to ship as a
// new version rather than move a published one.
func TestARetentionChangeToTheInventoryFailsTheLockForTheVersionInForce(t *testing.T) {
	inv, err := privacy.Inventory()
	require.NoError(t, err)
	require.NotEmpty(t, inv.RetentionClasses)
	inv.RetentionClasses[0].Description += " (changed)"
	versions, err := privacy.LoadLegalDocuments(inv)
	require.NoError(t, err)

	problems, err := legaldocstest.ReconcileLock("legal/versions.lock", versions, false)
	require.NoError(t, err)
	require.NotEmpty(t, problems)
	assert.Contains(t, strings.Join(problems, "\n"), "in force")
}
