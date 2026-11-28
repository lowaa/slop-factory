package tuidriver

import "context"

// ErrExited, Ended and Stopped expose how a wait that ended with the
// conversation's background context tells an exit from a Close: the race
// between the two channels being ready can't be forced from outside.
var (
	ErrExited = errExited
	Stopped   = stopped
)

// ConversationTag is the session tag the conversation id is recorded in.
const ConversationTag = conversationTag

func (c *Conversation) Ended() error { return c.ended() }

// Hook delivers one hook invocation as the hook hub would, for a test to
// land one at a moment the hub never would (after Close).
func (c *Conversation) Hook(ctx context.Context, event string, payload []byte) ([]byte, error) {
	return c.hook(ctx, event, payload)
}
