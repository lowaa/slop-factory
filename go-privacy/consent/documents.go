package consent

import (
	"context"
	"fmt"
	"sort"
	"time"
)

// DocVersion is one published version of a legal document.
type DocVersion struct {
	Version   string
	Effective time.Time
	// Material versions re-prompt everyone; non-material ones (a typo fix)
	// are cited by new decisions but do not ask again.
	Material bool
}

// Documents supplies the published versions of a document kind. It is the
// whole of this package's dependency on legal documents, so it works with
// whatever publishes them (the legaldocs package, once it exists, or a
// fixed list).
type Documents interface {
	// Versions returns every published version of kind, in any order. It
	// is an error for a kind to have none.
	Versions(ctx context.Context, kind string) ([]DocVersion, error)
}

// docState is a kind's versions ordered by (Effective, Version), the order
// that says whether a decision's version is at or after the latest
// material one.
type docState struct {
	rank           map[string]int
	latest         string
	latestMaterial int // rank of the latest material version
}

// newDocState orders the versions in force at now. A version whose
// effective date is still ahead is not one anybody can be asked to accept
// yet: counting it would deny existing grants early and refuse decisions
// that cite the text people can actually see.
func newDocState(kind string, versions []DocVersion, now time.Time) (*docState, error) {
	vs := make([]DocVersion, 0, len(versions))
	for _, v := range versions {
		if !v.Effective.After(now) {
			vs = append(vs, v)
		}
	}
	if len(vs) == 0 {
		return nil, fmt.Errorf("consent: document %q has no version in force", kind)
	}
	sort.Slice(vs, func(i, j int) bool {
		if !vs[i].Effective.Equal(vs[j].Effective) {
			return vs[i].Effective.Before(vs[j].Effective)
		}
		return vs[i].Version < vs[j].Version
	})
	s := &docState{rank: map[string]int{}, latest: vs[len(vs)-1].Version}
	for i, v := range vs {
		s.rank[v.Version] = i
		if v.Material {
			s.latestMaterial = i
		}
	}
	return s, nil
}

// current reports whether a decision citing version is at or after the
// latest material version. An unknown version is not current: failing
// closed means a decision cannot cite text this app never published.
func (s *docState) current(version string) bool {
	r, ok := s.rank[version]
	return ok && r >= s.latestMaterial
}
