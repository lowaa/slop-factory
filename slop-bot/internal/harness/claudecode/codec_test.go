package claudecode

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-bot/internal/harness"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/rpcdriver"
)

func decodeAll(t *testing.T, c *codec, lines ...string) []harness.Event {
	t.Helper()
	var evs []harness.Event
	for _, l := range lines {
		got, err := c.Decode([]byte(l), nil)
		require.NoError(t, err)
		evs = append(evs, got...)
	}
	return evs
}

func kinds(evs []harness.Event) []harness.EventKind {
	out := make([]harness.EventKind, len(evs))
	for i, e := range evs {
		out[i] = e.Kind
	}
	return out
}

func TestARecordedTurn(t *testing.T) {
	t.Parallel()
	f, err := os.Open("testdata/turn_with_tool.jsonl")
	require.NoError(t, err)
	defer func() { _ = f.Close() }()
	var lines []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	evs := decodeAll(t, &codec{}, lines...)
	assert.Equal(t, []harness.EventKind{
		harness.TurnStarted,
		harness.ToolStarted,
		harness.LimitReport,
		harness.LimitReport,
		harness.ToolEnded,
		harness.Text,
		harness.LimitReport,
		harness.LimitReport,
		harness.UsageReport,
		harness.TurnEnded,
	}, kinds(evs))
	assert.Equal(t, "Bash", evs[1].Tool)
	assert.Equal(t, "DONE", evs[5].Text)
	assert.Equal(t, "five_hour", evs[2].Limit.Window, "each window apart")
	assert.InDelta(t, 46.0, evs[2].Limit.UsedPercent, 0.001)
	assert.Equal(t, time.Unix(1795840800, 0).UTC(), evs[2].Limit.ResetsAt)
	assert.Equal(t, "seven_day", evs[3].Limit.Window)
	assert.InDelta(t, 34.0, evs[3].Limit.UsedPercent, 0.001)
	assert.Equal(t, int64(79), evs[8].Usage.OutputTokens)
	assert.InDelta(t, 0.00356304, evs[8].Usage.CostUSD, 1e-9)
	assert.True(t, evs[8].Usage.CostEstimated, "apiKeySource none: the subscription, so the cost is an estimate")
	assert.Equal(t, harness.TurnCompleted, evs[9].Outcome)
}

// A rejection holds even with no window figures, or one the windows don't
// name; a missing reset is zero, not 1970.
func TestARejectedRateLimitIsAFullWindow(t *testing.T) {
	t.Parallel()
	evs := decodeAll(t, &codec{},
		`{"type":"rate_limit_event","rate_limit_info":{"status":"rejected","resetsAt":1795840800,"rateLimitType":"seven_day_opus"}}`,
		`{"type":"rate_limit_event","rate_limit_info":{"status":"allowed","unifiedWindows":{"five_hour":{"utilization":0.5}}}}`)
	var limits []*harness.Limit
	for _, ev := range evs {
		if ev.Kind == harness.LimitReport {
			limits = append(limits, ev.Limit)
		}
	}
	assert.Equal(t, []*harness.Limit{
		{Window: "seven_day_opus", UsedPercent: 100, ResetsAt: time.Unix(1795840800, 0).UTC()},
		{Window: "five_hour", UsedPercent: 50},
	}, limits)
}

func TestTurnOutcomes(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		result string
		want   harness.TurnOutcome
	}{
		"success":       {`{"type":"result","subtype":"success","is_error":false}`, harness.TurnCompleted},
		"interrupted":   {`{"type":"result","subtype":"error_during_execution","is_error":true,"terminal_reason":"aborted_tools"}`, harness.TurnInterrupted},
		"not logged in": {`{"type":"result","subtype":"success","is_error":true,"result":"Not logged in · Please run /login"}`, harness.TurnFailed},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			evs := decodeAll(t, &codec{}, `{"type":"system","subtype":"init"}`, tc.result)
			last := evs[len(evs)-1]
			assert.Equal(t, harness.TurnEnded, last.Kind)
			assert.Equal(t, tc.want, last.Outcome)
		})
	}
}

func TestInitStartsOneTurnNotOnePerRecord(t *testing.T) {
	t.Parallel()
	c := &codec{}
	evs := decodeAll(t, c,
		`{"type":"system","subtype":"init"}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"a"}]}}`,
		`{"type":"system","subtype":"init"}`, // re-sent mid-turn when a steer is absorbed
		`{"type":"result","subtype":"success"}`,
		`{"type":"system","subtype":"init"}`, // the next turn
	)
	var starts int
	for _, e := range evs {
		if e.Kind == harness.TurnStarted {
			starts++
		}
	}
	assert.Equal(t, 2, starts)
}

func TestAnAPIKeysCostIsNotAnEstimate(t *testing.T) {
	t.Parallel()
	evs := decodeAll(t, &codec{},
		`{"type":"system","subtype":"init","apiKeySource":"ANTHROPIC_API_KEY"}`,
		`{"type":"result","subtype":"success","total_cost_usd":0.01}`,
	)
	require.Equal(t, harness.UsageReport, evs[1].Kind)
	assert.False(t, evs[1].Usage.CostEstimated)
}

func TestControlResponsesDontStartATurn(t *testing.T) {
	t.Parallel()
	evs := decodeAll(t, &codec{}, `{"type":"control_response","response":{"subtype":"success"}}`)
	assert.Empty(t, evs)
}

func wire(buf *bytes.Buffer) *rpcdriver.Wire { return rpcdriver.NewWire(buf) }

func TestSendAndInterruptEncoding(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	c := &codec{}
	require.NoError(t, c.Send(context.Background(), wire(&buf), &harness.Message{Text: "hello"}, false))
	require.NoError(t, c.Interrupt(context.Background(), wire(&buf)))
	require.NoError(t, c.Interrupt(context.Background(), wire(&buf)))

	dec := json.NewDecoder(&buf)
	var user, i1, i2 map[string]any
	require.NoError(t, dec.Decode(&user))
	require.NoError(t, dec.Decode(&i1))
	require.NoError(t, dec.Decode(&i2))
	assert.Equal(t, "user", user["type"])
	assert.Equal(t, "hello", user["message"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"])
	assert.Equal(t, "control_request", i1["type"])
	assert.Equal(t, "interrupt", i1["request"].(map[string]any)["subtype"])
	assert.NotEqual(t, i1["request_id"], i2["request_id"], "each request has its own id")
}

func TestOpenReturnsTheChosenID(t *testing.T) {
	t.Parallel()
	id, err := (&codec{id: "abc"}).Open(context.Background(), nil, nil)
	require.NoError(t, err)
	assert.Equal(t, "abc", id)
}

func TestCommonFlags(t *testing.T) {
	t.Parallel()
	h := &Harness{}
	none, err := h.common(&harness.Workspace{})
	require.NoError(t, err)
	assert.Empty(t, none)
	all, err := h.common(&harness.Workspace{Permissive: true, Model: "opus", Effort: "high", Briefing: "brief", ExtraDirs: []string{"/ws/t1-lib"}})
	require.NoError(t, err)
	assert.Equal(t, []string{
		"--dangerously-skip-permissions", "--model", "opus", "--effort", "high",
		"--append-system-prompt", "brief", "--add-dir", "/ws/t1-lib",
	}, all)
}

func TestThePreToolHookGoesInThroughSessionSettings(t *testing.T) {
	t.Parallel()
	h := &Harness{PreToolHook: []string{"/opt/slop bot/slop-bot", "hook", "pre-tool"}}
	args, err := h.common(&harness.Workspace{})
	require.NoError(t, err)
	require.Len(t, args, 2)
	assert.Equal(t, "--settings", args[0])
	var settings struct {
		Hooks map[string][]struct {
			Matcher string `json:"matcher"`
			Hooks   []struct {
				Type    string `json:"type"`
				Command string `json:"command"`
				Timeout int    `json:"timeout"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	require.NoError(t, json.Unmarshal([]byte(args[1]), &settings))
	pre := settings.Hooks["PreToolUse"]
	require.Len(t, pre, 1)
	assert.Equal(t, "Bash|mcp__.*", pre[0].Matcher)
	require.Len(t, pre[0].Hooks, 1)
	assert.Equal(t, "command", pre[0].Hooks[0].Type)
	assert.Equal(t, "'/opt/slop bot/slop-bot' hook pre-tool", pre[0].Hooks[0].Command, "quoted for the shell Claude Code runs it with")
	assert.Positive(t, pre[0].Hooks[0].Timeout)
}

func TestEnvDisablesBackgroundTasksAndKeepsAPIKeysOut(t *testing.T) {
	t.Parallel()
	h := &Harness{Environ: func() []string { return []string{"PATH=/bin", "ANTHROPIC_API_KEY=placeholder"} }}
	env := h.env(&harness.Workspace{Env: map[string]string{"SLOP_BOT_AGENT_TOKEN": "tok"}})
	assert.Contains(t, env, "CLAUDE_CODE_DISABLE_BACKGROUND_TASKS=1")
	assert.Contains(t, env, "SLOP_BOT_AGENT_TOKEN=tok")
	assert.NotContains(t, env, "ANTHROPIC_API_KEY=placeholder", "an API key would silently switch billing off the subscription")
	opted := h.env(&harness.Workspace{APIKeyOptIn: true})
	assert.Contains(t, opted, "ANTHROPIC_API_KEY=placeholder")
}

func TestAssistantAndToolResultRecords(t *testing.T) {
	t.Parallel()
	evs := decodeAll(t, &codec{},
		`{"type":"assistant","message":{"content":[{"type":"thinking"},{"type":"text","text":"a"},{"type":"tool_use","name":"Read"}]}}`,
		`{"type":"user","message":{"content":[{"type":"text","text":"ignored"},{"type":"tool_result"}]}}`,
	)
	assert.Equal(t, []harness.EventKind{harness.TurnStarted, harness.Text, harness.ToolStarted, harness.ToolEnded}, kinds(evs))
	assert.Equal(t, "Read", evs[2].Tool)
}

func TestARateLimitWithoutInfoReportsNothing(t *testing.T) {
	t.Parallel()
	evs := decodeAll(t, &codec{}, `{"type":"rate_limit_event"}`)
	assert.Equal(t, []harness.EventKind{harness.TurnStarted}, kinds(evs))
}

func TestMalformedLinesAreErrors(t *testing.T) {
	t.Parallel()
	_, err := (&codec{}).Decode([]byte(`{not json`), nil)
	assert.Error(t, err)
}

func TestWriteReportsEncodingFailures(t *testing.T) {
	t.Parallel()
	assert.Error(t, write(nil, make(chan int)))
}

func TestAResultResetsTheTurn(t *testing.T) {
	t.Parallel()
	c := &codec{}
	decodeAll(t, c, `{"type":"system","subtype":"init"}`)
	assert.True(t, c.inTurn)
	decodeAll(t, c, `{"type":"result","subtype":"success"}`)
	assert.False(t, c.inTurn)
}
