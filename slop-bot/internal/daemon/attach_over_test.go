package daemon

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/lowaa/slop-factory/slop-bot/internal/agents"
	"github.com/lowaa/slop-factory/slop-bot/internal/state"
)

// A failed lease renewal ends the attach, killing the person's TUI, only
// when the attach really is over; a passing failure is retried.
func TestOnlyAnEndedAttachStopsTheHeartbeat(t *testing.T) {
	t.Parallel()
	assert.True(t, attachOver(fmt.Errorf("%w: %w", agents.ErrNotAttached, state.ErrConflict)))
	assert.True(t, attachOver(state.ErrEnded))
	assert.True(t, attachOver(state.ErrNotFound))
	assert.False(t, attachOver(errors.New("database is locked")))
}
