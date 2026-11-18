package metrics_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/metrics"
)

func TestInvariantViolations_StartAtZeroAndFollowTheLastCheck(t *testing.T) {
	m := metrics.New(&metrics.Options{})
	m.TrackInvariantRules([]string{"rule_a", "rule_b"})

	out := scrape(t, m)
	assert.Contains(t, out, `slophaus_merge_queue_invariant_violations{rule="rule_a"} 0`)
	assert.Contains(t, out, `slophaus_merge_queue_invariant_violations{rule="rule_b"} 0`)

	m.SetInvariantViolations(map[string]int{"rule_a": 3})
	out = scrape(t, m)
	assert.Contains(t, out, `slophaus_merge_queue_invariant_violations{rule="rule_a"} 3`)
	assert.Contains(t, out, `slophaus_merge_queue_invariant_violations{rule="rule_b"} 0`)

	m.SetInvariantViolations(nil)
	assert.Contains(t, scrape(t, m), `slophaus_merge_queue_invariant_violations{rule="rule_a"} 0`)
}
