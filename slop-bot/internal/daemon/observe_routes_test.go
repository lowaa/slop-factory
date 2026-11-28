package daemon

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/lowaa/slop-factory/slop-bot/internal/agents"
	"github.com/lowaa/slop-factory/slop-bot/internal/ident"
	"github.com/lowaa/slop-factory/slop-bot/internal/state"
)

// An agent following the event log (as `inbox --follow` does) sees every
// event but other agents' conversations.
func TestAnAgentSeesOnlyItsOwnConversationEvents(t *testing.T) {
	t.Parallel()
	me := ident.AgentPrincipal("t1-aaaaaa", "1")
	other := &state.Event{Type: "harness.text", Agent: "t2-bbbbbb"}
	mine := &state.Event{Type: "harness.text", Agent: "t1-aaaaaa"}
	queued := &state.Event{Type: "message.queued", Agent: "t2-bbbbbb"}

	assert.False(t, visibleTo(me, other))
	assert.True(t, visibleTo(me, mine))
	assert.True(t, visibleTo(me, queued))
	assert.True(t, visibleTo(ident.UserPrincipal(), other), "the user sees everything")
}

// slop-bot releasing an agent it gave up on (a failed start, a crash loop)
// writes no agent.state, only agent.released: a follower must end on it,
// in exactly the failure a user is likely to be watching.
func TestAReleaseEndsAWatch(t *testing.T) {
	t.Parallel()
	released := &state.Event{Type: agents.EventReleased, Agent: "t1-aaaaaa", Payload: []byte(`{"reason":"its harness died 4 times in a row"}`)}
	assert.True(t, endsAgent(released))
	assert.True(t, watched(released), "watch shows why it ended")
}
