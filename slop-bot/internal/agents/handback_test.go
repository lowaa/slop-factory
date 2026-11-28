package agents_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-bot/internal/agents"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness"
	"github.com/lowaa/slop-factory/slop-bot/internal/state"
	"github.com/lowaa/slop-factory/slop-bot/internal/tickets"
)

// blockResumes makes every resume wait in the harness's start: entered is
// closed once one has begun, and it returns once release is closed or its
// context ends, whichever is first, closing cancelled for the latter.
func (f *fixture) blockResumes() (entered, release, cancelled chan struct{}) {
	entered, release, cancelled = make(chan struct{}), make(chan struct{}), make(chan struct{})
	f.hooks.set(func(h *harnessHooks) {
		h.start = func(ctx context.Context, _ *harness.Workspace, resume *harness.ConversationRef) error {
			if resume == nil {
				return nil
			}
			close(entered)
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				close(cancelled)
				return ctx.Err()
			}
		}
	})
	return entered, release, cancelled
}

// A ticket cancelled while its agent is being handed back ends the agent:
// the conversation the hand-back started is stopped, not adopted for an
// agent that has ended.
func TestATicketCancelledDuringAHandBackLeavesNothingRunning(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "")
	s, a := f.working("t")
	f.attached(s, a, &agents.AttachRequest{})
	entered, release, _ := f.blockResumes()
	out := make(chan agents.HandBackOutcome, 1)
	go func() {
		o, err := f.mgr.HandBack(f.ctx(), a.ID, holder, "the person quit the TUI")
		assert.NoError(t, err)
		out <- o
	}()
	<-entered
	f.setStatus(a.Ticket, tickets.StatusCancelled)
	f.await(agents.EventFinished, func(e *state.Event) bool { return e.Agent == a.ID })
	close(release)
	resumed := f.session()
	assert.Equal(t, agents.Ended, <-out)
	_, err := resumed.Await(f.ctx(), "never sent")
	assert.ErrorContains(t, err, "session ended", "the resumed conversation was stopped")
	f.noLiveAgent(a.Ticket)
}

// A hand-back's resume is a start: Stop cancels a hung one and waits for
// it, rather than returning while it can still adopt a conversation.
func TestStopCancelsAHungHandBackAndWaitsForIt(t *testing.T) {
	t.Parallel()
	f := newFixture(t, "")
	s, a := f.working("t")
	f.attached(s, a, &agents.AttachRequest{})
	entered, _, cancelled := f.blockResumes()
	out := make(chan error, 1)
	go func() {
		_, err := f.mgr.HandBack(context.Background(), a.ID, holder, "the person quit the TUI")
		out <- err
	}()
	<-entered
	require.NoError(t, f.mgr.Stop(f.ctx()))
	select {
	case <-cancelled:
	default:
		t.Fatal("Stop returned before the hand-back's start was cancelled")
	}
	assert.Error(t, <-out)
}
