package agents_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/lowaa/slop-factory/slop-bot/internal/agents"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness"
)

// The event log carries only what an event says: `watch` and `events`
// readers take a key's presence as the event having it, so an empty field
// mustn't appear as "" or a zero time.
func TestAnEventsPayloadHasExactlyTheFieldsItSet(t *testing.T) {
	t.Parallel()
	resets := time.Date(2026, 11, 28, 10, 0, 0, 0, time.UTC)
	cases := map[string]struct {
		ev   harness.Event
		want map[string]any
	}{
		"nothing set": {harness.Event{Kind: harness.TurnStarted}, map[string]any{}},
		"a tool call": {harness.Event{Kind: harness.ToolStarted, Tool: "bash"}, map[string]any{"tool": "bash"}},
		"a turn end": {harness.Event{Kind: harness.TurnEnded, Outcome: harness.TurnCompleted},
			map[string]any{"outcome": "completed"}},
		"text": {harness.Event{Kind: harness.Text, Text: "hello"}, map[string]any{"text": "hello"}},
		"a one-window limit": {harness.Event{Kind: harness.LimitReport, Limit: &harness.Limit{UsedPercent: 40}},
			map[string]any{"used_percent": 40.0}},
		"a named window with its reset": {
			harness.Event{Kind: harness.LimitReport, Limit: &harness.Limit{Window: "five_hour", UsedPercent: 40, ResetsAt: resets}},
			map[string]any{"used_percent": 40.0, "window": "five_hour", "resets_at": resets},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, agents.EventPayload(&tc.ev))
		})
	}
}
