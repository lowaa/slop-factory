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
		harness.ToolEnded,
		harness.Text,
		harness.LimitReport,
		harness.UsageReport,
		harness.TurnEnded,
	}, kinds(evs))
	assert.Equal(t, "Bash", evs[1].Tool)
	assert.Equal(t, "DONE", evs[4].Text)
	assert.InDelta(t, 46.0, evs[2].Limit.UsedPercent, 0.001, "the tightest window: five_hour at 0.46")
	assert.Equal(t, time.Unix(1795840800, 0).UTC(), evs[2].Limit.ResetsAt)
	assert.Equal(t, int64(79), evs[6].Usage.OutputTokens)
	assert.InDelta(t, 0.00356304, evs[6].Usage.CostUSD, 1e-9)
	assert.Equal(t, harness.TurnCompleted, evs[7].Outcome)
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
	assert.Empty(t, common(&harness.Workspace{}))
	assert.Equal(t, []string{
		"--dangerously-skip-permissions", "--model", "opus", "--effort", "high",
		"--append-system-prompt", "brief", "--add-dir", "/ws/t1-lib",
	}, common(&harness.Workspace{Permissive: true, Model: "opus", Effort: "high", Briefing: "brief", ExtraDirs: []string{"/ws/t1-lib"}}))
}

func TestEnvDisablesBackgroundTasksAndKeepsAPIKeysOut(t *testing.T) {
	t.Parallel()
	h := &Harness{Environ: func() []string { return []string{"PATH=/bin", "ANTHROPIC_API_KEY=sk"} }}
	env := h.env(&harness.Workspace{Env: map[string]string{"SLOP_BOT_AGENT_TOKEN": "tok"}})
	assert.Contains(t, env, "CLAUDE_CODE_DISABLE_BACKGROUND_TASKS=1")
	assert.Contains(t, env, "SLOP_BOT_AGENT_TOKEN=tok")
	assert.NotContains(t, env, "ANTHROPIC_API_KEY=sk", "an API key would silently switch billing off the subscription")
	opted := h.env(&harness.Workspace{APIKeyOptIn: true})
	assert.Contains(t, opted, "ANTHROPIC_API_KEY=sk")
}
