package cli

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/lowaa/slop-factory/slop-bot/internal/api"
)

func TestDescribeEvent(t *testing.T) {
	t.Parallel()
	resets := time.Date(2026, 3, 4, 5, 6, 0, 0, time.UTC)
	resetsText := resets.Local().Format("Jan 2 15:04")
	at := time.Date(2026, 3, 4, 7, 8, 9, 0, time.UTC)

	cases := []struct {
		name    string
		typ     string
		payload string
		want    string
	}{
		{"watch of a live agent", api.WatchStarted, `{"state":"running"}`, "watching a1 (ticket T-1), running"},
		{"watch of an ended agent", api.WatchStarted, `{"state":"done","ended":true}`,
			"watching a1 (ticket T-1), done: it has ended; `slop-bot transcript a1` shows what it did"},
		{"turn started", "harness.turn_started", ``, "turn started"},
		{"turn ended", "harness.turn_ended", `{"outcome":"completed"}`, "turn ended: completed"},
		{"turn ended with text", "harness.turn_ended", `{"outcome":"failed","text":"boom"}`, "turn ended: failed: boom"},
		{"tool started", "harness.tool_started", `{"tool":"Bash"}`, "tool Bash"},
		{"tool ended", "harness.tool_ended", `{"tool":"Bash"}`, "tool done Bash"},
		{"text indents continuation lines", "harness.text", `{"text":"one\ntwo\n"}`, "text: one\n    two"},
		{"truncated text", "harness.text", `{"text":"one","truncated":7}`, "text: one … (7 more bytes)"},
		{"usage", "harness.usage", `{"input_tokens":3,"output_tokens":4}`, "usage: input 3, output 4"},
		{"limit", "harness.limit", `{"used_percent":42}`, "plan: 42% used"},
		{"limit with a window", "harness.limit", `{"used_percent":42,"window":"5h"}`, "plan 5h: 42% used"},
		{"limit that resets", "harness.limit", `{"used_percent":42,"resets_at":"` + resets.Format(time.RFC3339) + `"}`,
			"plan: 42% used, resets " + resetsText},
		{"state change", "agent.state", `{"from":"idle","to":"working"}`, "state idle → working"},
		{"state change with a reason", "agent.state", `{"from":"idle","to":"parked","reason":"limit"}`, "state idle → parked (limit)"},
		{"unknown type drops the agent prefix", "agent.spawned", ``, "spawned"},
		{"unparseable payload still names the event", "agent.spawned", `not json`, "spawned"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ev := &api.Event{Type: tc.typ, Agent: "a1", Ticket: "T-1", Payload: json.RawMessage(tc.payload)}
			assert.Equal(t, "[--:--:--] "+tc.want+"\n", describe(ev))

			ev.At = at
			assert.Equal(t, "["+at.Local().Format("15:04:05")+"] "+tc.want+"\n", describe(ev))
		})
	}
}
