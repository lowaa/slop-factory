package archtest

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ownerHelper matches a package-local owner or member check. "Owner of this
// tenant" is defined once, by authn.OwnerGate: a local copy is how the
// packages came to disagree on agents, nil dependencies and auditing.
var ownerHelper = regexp.MustCompile(`(?m)^func (?:\([^)]*\) )?(require(?:Tenant)?(?:Owner|Member))\(`)

// knownOwnerHelpers maps "package/file: helper" to why it is not the gate.
var knownOwnerHelpers = map[string]string{
	"settings/service.go: requireOwner":           "decides which settings are owner-only, then asks the gate",
	"ticket/close_merged.go: requireTenantOwner":  "returns the tenant it checked, after asking the gate",
	"user/invite.go: requireMember":               "the user package owns the membership table and asks its own store",
	"usersettings/tenant_store.go: requireMember": "a query helper inside a transaction, not an authorisation gate",
}

func TestNoLocalOwnerHelpers(t *testing.T) {
	root := ".."
	var found []string
	require.NoError(t, filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if strings.HasPrefix(rel, "authn/") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, m := range ownerHelper.FindAllStringSubmatch(string(src), -1) {
			found = append(found, rel+": "+m[1])
		}
		return nil
	}))
	for _, f := range found {
		_, ok := knownOwnerHelpers[f]
		assert.True(t, ok, "%s: use authn.OwnerGate or authn.MemberGate instead of a local helper", f)
	}
	for k := range knownOwnerHelpers {
		assert.Contains(t, found, k, "stale allowlist entry")
	}
}
