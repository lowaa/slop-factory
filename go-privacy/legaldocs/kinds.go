package legaldocs

import (
	"fmt"
	"regexp"
)

const (
	PrivacyPolicy          = "privacy_policy"
	CookiePolicy           = "cookie_policy"
	Terms                  = "terms"
	HealthDataNotice       = "health_data_notice"
	CCPANoticeAtCollection = "ccpa_notice_at_collection"
)

var builtinKinds = []string{PrivacyPolicy, CookiePolicy, Terms, HealthDataNotice, CCPANoticeAtCollection}

// The kind and version appear in URLs, file paths and database keys, so
// they are held to a small alphabet rather than escaped everywhere.
var (
	kindPattern    = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
	versionPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	localePattern  = regexp.MustCompile(`^[a-z]{2,3}(-[A-Za-z0-9]{2,8})*$`)
)

// Kinds is the set of document kinds an app publishes: the built-in ones
// plus any it declares.
type Kinds struct {
	known map[string]bool
}

// NewKinds returns the built-in kinds plus extra.
func NewKinds(extra ...string) (*Kinds, error) {
	k := &Kinds{known: map[string]bool{}}
	for _, name := range builtinKinds {
		k.known[name] = true
	}
	for _, name := range extra {
		if !kindPattern.MatchString(name) {
			return nil, fmt.Errorf("legaldocs: kind %q must be lowercase letters, digits and underscores", name)
		}
		k.known[name] = true
	}
	return k, nil
}

func (k *Kinds) Has(kind string) bool { return k.known[kind] }
