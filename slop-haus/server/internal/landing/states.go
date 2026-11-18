package landing

import (
	"fmt"
	"slices"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// The queue entry's state machine, written down once. The acting engine is part
// of the state, so "who may act on this row" is the row's state rather than a
// predicate each reader repeats. The table is the specification; SQL
// stays the atomic enforcer (a compare-and-set on the state).
//
// Staying put is always allowed and is not listed.

// Phase is the kind of state an entry is in, without the batch it belongs to.
type Phase string

const (
	PhaseQueued Phase = "queued"
	// The serial engine's steps.
	PhaseSerialMergingMain   Phase = "serial_merging_main"
	PhaseSerialWaitingChecks Phase = "serial_waiting_checks"
	PhaseSerialMerging       Phase = "serial_merging"
	// The batch engine's steps; the entry belongs to a batch.
	PhaseBatchTesting Phase = "batch_testing"
	PhaseBatchMerging Phase = "batch_merging"
	// PhaseMerged and PhaseDequeued are terminal: the row is gone.
	PhaseMerged Phase = "merged"
	// PhaseReleased is a testing batch's member let go; it only ever moves back to queued.
	PhaseReleased      Phase = "released"
	PhaseBackWithAgent Phase = "back_with_agent"
	PhaseDequeued      Phase = "dequeued"
)

var phases = []Phase{
	PhaseQueued, PhaseSerialMergingMain, PhaseSerialWaitingChecks, PhaseSerialMerging,
	PhaseBatchTesting, PhaseBatchMerging, PhaseMerged, PhaseReleased, PhaseBackWithAgent, PhaseDequeued,
}

// Phases lists every phase, in lifecycle order.
func Phases() []Phase { return slices.Clone(phases) }

// Valid reports whether p is a phase.
func (p Phase) Valid() bool { return slices.Contains(phases, p) }

// Serial reports whether the serial engine acts on an entry in p.
func (p Phase) Serial() bool {
	return p == PhaseSerialMergingMain || p == PhaseSerialWaitingChecks || p == PhaseSerialMerging
}

// Batch reports whether the batch engine acts on an entry in p.
func (p Phase) Batch() bool { return p == PhaseBatchTesting || p == PhaseBatchMerging }

// Active reports whether an engine is acting on an entry in p.
func (p Phase) Active() bool { return p.Serial() || p.Batch() }

// Terminal reports whether an entry in p has left the queue for good.
func (p Phase) Terminal() bool { return p == PhaseMerged || p == PhaseDequeued }

// Cause is why an entry moves: the event or decision behind a transition.
type Cause string

const (
	CauseSerialClaimed Cause = "serial_claimed"
	CauseMainMerged    Cause = "main_merged"
	CauseMainMoved     Cause = "main_moved"
	CauseChecksPassed  Cause = "checks_passed"
	CauseMerged        Cause = "merged"
	CauseBatchBuilt    Cause = "batch_built"
	CauseBatchGreen    Cause = "batch_green"
	CauseBatchMerged   Cause = "batch_merged"
	// CauseBatchEnded is a testing batch going red, voided or split.
	CauseBatchEnded Cause = "batch_ended"
	CauseRequeued   Cause = "requeued"
	CauseHandedBack Cause = "handed_back"
	CauseRefinished Cause = "refinished"
	CauseDequeued   Cause = "dequeued"
	// CauseMergedOutside is a change found merged on the host by something other
	// than the engine that held the entry.
	CauseMergedOutside Cause = "merged_outside"
)

var causes = []Cause{
	CauseSerialClaimed, CauseMainMerged, CauseMainMoved, CauseChecksPassed, CauseMerged,
	CauseBatchBuilt, CauseBatchGreen, CauseBatchMerged, CauseBatchEnded,
	CauseRequeued, CauseHandedBack, CauseRefinished, CauseDequeued, CauseMergedOutside,
}

// Causes lists every cause a transition can carry.
func Causes() []Cause { return slices.Clone(causes) }

// Transition is one legal move of an entry's phase.
type Transition struct {
	From, To Phase
	Cause    Cause
}

func (t Transition) String() string { return fmt.Sprintf("%s -> %s (%s)", t.From, t.To, t.Cause) }

// transitions is every legal move. There is no edge from a batch phase to a
// serial one: a row a batch has reserved is never the serial engine's.
// A handed-back row is the author's, so it only returns to queued; a closed
// entry is dequeued from queued, or from an active phase when its ticket is closed mid-run.
var transitions = buildTransitions()

func buildTransitions() []Transition {
	ts := []Transition{
		{PhaseQueued, PhaseSerialMergingMain, CauseSerialClaimed},
		// A head that is green and already has the base merges without a merge-main.
		{PhaseQueued, PhaseSerialMerging, CauseSerialClaimed},
		{PhaseSerialMergingMain, PhaseSerialWaitingChecks, CauseMainMerged},
		{PhaseSerialWaitingChecks, PhaseSerialMergingMain, CauseMainMoved},
		{PhaseSerialWaitingChecks, PhaseSerialMerging, CauseChecksPassed},
		// The merge is tried first, and refused when the head is behind the base.
		{PhaseSerialMerging, PhaseSerialMergingMain, CauseMainMoved},
		{PhaseSerialMerging, PhaseMerged, CauseMerged},

		{PhaseQueued, PhaseBatchTesting, CauseBatchBuilt},
		{PhaseBatchTesting, PhaseBatchMerging, CauseBatchGreen},
		// A member's own merge is refused as behind the base after an earlier member landed.
		{PhaseBatchMerging, PhaseBatchTesting, CauseMainMoved},
		{PhaseBatchMerging, PhaseMerged, CauseBatchMerged},
		{PhaseBatchTesting, PhaseReleased, CauseBatchEnded},
		{PhaseReleased, PhaseQueued, CauseRequeued},

		{PhaseBackWithAgent, PhaseQueued, CauseRefinished},
		{PhaseQueued, PhaseDequeued, CauseDequeued},
	}
	// Any state with work in flight, and a queued entry that fails before
	// one starts, can be handed back to its author.
	for _, p := range phases {
		if p == PhaseQueued || p.Active() {
			ts = append(ts, Transition{p, PhaseBackWithAgent, CauseHandedBack})
		}
	}
	// A change found merged on the host leaves the queue whatever the entry was
	// doing; the edges the engines own (a merging step's own merge) stay theirs.
	// An entry whose ticket is moved to discussion mid-run is dequeued.
	for _, p := range phases {
		if p != PhaseQueued && !p.Active() {
			continue
		}
		if _, owned := lookupIn(ts, p, PhaseMerged); !owned {
			ts = append(ts, Transition{p, PhaseMerged, CauseMergedOutside})
		}
		if _, owned := lookupIn(ts, p, PhaseDequeued); !owned {
			ts = append(ts, Transition{p, PhaseDequeued, CauseDequeued})
		}
	}
	return ts
}

// Transitions lists every legal transition.
func Transitions() []Transition { return slices.Clone(transitions) }

func lookupIn(ts []Transition, from, to Phase) (Transition, bool) {
	for _, t := range ts {
		if t.From == from && t.To == to {
			return t, true
		}
	}
	return Transition{}, false
}

// stateDiagram is the queue entry's state machine as a mermaid diagram.
func stateDiagram() string {
	table := map[Phase][]Phase{}
	for _, t := range transitions {
		table[t.From] = append(table[t.From], t.To)
	}
	return ticket.StateDiagram(phases, table)
}

// Lookup returns the transition from one phase to another, if the table has it.
func Lookup(from, to Phase) (Transition, bool) {
	for _, t := range transitions {
		if t.From == from && t.To == to {
			return t, true
		}
	}
	return Transition{}, false
}

// Allowed reports whether an entry may move from one phase to another.
func Allowed(from, to Phase) bool {
	if from == to {
		return true
	}
	_, ok := Lookup(from, to)
	return ok
}

// From lists the phases an entry in p can move to.
func From(p Phase) []Phase {
	var out []Phase
	for _, t := range transitions {
		if t.From == p {
			out = append(out, t.To)
		}
	}
	return out
}

// CausesOf lists the causes that move an entry from one phase to another.
func CausesOf(from, to Phase) []Cause {
	if t, ok := Lookup(from, to); ok {
		return []Cause{t.Cause}
	}
	return nil
}

// EntryState is a queue entry's full state: its phase and, in a batch phase,
// the batch acting on it.
type EntryState struct {
	Phase Phase
	// BatchID is set in a batch phase and zero otherwise.
	BatchID int64
}

// Valid reports whether the state is well formed: a batch phase names its
// batch, any other phase names none.
func (s EntryState) Valid() bool {
	if !s.Phase.Valid() {
		return false
	}
	return s.Phase.Batch() == (s.BatchID != 0)
}

// CanMoveTo reports whether an entry in s may move to next. A batch entry
// never moves straight to another batch: it is released first.
func (s EntryState) CanMoveTo(next EntryState) bool {
	if !s.Valid() || !next.Valid() {
		return false
	}
	if s.Phase.Batch() && next.Phase.Batch() && s.BatchID != next.BatchID {
		return false
	}
	return Allowed(s.Phase, next.Phase)
}
