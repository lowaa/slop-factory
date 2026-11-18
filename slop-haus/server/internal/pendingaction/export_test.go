package pendingaction

import (
	"testing"
	"time"
)

// SetNow swaps the clock, so tests age proposals without sleeping.
func (s *Service) SetNow(now func() time.Time) { s.now = now }

// SetSpecForTest replaces a kind's rules until the test ends, adding the kind
// when it is not one: the kinds are a package-level table, so tests of the
// rules a kind can carry (an owner-only one, say) need a kind that has them.
func SetSpecForTest(t testing.TB, kind Kind, spec KindSpec) {
	t.Helper()
	oldSpec, had := kindSpecs[kind]
	oldKinds := Kinds
	kindSpecs[kind] = spec
	if !had {
		Kinds = append(append([]Kind(nil), Kinds...), kind)
	}
	t.Cleanup(func() {
		Kinds = oldKinds
		if had {
			kindSpecs[kind] = oldSpec
		} else {
			delete(kindSpecs, kind)
		}
	})
}

// ForgetKindForTest removes a kind's rules until the test ends, leaving
// proposals of it stored that the table no longer knows.
func ForgetKindForTest(t testing.TB, kind Kind) {
	t.Helper()
	oldSpec := kindSpecs[kind]
	oldKinds := Kinds
	delete(kindSpecs, kind)
	Kinds = nil
	for _, k := range oldKinds {
		if k != kind {
			Kinds = append(Kinds, k)
		}
	}
	t.Cleanup(func() {
		Kinds = oldKinds
		kindSpecs[kind] = oldSpec
	})
}

// ToBody is a view as the wire shows it.
func ToBody(v *View) ProposalBody { return toBody(v) }

// SetAssessments wires the steward's assessments after construction.
func (s *Service) SetAssessments(a Assessments) { s.assessments = a }

// SetDrafts swaps the steward's drafts reader.
func (s *Service) SetDrafts(d Drafts) { s.drafts = d }
