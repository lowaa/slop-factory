package pi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-bot/internal/harness"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/rpcdriver"
)

func testCodec() *codec { return newCodec("probe-0001", false, slog.New(slog.DiscardHandler)) }

func fixture(t *testing.T, name string) []string {
	t.Helper()
	f, err := os.Open("testdata/" + name)
	require.NoError(t, err)
	defer func() { _ = f.Close() }()
	var lines []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	require.NoError(t, sc.Err())
	return lines
}

func decodeAll(t *testing.T, c *codec, w *rpcdriver.Wire, lines ...string) []harness.Event {
	t.Helper()
	var evs []harness.Event
	for _, l := range lines {
		got, err := c.Decode([]byte(l), w)
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

func TestARecordedTurnWithATool(t *testing.T) {
	t.Parallel()
	evs := decodeAll(t, testCodec(), nil, fixture(t, "turn_with_tool.jsonl")...)
	require.Equal(t, []harness.EventKind{
		harness.TurnStarted,
		harness.ToolStarted,
		harness.ToolEnded,
		harness.Text,
		harness.UsageReport,
		harness.TurnEnded,
	}, kinds(evs), "one turn for the whole run: two pi turns and agent_end inside it")
	assert.Equal(t, "bash", evs[1].Tool)
	assert.Equal(t, "bash", evs[2].Tool)
	assert.Equal(t, "DONE", evs[3].Text)
	u := evs[4].Usage
	assert.Equal(t, int64(1329+341), u.InputTokens, "summed over both model requests")
	assert.Equal(t, int64(23+5), u.OutputTokens)
	assert.Equal(t, int64(1024), u.CacheReadTokens)
	assert.InDelta(t, 0.002888+0.0008344, u.CostUSD, 1e-9)
	assert.True(t, u.CostEstimated, "pi's cost is a list-price estimate")
	assert.Equal(t, harness.TurnCompleted, evs[5].Outcome)
}

func TestARecordedSteerStaysInTheSameTurn(t *testing.T) {
	t.Parallel()
	evs := decodeAll(t, testCodec(), nil, fixture(t, "steer_mid_tool.jsonl")...)
	require.Equal(t, []harness.EventKind{
		harness.TurnStarted,
		harness.ToolStarted,
		harness.ToolEnded,
		harness.Text,
		harness.UsageReport,
		harness.TurnEnded,
	}, kinds(evs))
	assert.Equal(t, "OK\nSTEER-SEEN", evs[3].Text)
	assert.Equal(t, harness.TurnCompleted, evs[5].Outcome)
}

func TestARecordedAbortIsAnInterruptNotAFailure(t *testing.T) {
	t.Parallel()
	evs := decodeAll(t, testCodec(), nil, fixture(t, "abort_with_queued_steer.jsonl")...)
	require.Equal(t, []harness.EventKind{
		harness.TurnStarted,
		harness.ToolStarted,
		harness.ToolEnded,
		harness.UsageReport,
		harness.TurnEnded,
	}, kinds(evs))
	assert.Equal(t, harness.TurnInterrupted, evs[4].Outcome, "the aborted request's stopReason error must not win")
	assert.Empty(t, evs[4].Text)
}

const (
	agentStart = `{"type":"agent_start"}`
	settled    = `{"type":"agent_settled","aborted":false}`
	errorEnd   = `{"type":"message_end","message":{"role":"assistant","content":[],"stopReason":"error","errorMessage":"429 usage limit reached"}}`
	okEnd      = `{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"fine"}],"stopReason":"stop"}}`
)

func TestTurnOutcomes(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		lines    []string
		want     harness.TurnOutcome
		wantText string
	}{
		"a model error fails the turn": {
			[]string{agentStart, errorEnd, `{"type":"agent_end","willRetry":false}`, settled},
			harness.TurnFailed, "429 usage limit reached",
		},
		"a retry that succeeds clears the error": {
			[]string{agentStart, errorEnd, `{"type":"agent_end","willRetry":true}`, agentStart, okEnd, settled},
			harness.TurnCompleted, "",
		},
		"retries running out fail the turn": {
			[]string{agentStart, okEnd, `{"type":"auto_retry_end","success":false,"attempt":3,"finalError":"overloaded"}`, settled},
			harness.TurnFailed, "overloaded",
		},
		"an error without a message still fails the turn": {
			[]string{agentStart, `{"type":"message_end","message":{"role":"assistant","content":[],"stopReason":"error"}}`, settled},
			harness.TurnFailed, "the model request failed",
		},
		"aborted": {
			[]string{agentStart, errorEnd, `{"type":"agent_settled","aborted":true}`},
			harness.TurnInterrupted, "",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			evs := decodeAll(t, testCodec(), nil, tc.lines...)
			var starts int
			for _, e := range evs {
				if e.Kind == harness.TurnStarted {
					starts++
				}
			}
			assert.Equal(t, 1, starts, "a retry's second agent_start is the same turn")
			last := evs[len(evs)-1]
			require.Equal(t, harness.TurnEnded, last.Kind)
			assert.Equal(t, tc.want, last.Outcome)
			assert.Equal(t, tc.wantText, last.Text)
		})
	}
}

func TestASettleOutsideATurnIsIgnored(t *testing.T) {
	t.Parallel()
	assert.Empty(t, decodeAll(t, testCodec(), nil, settled))
}

func TestTurnsDontShareUsage(t *testing.T) {
	t.Parallel()
	end := `{"type":"message_end","message":{"role":"assistant","content":[],"stopReason":"stop","usage":{"input":10,"output":2,"cost":{"total":0.5}}}}`
	evs := decodeAll(t, testCodec(), nil, agentStart, end, settled, agentStart, end, settled)
	var usages []*harness.Usage
	for _, e := range evs {
		if e.Kind == harness.UsageReport {
			usages = append(usages, e.Usage)
		}
	}
	require.Len(t, usages, 2)
	assert.Equal(t, int64(10), usages[1].InputTokens)
	assert.InDelta(t, 0.5, usages[1].CostUSD, 1e-9)
}

func TestCompactionUsageIsCounted(t *testing.T) {
	t.Parallel()
	compaction := `{"type":"compaction_end","reason":"threshold","result":{"summary":"s","usage":{"input":100,"output":20,"cost":{"total":0.25}}},"aborted":false,"willRetry":false}`
	end := `{"type":"message_end","message":{"role":"assistant","content":[],"stopReason":"stop","usage":{"input":10,"output":2,"cost":{"total":0.5}}}}`
	t.Run("into the run's usage", func(t *testing.T) {
		t.Parallel()
		evs := decodeAll(t, testCodec(), nil, agentStart, end, compaction, settled)
		require.Equal(t, []harness.EventKind{harness.TurnStarted, harness.UsageReport, harness.TurnEnded}, kinds(evs))
		assert.Equal(t, int64(110), evs[1].Usage.InputTokens)
		assert.InDelta(t, 0.75, evs[1].Usage.CostUSD, 1e-9)
	})
	t.Run("on its own outside a run", func(t *testing.T) {
		t.Parallel()
		evs := decodeAll(t, testCodec(), nil, compaction)
		require.Equal(t, []harness.EventKind{harness.UsageReport}, kinds(evs))
		assert.Equal(t, int64(20), evs[0].Usage.OutputTokens)
		assert.True(t, evs[0].Usage.CostEstimated)
	})
}

// sent sends a message through c, returning the prompt's request id.
func sent(t *testing.T, c *codec) string {
	t.Helper()
	var buf bytes.Buffer
	require.NoError(t, c.Send(context.Background(), rpcdriver.NewWire(&buf), &harness.Message{Text: "hi"}, false))
	var cmd command
	require.NoError(t, json.Unmarshal(buf.Bytes(), &cmd))
	return cmd.ID
}

// messages is the text of every command written to buf.
func messages(t *testing.T, buf *bytes.Buffer) []string {
	t.Helper()
	var out []string
	dec := json.NewDecoder(bytes.NewReader(buf.Bytes()))
	for dec.More() {
		var cmd command
		require.NoError(t, dec.Decode(&cmd))
		out = append(out, cmd.Message)
	}
	return out
}

func lastCommand(t *testing.T, buf *bytes.Buffer) command {
	t.Helper()
	var cmd command
	dec := json.NewDecoder(bytes.NewReader(buf.Bytes()))
	for dec.More() {
		require.NoError(t, dec.Decode(&cmd))
	}
	return cmd
}

func response(id, body string) string {
	return `{"id":"` + id + `","type":"response","command":"prompt",` + body + `}`
}

func TestAPromptThatStartsNoRunStillEndsATurn(t *testing.T) {
	t.Parallel()
	t.Run("rejected while idle", func(t *testing.T) {
		t.Parallel()
		c := testCodec()
		evs := decodeAll(t, c, nil, response(sent(t, c), `"success":false,"error":"Authentication failed for \"openai\""`))
		require.Equal(t, []harness.EventKind{harness.TurnStarted, harness.TurnEnded}, kinds(evs))
		assert.Equal(t, harness.TurnFailed, evs[1].Outcome)
		assert.Contains(t, evs[1].Text, "Authentication failed")
	})
	t.Run("handled while idle", func(t *testing.T) {
		t.Parallel()
		c := testCodec()
		evs := decodeAll(t, c, nil, response(sent(t, c), `"success":true,"data":{"disposition":"handled"}`))
		require.Equal(t, []harness.EventKind{harness.TurnStarted, harness.TurnEnded}, kinds(evs))
		assert.Equal(t, harness.TurnCompleted, evs[1].Outcome)
	})
	t.Run("rejected mid-turn is sent again when compaction ends", func(t *testing.T) {
		t.Parallel()
		c := testCodec()
		var buf bytes.Buffer
		w := rpcdriver.NewWire(&buf)
		evs := decodeAll(t, c, w, agentStart, response(sent(t, c), `"success":false,"error":"Cannot submit a prompt while compaction is in progress"`))
		assert.Equal(t, []harness.EventKind{harness.TurnStarted}, kinds(evs), "the running turn is left alone")
		assert.Empty(t, buf.String(), "nothing is sent until compaction ends")

		decodeAll(t, c, w, `{"type":"compaction_end","reason":"threshold","aborted":false,"willRetry":false}`)
		var again command
		require.NoError(t, json.Unmarshal(buf.Bytes(), &again))
		assert.Equal(t, "hi", again.Message)
		assert.Equal(t, "steer", again.StreamingBehavior)

		buf.Reset()
		decodeAll(t, c, w, settled)
		assert.Empty(t, buf.String(), "sent once, not again at settle")
	})
	t.Run("rejected mid-turn with no compaction is sent at settle, in the same turn", func(t *testing.T) {
		t.Parallel()
		c := testCodec()
		var buf bytes.Buffer
		w := rpcdriver.NewWire(&buf)
		evs := decodeAll(t, c, w, agentStart, response(sent(t, c), `"success":false,"error":"busy"`), settled)
		assert.Equal(t, []harness.EventKind{harness.TurnStarted}, kinds(evs), "the turn end waits for the resent message's run, so Stop does too")
		again := lastCommand(t, &buf)
		assert.Equal(t, "hi", again.Message)

		evs = decodeAll(t, c, w, response(again.ID, `"success":true,"data":{"disposition":"started"}`), agentStart, okEnd, settled)
		assert.Equal(t, []harness.EventKind{harness.Text, harness.UsageReport, harness.TurnEnded}, kinds(evs))
		assert.Equal(t, harness.TurnCompleted, evs[2].Outcome)
	})
	t.Run("rejected again at settle ends the turn and waits for the next Send", func(t *testing.T) {
		t.Parallel()
		c := testCodec()
		var buf bytes.Buffer
		w := rpcdriver.NewWire(&buf)
		decodeAll(t, c, w, agentStart, response(sent(t, c), `"success":false,"error":"busy"`), settled)
		evs := decodeAll(t, c, w, response(lastCommand(t, &buf).ID, `"success":false,"error":"busy"`))
		require.Equal(t, []harness.EventKind{harness.UsageReport, harness.TurnEnded}, kinds(evs))
		assert.Equal(t, harness.TurnCompleted, evs[1].Outcome, "the run itself completed")

		buf.Reset()
		require.NoError(t, c.Send(context.Background(), w, &harness.Message{Text: "next"}, false))
		assert.Equal(t, []string{"hi", "next"}, messages(t, &buf), "the held message goes first")
	})
	t.Run("an interrupt doesn't restart the agent with a held message", func(t *testing.T) {
		t.Parallel()
		c := testCodec()
		var buf bytes.Buffer
		w := rpcdriver.NewWire(&buf)
		evs := decodeAll(t, c, w, agentStart, response(sent(t, c), `"success":false,"error":"busy"`),
			`{"type":"compaction_end","reason":"threshold","aborted":true}`, `{"type":"agent_settled","aborted":true}`)
		require.Equal(t, []harness.EventKind{harness.TurnStarted, harness.UsageReport, harness.TurnEnded}, kinds(evs))
		assert.Equal(t, harness.TurnInterrupted, evs[2].Outcome)
		assert.Empty(t, buf.String(), "nothing resent")

		require.NoError(t, c.Send(context.Background(), w, &harness.Message{Text: "next"}, false))
		assert.Equal(t, []string{"hi", "next"}, messages(t, &buf))
	})
	t.Run("rejected while compacting outside a run is held, not failed", func(t *testing.T) {
		t.Parallel()
		c := testCodec()
		var buf bytes.Buffer
		w := rpcdriver.NewWire(&buf)
		evs := decodeAll(t, c, w, `{"type":"compaction_start","reason":"threshold"}`,
			response(sent(t, c), `"success":false,"error":"Cannot submit a prompt while compaction is in progress"`))
		assert.Empty(t, evs)
		decodeAll(t, c, w, `{"type":"compaction_end","reason":"threshold","aborted":false}`)
		assert.Equal(t, []string{"hi"}, messages(t, &buf))
	})
	t.Run("started and queued are left to the run's own events", func(t *testing.T) {
		t.Parallel()
		c := testCodec()
		assert.Empty(t, decodeAll(t, c, nil, response(sent(t, c), `"success":true,"data":{"disposition":"started"}`)))
		assert.Empty(t, decodeAll(t, c, nil, response(sent(t, c), `"success":true,"data":{"disposition":"queued"}`)))
	})
	t.Run("another command's failure is not a turn", func(t *testing.T) {
		t.Parallel()
		assert.Empty(t, decodeAll(t, testCodec(), nil, `{"id":"slop-bot-9","type":"response","command":"abort","success":false,"error":"x"}`))
	})
}

func TestDialogsAreCancelledAndNotificationsIgnored(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	w := rpcdriver.NewWire(&buf)
	evs := decodeAll(t, testCodec(), w,
		`{"type":"extension_ui_request","id":"ui-1","method":"confirm","title":"Delete?"}`,
		`{"type":"extension_ui_request","id":"ui-2","method":"notify","message":"hello"}`,
	)
	assert.Empty(t, evs)
	var reply map[string]any
	dec := json.NewDecoder(&buf)
	require.NoError(t, dec.Decode(&reply))
	assert.Equal(t, map[string]any{"type": "extension_ui_response", "id": "ui-1", "cancelled": true}, reply)
	assert.False(t, dec.More(), "a notification needs no answer")
}

func TestSendAlwaysPromptsWithSteerAndInterruptAborts(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	w := rpcdriver.NewWire(&buf)
	c := testCodec()
	require.NoError(t, c.Send(context.Background(), w, &harness.Message{Text: "idle"}, false))
	require.NoError(t, c.Send(context.Background(), w, &harness.Message{Text: "mid"}, true))
	require.NoError(t, c.Interrupt(context.Background(), w))

	dec := json.NewDecoder(&buf)
	var idle, mid, abort command
	require.NoError(t, dec.Decode(&idle))
	require.NoError(t, dec.Decode(&mid))
	require.NoError(t, dec.Decode(&abort))
	for _, cmd := range []command{idle, mid} {
		assert.Equal(t, "prompt", cmd.Type)
		assert.Equal(t, "steer", cmd.StreamingBehavior, "pi starts a run when idle and steers when not")
	}
	assert.Equal(t, "mid", mid.Message)
	assert.Equal(t, "abort", abort.Type)
	assert.NotEqual(t, idle.ID, mid.ID, "each request has its own id")
}

// handshakeReply is the recorded get_state answer, re-addressed to Open's
// request.
func handshakeReply(t *testing.T) []byte {
	t.Helper()
	line := fixture(t, "turn_with_tool.jsonl")[0]
	return bytes.Replace([]byte(line), []byte(`"id":"hs"`), []byte(`"id":"`+handshakeID+`"`), 1)
}

func lines(ls ...[]byte) func(context.Context) ([]byte, error) {
	return func(context.Context) ([]byte, error) {
		if len(ls) == 0 {
			return nil, errors.New("the harness exited during the handshake")
		}
		l := ls[0]
		ls = ls[1:]
		return l, nil
	}
}

func TestOpen(t *testing.T) {
	t.Parallel()
	t.Run("get_state is the handshake", func(t *testing.T) {
		t.Parallel()
		var buf bytes.Buffer
		id, err := testCodec().Open(context.Background(), rpcdriver.NewWire(&buf),
			lines([]byte(`{"type":"queue_update","steering":[],"followUp":[]}`), handshakeReply(t)))
		require.NoError(t, err)
		assert.Equal(t, "probe-0001", id)
		var cmd command
		require.NoError(t, json.Unmarshal(buf.Bytes(), &cmd))
		assert.Equal(t, command{ID: handshakeID, Type: "get_state"}, cmd)
	})
	t.Run("a dialog before the answer is cancelled", func(t *testing.T) {
		t.Parallel()
		var buf bytes.Buffer
		_, err := testCodec().Open(context.Background(), rpcdriver.NewWire(&buf),
			lines([]byte(`{"type":"extension_ui_request","id":"ui-1","method":"select","title":"Pick"}`), handshakeReply(t)))
		require.NoError(t, err)
		dec := json.NewDecoder(&buf)
		var hs, reply map[string]any
		require.NoError(t, dec.Decode(&hs))
		require.NoError(t, dec.Decode(&reply))
		assert.Equal(t, map[string]any{"type": "extension_ui_response", "id": "ui-1", "cancelled": true}, reply)
	})
	t.Run("a different session is an error", func(t *testing.T) {
		t.Parallel()
		c := newCodec("other", false, slog.New(slog.DiscardHandler))
		_, err := c.Open(context.Background(), rpcdriver.NewWire(&bytes.Buffer{}), lines(handshakeReply(t)))
		assert.ErrorContains(t, err, `not "other"`)
	})
	t.Run("no model is an error", func(t *testing.T) {
		t.Parallel()
		reply := `{"id":"` + handshakeID + `","type":"response","command":"get_state","success":true,"data":{"sessionId":"probe-0001"}}`
		_, err := testCodec().Open(context.Background(), rpcdriver.NewWire(&bytes.Buffer{}), lines([]byte(reply)))
		assert.ErrorContains(t, err, "no model")
	})
	t.Run("a failed get_state is an error", func(t *testing.T) {
		t.Parallel()
		reply := `{"id":"` + handshakeID + `","type":"response","command":"get_state","success":false,"error":"boom"}`
		_, err := testCodec().Open(context.Background(), rpcdriver.NewWire(&bytes.Buffer{}), lines([]byte(reply)))
		assert.ErrorContains(t, err, "boom")
	})
	t.Run("an exit during the handshake is an error", func(t *testing.T) {
		t.Parallel()
		_, err := testCodec().Open(context.Background(), rpcdriver.NewWire(&bytes.Buffer{}), lines())
		assert.ErrorContains(t, err, "exited")
	})
}
