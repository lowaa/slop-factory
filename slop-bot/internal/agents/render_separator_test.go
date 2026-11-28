package agents_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/lowaa/slop-factory/slop-bot/internal/agents"
	"github.com/lowaa/slop-factory/slop-bot/internal/state"
)

// Messages are separated by one blank line, and the first has nothing
// before it.
func TestRenderSeparatesMessagesWithABlankLineAndNotTheFirst(t *testing.T) {
	t.Parallel()
	sys := func(body string) state.Message { return state.Message{Kind: state.KindSystem, Body: body} }
	assert.Equal(t, "[slop-bot] a", agents.Render([]state.Message{sys("a")}))
	assert.Equal(t, "[slop-bot] a\n\n[slop-bot] b", agents.Render([]state.Message{sys("a"), sys("b")}))
}
