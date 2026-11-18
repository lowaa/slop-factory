package privacy

import (
	"github.com/lowaa/slop-factory/go-privacy/consent"
	"github.com/lowaa/slop-factory/go-privacy/legaldocs"
)

// Purpose ids. They are stored in every consent event and named in the
// inventory, so they are never renamed.
const (
	PurposeNecessary = "necessary"
	PurposeAnalytics = "analytics"
	PurposeMarketing = "marketing"
)

// Registry is slop-haus's consent purposes. It has no processing purposes
// (no health data, no documents sent to a model) and no capability-gated
// ones. Today nothing sets analytics or marketing storage: the purposes are
// declared so the banner, the record and the script gate are in place before
// the first vendor needs them (go-privacy/README.md, section 12).
func Registry() (*consent.Registry, error) {
	return consent.NewRegistry(
		consent.Purpose{ID: PurposeNecessary, Kind: consent.CookieCategory, Necessary: true},
		consent.Purpose{ID: PurposeAnalytics, Kind: consent.CookieCategory, Document: legaldocs.CookiePolicy},
		consent.Purpose{ID: PurposeMarketing, Kind: consent.CookieCategory, Document: legaldocs.CookiePolicy, SaleOrShare: true},
	)
}
