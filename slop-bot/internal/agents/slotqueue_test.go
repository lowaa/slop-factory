package agents_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-bot/internal/agents"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/fake"
	"github.com/lowaa/slop-factory/slop-bot/internal/ident"
	"github.com/lowaa/slop-factory/slop-bot/internal/state"
)

// Tests of the slot queue's order in state.db: agents waiting for a slot
// are served in the order they started waiting, across a restart too.

func (f *fixture) queuedTickets(pool state.Pool) []ident.TicketID {
	f.t.Helper()
	list, err := f.state.Queue.List(context.Background(), pool)
	require.NoError(f.t, err)
	out := make([]ident.TicketID, len(list))
	for i, q := range list {
		out[i] = q.Ticket
	}
	return out
}

func TestAnAgentWaitingForASlotIsQueuedInStateDB(t *testing.T) {
	t.Parallel()
	f := newFixture(t, oneSlot)
	_, a1 := f.waitingAgent("first", nil)
	s2, _, a2 := f.startedAgent("second", nil)
	f.do(s2, fake.Command{Cmd: fake.CmdTurnStart}) // the second holds the slot

	f.tell(a1.Agent, "go ahead")
	assert.Equal(t, []ident.TicketID{a1.Ticket}, f.queuedTickets(state.PoolNormal))

	f.ask(a2) // so its turn's end frees the slot without a nudge to queue
	f.do(s2, fake.Command{Cmd: fake.CmdTurnEnd})
	f.await(agents.EventDelivered, of(a1.Agent))
	assert.Empty(t, f.queuedTickets(state.PoolNormal), "served, it leaves the queue")
}

// Two answered agents waiting for the one slot when the daemon stopped are
// served in the order they queued, not the order their resumes finish: the
// later one is let resume first, and still waits.
func TestARestartServesTheSlotQueueInItsOrder(t *testing.T) {
	t.Parallel()
	f := newFixture(t, oneSlot)
	_, a1 := f.waitingAgent("first", nil)
	_, a2 := f.waitingAgent("second", nil)

	release := make(chan struct{})
	f.restartWith(func() {
		ctx := context.Background()
		// a2 was answered, and queued, first.
		for _, a := range []ident.Principal{a2, a1} {
			_, _, err := f.state.Messages.Answer(ctx, &state.NewMessage{From: state.UserParty, To: state.AgentParty(a.Agent), Body: "answer to " + string(a.Agent)})
			require.NoError(t, err)
			require.NoError(t, f.state.Queue.PushFront(ctx, state.PoolNormal, a.Ticket))
		}
		latest, err := f.state.Events.Latest(ctx)
		require.NoError(t, err)
		f.cursor = latest
		// a2's resume waits until a1's has settled where it stands.
		f.hooks.set(func(h *harnessHooks) {
			h.start = func(ctx context.Context, ws *harness.Workspace, resume *harness.ConversationRef) error {
				if resume == nil || ws.Agent != a2.Agent {
					return nil
				}
				select {
				case <-release:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			}
		})
	})
	f.session() // a1's
	first := f.await(agents.EventState, func(e *state.Event) bool {
		return e.Agent == a1.Agent && withReason(state.StateIdle, "a message waits for a slot")(e)
	})
	require.NotNil(t, first, "a1 waits behind a2, though a2 isn't running yet")
	close(release)
	s2 := f.session()
	got := f.user(s2)
	assert.Contains(t, got.Text, "answer to "+string(a2.Agent))
	assert.Equal(t, 1, f.pending(a1.Agent), "a1 still waits for the slot")
}
