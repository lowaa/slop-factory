package mcpserver_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/agentmsg"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

const humanBanner = "MESSAGE FROM THE HUMAN:"

// say is the human messaging the rig's agent.
func (r *realRig) say(body string) {
	r.t.Helper()
	_, err := r.messages.Send(r.ctx, &authn.User{ID: r.human}, r.agentID, body)
	require.NoError(r.t, err)
}

// holds has the agent claim a ticket over MCP.
func (r *realRig) holds(title string) *ticket.Ticket {
	r.t.Helper()
	held := r.openTicket(title)
	claimed := r.call("ticket_next", map[string]any{"ticket_id": held.ID})
	require.False(r.t, claimed.IsError, resultText(r.t, claimed))
	return held
}

// leading is the first text block of a result: where a notice rides.
func leading(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	require.NotEmpty(t, res.Content)
	tc, ok := res.Content[0].(*mcp.TextContent)
	require.True(t, ok)
	return tc.Text
}

// The message reaches an agent mid-ticket on its next call, whichever tool
// that is, leading the result and once.
func TestMessages_ReachTheAgentOnItsNextCallOfAnyTool(t *testing.T) {
	for name, tc := range map[string]struct {
		tool string
		args func(r *realRig, held *ticket.Ticket) map[string]any
	}{
		"checkin":    {tool: "checkin"},
		"ticket_get": {tool: "ticket_get", args: func(_ *realRig, held *ticket.Ticket) map[string]any { return map[string]any{"ticket_id": held.ID} }},
		"ticket_comment": {tool: "ticket_comment", args: func(_ *realRig, held *ticket.Ticket) map[string]any {
			return map[string]any{"ticket_id": held.ID, "body": "progress"}
		}},
		"ticket_search": {tool: "ticket_search", args: func(*realRig, *ticket.Ticket) map[string]any { return map[string]any{"text": "work"} }},
	} {
		t.Run(name, func(t *testing.T) {
			r := newRealRig(t)
			held := r.holds("Being worked")
			r.say("stop, wrong approach")
			r.say("also handle the edge case")
			var args map[string]any
			if tc.args != nil {
				args = tc.args(r, held)
			}

			res := r.call(tc.tool, args)
			require.False(t, res.IsError, resultText(t, res))
			first := leading(t, res)
			assert.Contains(t, first, humanBanner)
			assert.Contains(t, first, "stop, wrong approach")
			assert.Contains(t, first, "also handle the edge case")
			assert.Less(t, strings.Index(first, "stop, wrong"), strings.Index(first, "also handle"), "oldest first, together")
			assert.Equal(t, 2, strings.Count(resultText(t, res), humanBanner), "one frame per message, once")

			again := r.call(tc.tool, args)
			assert.NotContains(t, resultText(t, again), humanBanner, "delivered exactly once")
		})
	}
}

// The message is taken by the call and never lost to its failing.
func TestMessages_AFailingCallStillDeliversThem(t *testing.T) {
	r := newRealRig(t)
	r.say("hold your merge")

	res := r.call("ticket_get", map[string]any{"ticket_id": 987654})
	assert.True(t, res.IsError)
	text := resultText(t, res)
	assert.Contains(t, text, humanBanner)
	assert.Contains(t, text, "hold your merge")
	assert.Contains(t, text, "Separately, this call failed")

	assert.NotContains(t, resultText(t, r.call("checkin", nil)), humanBanner)
}

// The message and a withdrawn ticket's notice both reach the agent, in
// separate blocks, on the call the withdrawal fails.
func TestMessages_RideAlongsideARevocationNotice(t *testing.T) {
	r := newRealRig(t)
	held := r.claimThenRevoke()
	r.say("and tell me what you found")

	res := r.call("ticket_comment", map[string]any{"ticket_id": held.ID, "body": "Nearly there."})
	assert.True(t, res.IsError)
	text := resultText(t, res)
	assert.Contains(t, text, humanBanner)
	assert.Contains(t, text, "and tell me what you found")
	assert.Contains(t, text, revokedNotice)
	assert.Less(t, strings.Index(text, humanBanner), strings.Index(text, revokedNotice))

	assert.NotContains(t, resultText(t, r.call("checkin", nil)), humanBanner)
}

// On a call that goes through, the human's words and the revocation notice
// are each a block of their own, the human's first: they are never run
// together into one string the model might read as a single thing.
func TestMessages_AndANoticeAreSeparateBlocksAheadOfTheResult(t *testing.T) {
	r := newRealRig(t)
	r.claimThenRevoke()
	r.say("and tell me what you found")

	res := r.call("checkin", nil)
	require.False(t, res.IsError, resultText(t, res))
	require.GreaterOrEqual(t, len(res.Content), 3)
	first, second := leading(t, res), res.Content[1].(*mcp.TextContent).Text
	assert.Contains(t, first, humanBanner)
	assert.NotContains(t, first, revokedNotice)
	assert.Contains(t, second, revokedNotice)
	assert.NotContains(t, second, humanBanner)
}

func TestMessages_AHumanCommentOnTheHeldTicketReachesTheHolder(t *testing.T) {
	r := newRealRig(t)
	held := r.holds("Being worked")

	// The agent's own comment, through its own tool, does not come back.
	own := r.call("ticket_comment", map[string]any{"ticket_id": held.ID, "body": "my own progress note"})
	require.False(t, own.IsError, resultText(t, own))
	assert.NotContains(t, resultText(t, r.call("checkin", nil)), "my own progress note")

	_, err := r.tickets.AddComment(r.ctx, held.ID, ticket.UserAuthor(r.human), "please also update the docs")
	require.NoError(t, err)

	res := r.call("checkin", nil)
	first := leading(t, res)
	assert.Contains(t, first, humanBanner)
	assert.Contains(t, first, "please also update the docs")
	assert.Contains(t, first, fmt.Sprintf("ticket #%d", held.ID))
	assert.NotContains(t, first, "my own progress note")

	assert.NotContains(t, resultText(t, r.call("checkin", nil)), humanBanner)
}

func TestMessages_AHumanCommentOnATicketNobodyHoldsGoesNowhere(t *testing.T) {
	r := newRealRig(t)
	other := r.openTicket("Nobody's")
	_, err := r.tickets.AddComment(r.ctx, other.ID, ticket.UserAuthor(r.human), "a note for whoever picks it up")
	require.NoError(t, err)

	assert.NotContains(t, resultText(t, r.call("checkin", nil)), humanBanner)
}

// Two calls at once split the pending messages between them; none is
// delivered to both, none is lost.
func TestMessages_ConcurrentCallsDeliverEachMessageOnce(t *testing.T) {
	r := newRealRig(t)
	const total = 12
	for i := range total {
		r.say("message-" + string(rune('a'+i)))
	}

	const callers = 6
	start := make(chan struct{})
	texts := make([]string, callers)
	var wg sync.WaitGroup
	for i := range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			res, err := r.session.CallTool(context.Background(), &mcp.CallToolParams{Name: "checkin"})
			if assert.NoError(t, err) {
				texts[i] = resultText(t, res)
			}
		}()
	}
	close(start)
	wg.Wait()

	for i := range total {
		marker := "message-" + string(rune('a'+i))
		n := 0
		for _, text := range texts {
			n += strings.Count(text, marker)
		}
		assert.Equal(t, 1, n, "%s was delivered %d times", marker, n)
	}
}

// A person can't be addressed through the agent's own tools: there is no
// tool that sends, so nothing an agent calls can put words in a peer's
// context.
func TestMessages_NoToolSendsOne(t *testing.T) {
	r := newRealRig(t)
	tools, err := r.session.ListTools(context.Background(), nil)
	require.NoError(t, err)
	for _, tool := range tools.Tools {
		assert.NotContains(t, strings.ToLower(tool.Name), "message", tool.Name)
		assert.NotContains(t, strings.ToLower(tool.Name), "notify", tool.Name)
	}
}

type fakeMessages struct {
	mu       sync.Mutex
	messages []agentmsg.Message
	// err is returned by every delivery; failOnce by the first only.
	err       error
	failOnce  error
	delivered int
	// withholdTo is the delivery at which the messages first appear (0 or 1:
	// the first), standing in for one that arrives during a wait.
	withholdTo int
}

func (f *fakeMessages) Deliver(context.Context, uuid.UUID) ([]agentmsg.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.delivered++
	if f.failOnce != nil {
		err := f.failOnce
		f.failOnce = nil
		return nil, err
	}
	if f.err != nil {
		return nil, f.err
	}
	if f.delivered < f.withholdTo {
		return nil, nil
	}
	out := f.messages
	f.messages = nil
	return out, nil
}

// Delivery trouble must never cost the agent its call: the messages stay
// pending (the service did not mark them), so the next call tries again.
func TestMessages_ADeliveryFailureNeverFailsTheCall(t *testing.T) {
	fake := &fakeMessages{
		failOnce: errors.New("database unreachable"),
		messages: []agentmsg.Message{{Kind: agentmsg.KindMessage, Body: "still coming"}},
	}
	r := newRigWith(t, &rigOptions{messages: fake})

	res, err := r.session.CallTool(context.Background(), &mcp.CallToolParams{Name: "checkin"})
	require.NoError(t, err)
	assert.False(t, res.IsError, resultText(t, res))
	assert.NotContains(t, resultText(t, res), humanBanner)
	assert.Equal(t, 1, fake.delivered)

	next, err := r.session.CallTool(context.Background(), &mcp.CallToolParams{Name: "checkin"})
	require.NoError(t, err)
	assert.Contains(t, resultText(t, next), "still coming", "the next call tries again and delivers it")
}

// A read-only credential observes; it does not use up what the working run
// is to be told.
func TestMessages_AReadOnlyCallerDoesNotTakeThem(t *testing.T) {
	fake := &fakeMessages{messages: []agentmsg.Message{{Kind: agentmsg.KindMessage, Body: "for the worker"}}}
	r := newRigWith(t, &rigOptions{messages: fake, readOnly: true})

	res, err := r.session.CallTool(context.Background(), &mcp.CallToolParams{Name: "ticket_get", Arguments: map[string]any{"ticket_id": 7}})
	require.NoError(t, err)
	assert.NotContains(t, resultText(t, res), "for the worker")
	assert.Zero(t, fake.delivered)
}
