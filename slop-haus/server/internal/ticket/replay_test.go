package ticket_test

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"github.com/lowaa/slop-factory/go-auth/authn/authntest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"

	"github.com/google/uuid"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// doneReplayable is a ticket claimed, pinned and finished by its original
// attempt, then edited: what a replay must not be shown.
func (r *pinRig) doneReplayable(t *testing.T) *ticket.Ticket {
	t.Helper()
	original := r.agent(t, "original")
	tk := r.createWithBody(t, "Original title", "Original body", "a/")
	require.NotNil(t, r.claim(t, original))
	_, err := r.svc.Done(r.ctx, original, &ticket.DoneParams{Summary: "Done."})
	require.NoError(t, err)
	r.edit(t, tk.ID, "Hindsight title", "Hindsight body")
	require.Equal(t, ticket.StatusDone, r.status(t, tk.ID))
	return tk
}

func (r *pinRig) addReplay(t *testing.T, id int64) *ticket.AttemptAdded {
	t.Helper()
	added, err := r.svc.AddReplays(r.ctx, r.human, id, []*uuid.UUID{nil})
	require.NoError(t, err)
	require.Len(t, added, 1)
	return added[0]
}

func TestReplay_BranchesAtTheOriginalBaseSeesTheOriginalTextAndLeavesTheTicketDone(t *testing.T) {
	t.Parallel()
	r := newPinRig(t)
	tk := r.doneReplayable(t)
	added := r.addReplay(t, tk.ID)
	assert.True(t, added.Shadow)
	assert.True(t, added.Replay)
	assert.Equal(t, ticket.StatusDone, r.status(t, tk.ID), "a replay never counts toward the ticket's status")

	res := r.claimResult(t, r.agent(t, "replayer"))

	assert.Equal(t, "Original title", res.Ticket.Title, "the frozen view, not the live ticket")
	assert.Equal(t, "Original body", res.Ticket.Body)
	require.NotNil(t, res.Branch)
	assert.True(t, res.Branch.Ready)
	assert.True(t, res.Branch.Unpromoted)
	assert.Equal(t, headOne, r.host.BranchAt[r.branchKey(ticket.BranchName(tk.ID, added.Number))], "at the original base commit")
	assert.Equal(t, headOne, r.host.BranchAt[r.branchKey(ticket.UnpromotedBase(tk.ID))])
	assert.Equal(t, ticket.StatusDone, r.status(t, tk.ID))

	guidance := ticket.GuidanceFor(res.Ticket, res.Branch)
	assert.Equal(t, ticket.ReplayNote, guidance.DoneThroughOther)
	assert.Contains(t, guidance.DoneThroughOther, "Do not deploy, push to main or touch any live system")
}

func TestReplay_TheBaseBranchIsMadeAgainWhenItWasDeleted(t *testing.T) {
	t.Parallel()
	r := newPinRig(t)
	tk := r.doneReplayable(t)
	added := r.addReplay(t, tk.ID)
	replayer := r.agent(t, "replayer")
	r.claimResult(t, replayer)
	base := r.branchKey(ticket.UnpromotedBase(tk.ID))
	require.Contains(t, r.host.BranchAt, base)

	// The closing rule deleted it while the replay waited on a discussion.
	r.release(t, replayer)
	delete(r.host.BranchAt, base)
	next := r.agent(t, "next")
	res := r.claimResult(t, next)

	assert.True(t, res.Branch.Ready)
	assert.Equal(t, headOne, r.host.BranchAt[base])
	assert.Equal(t, added.Number, r.leasedAttempt(t, next))
}

func TestReplay_IsRefusedPromotion(t *testing.T) {
	t.Parallel()
	r := newPinRig(t)
	tk := r.doneReplayable(t)
	added := r.addReplay(t, tk.ID)

	_, err := r.svc.PromoteAttempt(r.ctx, r.human, tk.ID, added.Number)

	require.ErrorIs(t, err, ticket.ErrReplayNotPromotable)
	assert.Equal(t, 1, r.promotedOf(t, tk.ID), "the original stays promoted")
}

func TestReplay_RefusedWhereItHasNothingToReplay(t *testing.T) {
	t.Parallel()
	t.Run("a ticket that is not done", func(t *testing.T) {
		r := newPinRig(t)
		tk := r.ticket(t, "Live", "a/")
		require.NotNil(t, r.claim(t, r.agent(t, "worker")))

		_, err := r.svc.AddReplays(r.ctx, r.human, tk.ID, []*uuid.UUID{nil})

		require.ErrorIs(t, err, ticket.ErrReplayNotDone)
		assert.Len(t, r.attemptRows(t, tk.ID), 1)
	})
	t.Run("a done ticket with no base commit", func(t *testing.T) {
		r := newPinRig(t)
		tk := r.doneReplayable(t)
		_, err := r.database.Exec(r.ctx, `UPDATE tickets SET base_commit = NULL WHERE id = $1`, tk.ID)
		require.NoError(t, err)

		_, err = r.svc.AddReplays(r.ctx, r.human, tk.ID, []*uuid.UUID{nil})

		require.ErrorIs(t, err, ticket.ErrNoBase)
		assert.Len(t, r.attemptRows(t, tk.ID), 1)
	})
	t.Run("a done ticket with no snapshot", func(t *testing.T) {
		r := newPinRig(t)
		tk := r.doneReplayable(t)
		_, err := r.database.Exec(r.ctx, `DELETE FROM ticket_snapshots WHERE ticket_id = $1`, tk.ID)
		require.NoError(t, err)

		_, err = r.svc.AddReplays(r.ctx, r.human, tk.ID, []*uuid.UUID{nil})

		require.ErrorIs(t, err, ticket.ErrNoSnapshot)
		assert.Len(t, r.attemptRows(t, tk.ID), 1)
	})
	t.Run("an ordinary attempt on a done ticket stays refused", func(t *testing.T) {
		r := newPinRig(t)
		tk := r.doneReplayable(t)

		_, err := r.svc.AddAttempt(r.ctx, r.human, tk.ID, nil)

		require.ErrorIs(t, err, ticket.ErrNoAttemptsOnStatus)
	})
}

func TestReplay_TheCapCountsLiveAttemptsOnly(t *testing.T) {
	t.Parallel()
	r := newPinRig(t)
	tk := r.doneReplayable(t)
	for i := 0; i < ticket.MaxLiveAttempts; i++ {
		r.addReplay(t, tk.ID)
	}

	_, err := r.svc.AddReplays(r.ctx, r.human, tk.ID, []*uuid.UUID{nil})

	require.ErrorIs(t, err, ticket.ErrAttemptCap, "the finished original did not count, the five live replays do")
}

func (r *pinRig) replayMux(replays bool) http.Handler {
	mux := http.NewServeMux()
	api := humago.New(mux, huma.DefaultConfig("test", "0.0.0"))
	api.UseMiddleware(authn.UserMiddleware(api, func(_ context.Context, _ string) (*authn.User, error) {
		return &authn.User{ID: r.human, TenantID: r.tenant}, nil
	}, authntest.Guard(), slog.New(slog.DiscardHandler)))
	ticket.RegisterRoutes(api, ticket.NewHandler(r.svc, slog.DiscardHandler, ticket.WithAttemptsAdd(true), ticket.WithReplays(replays)))
	return mux
}

func TestReplay_HandlerIsGatedAndReportsTheReplayFlag(t *testing.T) {
	t.Parallel()
	r := newPinRig(t)
	tk := r.doneReplayable(t)
	path := fmt.Sprintf("/api/tickets/%d/attempts", tk.ID)
	body := `{"replay": true}`

	off := send(t, r.replayMux(false), http.MethodPost, path, body)
	assert.Equal(t, http.StatusNotFound, off.Code, off.Body.String())
	assert.Equal(t, ticket.FeatureDisabledCode, errorCode(t, off.Body.Bytes()))
	assert.Len(t, r.attemptRows(t, tk.ID), 1)

	plain := send(t, r.replayMux(true), http.MethodPost, path, `{}`)
	assert.Equal(t, http.StatusConflict, plain.Code, "an ordinary add to a done ticket stays refused: %s", plain.Body.String())

	on := send(t, r.replayMux(true), http.MethodPost, path, body)
	require.Equal(t, http.StatusCreated, on.Code, on.Body.String())
	got := decodeTicket(t, on.Body.Bytes()).Attempts
	require.Len(t, got, 2)
	assert.False(t, got[0].Replay)
	assert.True(t, got[1].Replay)
	assert.True(t, got[1].Shadow)
}
