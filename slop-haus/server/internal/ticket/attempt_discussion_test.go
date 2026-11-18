package ticket_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

// needs_discussion binds each attempt until that attempt has asked its own
// question: the first to discuss must not release a sibling that has not
// planned yet.
func TestNeedsDiscussion_ClearsOnlyOnceEveryLiveAttemptHasDiscussed(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a1, a2 := f.agent(t, "first"), f.agent(t, "second")
	created, err := f.svc.Create(f.ctx, &ticket.CreateParams{
		Title: "Rough idea", NeedsDiscussion: true, Paths: []model.Path{{Path: "server/"}},
	})
	require.NoError(t, err)
	require.Equal(t, 2, f.addAttempt(t, created.ID, false))

	require.True(t, f.claim(t, a1).NeedsDiscussion)
	f.discuss(t, a1)

	assert.True(t, f.get(t, created.ID).NeedsDiscussion, "attempt 2 has not discussed, so the ticket still needs it")

	claimed := f.claim(t, a2)
	require.NotNil(t, claimed)
	assert.True(t, claimed.NeedsDiscussion, "attempt 2 is told to plan at its claim")

	_, err = f.svc.Done(f.ctx, a2, &ticket.DoneParams{Summary: "built it anyway", PRURL: reviewPR})
	assert.ErrorIs(t, err, ticket.ErrNeedsDiscussion, "done")
	_, err = f.svc.Review(f.ctx, a2, &ticket.ReviewParams{Reason: "reviewed by a person", PRURL: reviewPR, Handoff: "PR open"})
	assert.ErrorIs(t, err, ticket.ErrNeedsDiscussion, "review")
	gate, err := f.svc.ChangeGateFor(f.ctx, a2)
	require.NoError(t, err)
	assert.True(t, gate.Ticket.NeedsDiscussion, "the merge gate reads the flag for attempt 2")

	detail, err := f.svc.DetailFor(f.ctx, created.ID, a2)
	require.NoError(t, err)
	assert.True(t, detail.Ticket.NeedsDiscussion, "ticket_get tells attempt 2 too")

	f.discuss(t, a2)
	assert.False(t, f.get(t, created.ID).NeedsDiscussion, "the last live attempt's discussion clears it")
}

func TestNeedsDiscussion_AnAttemptThatDiscussedIsClaimedForWork(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a1, a2 := f.agent(t, "first"), f.agent(t, "second")
	created, err := f.svc.Create(f.ctx, &ticket.CreateParams{
		Title: "Rough idea", NeedsDiscussion: true, Paths: []model.Path{{Path: "server/"}},
	})
	require.NoError(t, err)
	require.Equal(t, 2, f.addAttempt(t, created.ID, false))

	f.claim(t, a1)
	f.discuss(t, a1)
	require.True(t, f.claim(t, a2).NeedsDiscussion)
	f.humanReplyTo(t, created.ID, 1)
	f.closeAffinity(t, created.ID)

	back := f.claim(t, a1)
	require.NotNil(t, back)
	require.Equal(t, 1, f.leasedAttempt(t, a1))
	assert.False(t, back.NeedsDiscussion, "attempt 1's claim after its answer is a working claim")
	gate, err := f.svc.ChangeGateFor(f.ctx, a1)
	require.NoError(t, err)
	assert.False(t, gate.Ticket.NeedsDiscussion)
	gate, err = f.svc.ChangeGateFor(f.ctx, a2)
	require.NoError(t, err)
	assert.True(t, gate.Ticket.NeedsDiscussion, "while attempt 2 is still held to its plan")
}

func TestNeedsDiscussion_SettingItAgainStartsEveryAttemptsDiscussionOver(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a1 := f.agent(t, "first")
	created, err := f.svc.Create(f.ctx, &ticket.CreateParams{
		Title: "Rough idea", NeedsDiscussion: true, Paths: []model.Path{{Path: "server/"}},
	})
	require.NoError(t, err)
	require.Equal(t, 2, f.addAttempt(t, created.ID, false))
	f.claim(t, a1)
	f.discuss(t, a1)

	_, err = f.svc.Update(f.ctx, created.ID, &ticket.UpdateParams{NeedsDiscussion: ptr(true)})
	require.NoError(t, err)

	var discussed int
	require.NoError(t, f.database.QueryRow(f.ctx,
		`SELECT count(*) FROM attempts WHERE ticket_id = $1 AND discussed_at IS NOT NULL`, created.ID).Scan(&discussed))
	assert.Zero(t, discussed)
}

func TestNeedsDiscussion_AFinishedAttemptDoesNotHoldTheFlag(t *testing.T) {
	t.Parallel()
	for _, finished := range []string{"closed", "done"} {
		t.Run(finished, func(t *testing.T) {
			f := newFixture(t)
			a1 := f.agent(t, "first")
			created, err := f.svc.Create(f.ctx, &ticket.CreateParams{
				Title: "Rough idea", NeedsDiscussion: true, Paths: []model.Path{{Path: "server/"}},
			})
			require.NoError(t, err)
			n := f.addAttempt(t, created.ID, false)
			f.setAttemptStatus(t, created.ID, n, finished)

			f.claim(t, a1)
			f.discuss(t, a1)

			assert.False(t, f.get(t, created.ID).NeedsDiscussion)
		})
	}
}
