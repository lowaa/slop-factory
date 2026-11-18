package change

import (
	"slices"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// LandingState is where a change is in its landing, read from the
// ticket_changes columns (merged_at, superseded_at, closed_at and
// merging_since) rather than stored: the columns are written by independent
// statements, each guarded in SQL, and this is the one place that says what
// their combinations mean.
type LandingState string

const (
	// LandingOpen is a change nothing has decided yet: in draft, in review,
	// or ready and waiting on its merge.
	LandingOpen LandingState = "open"
	// LandingMerging is a change whose merge is in flight: a claim younger
	// than mergeClaimBound.
	LandingMerging LandingState = "merging"
	// LandingMerged is a change that was merged.
	LandingMerged LandingState = "merged"
	// LandingClosed is a change slop-haus closed on the host without merging
	// it (the closing rule).
	LandingClosed LandingState = "closed"
	// LandingSuperseded is a change replaced by a newer one for its attempt.
	LandingSuperseded LandingState = "superseded"
)

var landingStates = []LandingState{LandingOpen, LandingMerging, LandingMerged, LandingClosed, LandingSuperseded}

// LandingStates is every landing state.
func LandingStates() []LandingState { return slices.Clone(landingStates) }

// State is the change's landing state. Where the columns say more than one
// thing, replaced beats merged, merged beats closed and closed beats a merge
// claim. (The table refuses a change both merged and closed.)
func (r *Record) State() LandingState {
	switch {
	case r.SupersededAt != nil:
		return LandingSuperseded
	case r.MergedAt != nil:
		return LandingMerged
	case r.ClosedAt != nil:
		return LandingClosed
	case r.Merging:
		return LandingMerging
	}
	return LandingOpen
}

// landingTransitions is every move of a change's landing state that the
// store's statements can make. Merged and superseded are final, and a closed
// change cannot be merged: the table has a CHECK that it is not both. A merging
// change is not superseded: Open refuses while a claim is live (a claim that
// has expired reads as open again). Staying put is not listed.
var landingTransitions = map[LandingState][]LandingState{
	LandingOpen:       {LandingMerging, LandingMerged, LandingClosed, LandingSuperseded},
	LandingMerging:    {LandingOpen, LandingMerged, LandingClosed},
	LandingClosed:     {LandingSuperseded},
	LandingMerged:     nil,
	LandingSuperseded: nil,
}

// CanTransition reports whether the store can move a change from one landing
// state to another.
func (s LandingState) CanTransition(to LandingState) bool {
	return s == to || slices.Contains(landingTransitions[s], to)
}

// Final reports whether nothing moves a change out of s.
func (s LandingState) Final() bool { return len(landingTransitions[s]) == 0 }

// LandingStateDiagram is the landing machine as a mermaid diagram.
func LandingStateDiagram() string { return ticket.StateDiagram(landingStates, landingTransitions) }
