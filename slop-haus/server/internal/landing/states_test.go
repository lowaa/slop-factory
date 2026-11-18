package landing

import (
	"fmt"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

type phasePair struct{ from, to Phase }

func (p phasePair) String() string { return fmt.Sprintf("%s -> %s", p.from, p.to) }

// legal is the specification, written out independently of the table.
var legal = map[phasePair]Cause{
	{PhaseQueued, PhaseSerialMergingMain}:              CauseSerialClaimed,
	{PhaseQueued, PhaseSerialMerging}:                  CauseSerialClaimed,
	{PhaseSerialMerging, PhaseSerialMergingMain}:       CauseMainMoved,
	{PhaseSerialMergingMain, PhaseSerialWaitingChecks}: CauseMainMerged,
	{PhaseSerialWaitingChecks, PhaseSerialMergingMain}: CauseMainMoved,
	{PhaseSerialWaitingChecks, PhaseSerialMerging}:     CauseChecksPassed,
	{PhaseSerialMerging, PhaseMerged}:                  CauseMerged,
	{PhaseQueued, PhaseBatchTesting}:                   CauseBatchBuilt,
	{PhaseBatchTesting, PhaseBatchMerging}:             CauseBatchGreen,
	{PhaseBatchMerging, PhaseBatchTesting}:             CauseMainMoved,
	{PhaseBatchMerging, PhaseMerged}:                   CauseBatchMerged,
	{PhaseBatchTesting, PhaseReleased}:                 CauseBatchEnded,
	{PhaseReleased, PhaseQueued}:                       CauseRequeued,
	{PhaseBackWithAgent, PhaseQueued}:                  CauseRefinished,
	{PhaseQueued, PhaseDequeued}:                       CauseDequeued,
	{PhaseQueued, PhaseBackWithAgent}:                  CauseHandedBack,
	{PhaseSerialMergingMain, PhaseBackWithAgent}:       CauseHandedBack,
	{PhaseSerialWaitingChecks, PhaseBackWithAgent}:     CauseHandedBack,
	{PhaseSerialMerging, PhaseBackWithAgent}:           CauseHandedBack,
	{PhaseBatchTesting, PhaseBackWithAgent}:            CauseHandedBack,
	{PhaseBatchMerging, PhaseBackWithAgent}:            CauseHandedBack,
	{PhaseQueued, PhaseMerged}:                         CauseMergedOutside,
	{PhaseSerialMergingMain, PhaseMerged}:              CauseMergedOutside,
	{PhaseSerialWaitingChecks, PhaseMerged}:            CauseMergedOutside,
	{PhaseBatchTesting, PhaseMerged}:                   CauseMergedOutside,
	{PhaseSerialMergingMain, PhaseDequeued}:            CauseDequeued,
	{PhaseSerialWaitingChecks, PhaseDequeued}:          CauseDequeued,
	{PhaseSerialMerging, PhaseDequeued}:                CauseDequeued,
	{PhaseBatchTesting, PhaseDequeued}:                 CauseDequeued,
	{PhaseBatchMerging, PhaseDequeued}:                 CauseDequeued,
}

func TestEveryPairIsLegalOrNot(t *testing.T) {
	for _, from := range Phases() {
		for _, to := range Phases() {
			pair := phasePair{from, to}
			cause, isLegal := legal[pair]
			if from == to {
				assert.True(t, Allowed(from, to), "staying put: %s", pair)
				continue
			}
			assert.Equal(t, isLegal, Allowed(from, to), pair.String())
			if isLegal {
				assert.Equal(t, []Cause{cause}, CausesOf(from, to), pair.String())
			} else {
				assert.Empty(t, CausesOf(from, to), pair.String())
			}
		}
	}
}

func TestTableHasNoDuplicatesAndKnownCauses(t *testing.T) {
	seen := map[phasePair]bool{}
	for _, tr := range Transitions() {
		pair := phasePair{tr.From, tr.To}
		assert.False(t, seen[pair], "duplicate %s", tr)
		seen[pair] = true
		assert.Contains(t, Causes(), tr.Cause, tr.String())
		assert.True(t, tr.From.Valid() && tr.To.Valid(), tr.String())
	}
	assert.Len(t, seen, len(legal))
}

func TestNoBatchToSerial(t *testing.T) {
	for _, from := range Phases() {
		for _, to := range Phases() {
			if from.Batch() && to.Serial() {
				assert.False(t, Allowed(from, to), "%s -> %s", from, to)
			}
		}
	}
	// Reachable-from closure: no path leads from a batch phase to a serial one
	// except through queued, via released or back_with_agent.
	for _, p := range []Phase{PhaseBatchTesting, PhaseBatchMerging} {
		for _, to := range From(p) {
			assert.False(t, to.Serial(), "%s -> %s", p, to)
		}
	}
}

func TestTerminalPhasesHaveNoExits(t *testing.T) {
	for _, p := range Phases() {
		if p.Terminal() {
			assert.Empty(t, From(p), p)
		}
	}
}

func TestReleasedOnlyReturnsToQueued(t *testing.T) {
	assert.Equal(t, []Phase{PhaseQueued}, From(PhaseReleased))
}

func TestEveryNonTerminalPhaseCanReachTerminal(t *testing.T) {
	for _, p := range Phases() {
		if p.Terminal() {
			continue
		}
		reach := map[Phase]bool{p: true}
		queue := []Phase{p}
		for len(queue) > 0 {
			cur := queue[0]
			queue = queue[1:]
			for _, n := range From(cur) {
				if !reach[n] {
					reach[n] = true
					queue = append(queue, n)
				}
			}
		}
		assert.True(t, reach[PhaseMerged] && reach[PhaseDequeued], "%s", p)
	}
}

func TestEntryStateBatchRules(t *testing.T) {
	testing1 := EntryState{PhaseBatchTesting, 1}
	assert.True(t, testing1.Valid())
	assert.False(t, EntryState{PhaseBatchTesting, 0}.Valid())
	assert.False(t, EntryState{PhaseQueued, 1}.Valid())
	assert.False(t, EntryState{Phase("nope"), 0}.Valid())

	assert.True(t, testing1.CanMoveTo(EntryState{PhaseBatchMerging, 1}))
	assert.False(t, testing1.CanMoveTo(EntryState{PhaseBatchMerging, 2}), "another batch")
	assert.False(t, testing1.CanMoveTo(EntryState{PhaseSerialMergingMain, 0}))
	assert.True(t, testing1.CanMoveTo(EntryState{PhaseReleased, 0}))
	assert.True(t, EntryState{PhaseQueued, 0}.CanMoveTo(EntryState{PhaseBatchTesting, 7}))
	assert.False(t, EntryState{PhaseQueued, 0}.CanMoveTo(EntryState{PhaseBatchTesting, 0}))
}

func TestPhaseAtStep(t *testing.T) {
	for _, c := range []struct {
		from EntryState
		step Step
		want Phase
		ok   bool
	}{
		{EntryState{PhaseQueued, 0}, MergingMain, PhaseSerialMergingMain, true},
		{EntryState{PhaseQueued, 0}, WaitingChecks, PhaseSerialWaitingChecks, true},
		{EntryState{PhaseQueued, 0}, Merging, PhaseSerialMerging, true},
		{EntryState{PhaseBatchTesting, 2}, MergingMain, PhaseBatchTesting, true},
		{EntryState{PhaseBatchTesting, 2}, WaitingChecks, PhaseBatchTesting, true},
		{EntryState{PhaseBatchTesting, 2}, Merging, PhaseBatchMerging, true},
		{EntryState{PhaseQueued, 0}, "bogus", "", false},
		{EntryState{PhaseBatchTesting, 2}, "bogus", "", false},
	} {
		got, ok := phaseAtStep(c.from, c.step)
		assert.Equal(t, c.ok, ok, "%v %s", c.from, c.step)
		assert.Equal(t, c.want, got, "%v %s", c.from, c.step)
	}
}

func TestBatchNeverMovesToAnotherBatchInSamePhase(t *testing.T) {
	assert.False(t, EntryState{PhaseBatchTesting, 1}.CanMoveTo(EntryState{PhaseBatchTesting, 2}))
	assert.False(t, EntryState{PhaseBatchMerging, 1}.CanMoveTo(EntryState{PhaseBatchMerging, 2}))
	assert.True(t, EntryState{PhaseBatchTesting, 1}.CanMoveTo(EntryState{PhaseBatchTesting, 1}))
}

func TestStateDiagramInTheDocs(t *testing.T) {
	path := "../../STATE_MACHINES.md"
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	want, ok := ticket.WithDiagram(string(raw), "landing_queue_entry", stateDiagram())
	require.True(t, ok, "STATE_MACHINES.md has no queue entry diagram markers")
	if os.Getenv("UPDATE_DOCS") != "" {
		require.NoError(t, os.WriteFile(path, []byte(want), 0o644))
		return
	}
	assert.Equal(t, want, string(raw), "STATE_MACHINES.md is stale: run with UPDATE_DOCS=1")
}
