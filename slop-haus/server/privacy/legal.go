package privacy

import (
	"embed"
	"fmt"

	"github.com/lowaa/slop-factory/go-privacy/inventory"
	"github.com/lowaa/slop-factory/go-privacy/legaldocs"
)

// legalFS holds the legal documents as <kind>/<version>.md. A published
// version is never edited: a change is a new file, and versions.lock
// (checked in legal_test.go) fails the build if an old one's text moves.
//
//go:embed legal
var legalFS embed.FS

// LegalRoot is the directory legalFS's documents are under.
const LegalRoot = "legal"

// LegalKinds is the document kinds slop-haus publishes. It has no health
// data notice: slop-haus holds no health data.
var LegalKinds = []string{legaldocs.PrivacyPolicy, legaldocs.CookiePolicy, legaldocs.Terms, legaldocs.CCPANoticeAtCollection}

// LoadLegalDocuments reads and renders every embedded version, filling the
// generated sections from the inventory.
func LoadLegalDocuments(inv *inventory.Inventory) ([]*legaldocs.Version, error) {
	kinds, err := legaldocs.NewKinds()
	if err != nil {
		return nil, err
	}
	loader, err := legaldocs.NewLoader(&legaldocs.LoaderConfig{Kinds: kinds, Fragments: inventory.NewFragments(inv)})
	if err != nil {
		return nil, err
	}
	versions, err := loader.Load(legalFS, LegalRoot)
	if err != nil {
		return nil, fmt.Errorf("privacy: legal documents: %w", err)
	}
	return versions, nil
}
