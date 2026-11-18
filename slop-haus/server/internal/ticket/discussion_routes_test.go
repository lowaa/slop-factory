package ticket_test

import (
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost/codehosttest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

func (r *discussionRecorder) told() []ticket.DiscussionEntry {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]ticket.DiscussionEntry(nil), r.entries...)
}

func TestDiscussionHook_AnEscalatedReleaseOpensARoundTheHookIsToldOf(t *testing.T) {
	t.Parallel()
	f := newCooldownFixture(t, testPolicy)
	hook := &discussionRecorder{}
	store := newStore(f.database)
	f.svc = ticket.NewService(store, slog.DiscardHandler, ticket.WithCooldownPolicy(testPolicy), ticket.WithClock(f.clock.now), ticket.WithDiscussionHook(hook))
	a := f.agent(t, "builder-1")
	made := f.held(t, a)
	past := time.Now().Add(-2 * time.Hour)
	for i := 1; i < testPolicy.EscalateAfter; i++ {
		f.releaseAt(t, a, past, ticket.ReleaseParams{Reason: "still not deployed"})
		f.sweepOnce(t)
		claimed, err := f.svc.Claim(f.ctx, a, nil)
		require.NoError(t, err)
		require.NotNil(t, claimed.Ticket)
	}
	assert.Empty(t, hook.told(), "no discussion yet")

	out := f.releaseAt(t, a, past, ticket.ReleaseParams{Reason: "prod still runs 3608e9a0"})
	require.True(t, out.Escalated)

	f.svc.WaitDiscussionHooks()
	assert.Equal(t, []ticket.DiscussionEntry{{TicketID: made.ID, Attempt: 1, Round: 1}}, hook.told())
}

func TestDiscussionHook_AMergeIntoTheBaseBranchOpensARoundTheHookIsToldOf(t *testing.T) {
	t.Parallel()
	f, _, _, _, id := mergedIntoBaseFixture(t)
	hook := &discussionRecorder{}
	svc := ticket.NewService(newStore(f.database), slog.DiscardHandler,
		ticket.WithCodeHosts(codehosttest.Registry(codehosttest.NewLinkHost(prHost))), ticket.WithDiscussionHook(hook))

	_, err := svc.MergedIntoBase(f.ctx, ticket.AttemptRef{TicketID: id, Number: 2}, sharedRef, ticket.Merger{UserID: &f.human})

	require.NoError(t, err)
	svc.WaitDiscussionHooks()
	assert.Equal(t, []ticket.DiscussionEntry{{TicketID: id, Attempt: 2, Round: 1}}, hook.told())
}

func TestDiscussionHook_APersonsMoveIntoDiscussionOpensARoundTheHookIsToldOf(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	hook := f.withDiscussionHook()
	created := f.ticket(t, "Rough idea", "server/")

	require.NoError(t, f.svc.InTransition(f.ctx, func(tx ticket.TransitionTx) error {
		_, err := ticket.MoveTicketTo(f.ctx, tx, created.ID, ticket.StatusDiscussion)
		return err
	}))

	f.svc.WaitDiscussionHooks()
	assert.Equal(t, []ticket.DiscussionEntry{{TicketID: created.ID, Attempt: 1, Round: 1}}, hook.told())
}
