package agents_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/lowaa/slop-factory/slop-bot/internal/harness/fake"
)

// A report at the threshold holds: the threshold is the first held
// percentage, not the last free one.
func TestAReportExactlyAtTheThresholdHolds(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "harnesses: {claude-code: {limit_threshold: 80}}\n")
	resets := t0.Add(time.Hour)
	f.reportLimits(
		fake.Command{Cmd: fake.CmdLimit, Used: 80, ResetsAt: resets.Unix(), Window: "at"},
		fake.Command{Cmd: fake.CmdLimit, Used: 79.9, ResetsAt: resets.Unix(), Window: "below"},
	)
	held := map[string]bool{}
	for _, l := range f.mgr.Limits() {
		held[l.Window] = l.Held
	}
	assert.Equal(t, map[string]bool{"at": true, "below": false}, held)
}
