package supervisorwatch_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/contracts"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/supervisorwatch"
)

// The server accepts the phases in the contracts manifest, and no others; the
// supervisor's own test holds fleet/phase.py to the same file.
func TestSlotPhases_MatchTheManifest(t *testing.T) {
	var ours []string
	for _, p := range supervisorwatch.SlotPhases {
		ours = append(ours, string(p))
	}
	assert.ElementsMatch(t, contracts.LoadPhases(t).SlotPhases, ours)
}

func TestIdleReasons_MatchTheManifest(t *testing.T) {
	var ours []string
	for _, r := range supervisorwatch.IdleReasons {
		ours = append(ours, string(r))
	}
	assert.ElementsMatch(t, contracts.LoadPhases(t).SlotReasons, ours)
}
