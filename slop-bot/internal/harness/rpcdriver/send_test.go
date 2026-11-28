package rpcdriver

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-clock"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness"
)

var errSend = errors.New("the harness's stdin is gone")

// failSendCodec is readyCodec whose Send always fails.
type failSendCodec struct{ readyCodec }

func (failSendCodec) Send(context.Context, *Wire, *harness.Message, bool) error { return errSend }

func (c *Conversation) turnOn() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.inTurn
}

// A send that fails while idle takes back the turn it opened, so a Stop
// doesn't wait for a turn the harness never heard of; one that fails mid-turn
// leaves the running turn alone.
func TestAFailedSendUndoesOnlyTheTurnItOpened(t *testing.T) {
	t.Parallel()
	for _, mid := range []bool{false, true} {
		c := startScript(t, `echo ready; read x`, clock.NewFake(time.Unix(0, 0)))
		c.opts.Codec = failSendCodec{}
		if mid {
			c.setTurn(true)
		}

		err := c.Send(context.Background(), &harness.Message{Text: "hi"})

		require.ErrorIs(t, err, errSend, "mid-turn %v", mid)
		assert.Equal(t, mid, c.turnOn(), "mid-turn %v", mid)
	}
}
