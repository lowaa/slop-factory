package pi

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-bot/internal/harness"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/rpcdriver"
)

// brokenPipe fails every write, as a pipe to a pi that has exited does.
type brokenPipe struct{}

func (brokenPipe) Write([]byte) (int, error) { return 0, errors.New("broken pipe") }

// holding is a codec holding held, which pi refused while busy and an
// interrupt left for the next Send.
func holding(t *testing.T, held ...*harness.Message) *codec {
	t.Helper()
	c := testCodec()
	var buf bytes.Buffer
	w := rpcdriver.NewWire(&buf)
	decodeAll(t, c, w, agentStart)
	var ids []string
	for _, m := range held {
		require.NoError(t, c.Send(context.Background(), w, m, true))
		ids = append(ids, lastCommand(t, &buf).ID)
	}
	for _, id := range ids {
		decodeAll(t, c, w, response(id, `"success":false,"error":"busy"`))
	}
	decodeAll(t, c, w, `{"type":"agent_settled","aborted":true}`)
	require.Equal(t, held, c.Undelivered())
	return c
}

// A Send whose write fails sends nothing more: what it couldn't write stays
// held, Undelivered, and its own message is the caller's (the error).
func TestAFailedWriteKeepsTheHeldMessages(t *testing.T) {
	t.Parallel()
	held := []*harness.Message{{Text: "one", IDs: []int64{1}}, {Text: "two", IDs: []int64{2}}}
	c := holding(t, held...)

	err := c.Send(context.Background(), rpcdriver.NewWire(brokenPipe{}), &harness.Message{Text: "three", IDs: []int64{3}}, false)

	require.Error(t, err)
	assert.Equal(t, held, c.Undelivered(), "neither held message was written, so both are still held")
	assert.Empty(t, c.prompts, "no prompt waits for an answer that can't come")
}

// A held message resent at a settle, onto a pi that has gone, stays held:
// the core requeues it on the exit.
func TestAFailedResendKeepsTheHeldMessages(t *testing.T) {
	t.Parallel()
	c := testCodec()
	var buf bytes.Buffer
	w := rpcdriver.NewWire(&buf)
	held := &harness.Message{Text: "one", IDs: []int64{1}}
	require.NoError(t, c.Send(context.Background(), w, held, false))
	decodeAll(t, c, w, agentStart, response(lastCommand(t, &buf).ID, `"success":false,"error":"busy"`))

	evs := decodeAll(t, c, rpcdriver.NewWire(brokenPipe{}), settled)

	assert.Equal(t, harness.TurnEnded, evs[len(evs)-1].Kind, "nothing was resent, so the turn ends")
	assert.Equal(t, []*harness.Message{held}, c.Undelivered())
}

// An oversized record with no field whole before the cut says nothing.
func TestAnOversizedRecordWithNothingWholeSaysNothing(t *testing.T) {
	t.Parallel()
	evs, err := testCodec().Oversized([]byte(`{"type":"tool_executi`), nil)
	require.NoError(t, err)
	assert.Empty(t, evs)
}
