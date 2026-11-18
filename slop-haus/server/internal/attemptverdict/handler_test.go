package attemptverdict_test

import (
	"log/slog"
	"net/http"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/attemptverdict"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

func statusOf(t *testing.T, err error) int {
	t.Helper()
	var se huma.StatusError
	require.ErrorAs(t, err, &se)
	return se.GetStatus()
}

// An agent's request carries no signed-in person, so the handler hands the
// service nil and the service refuses it.
func TestHandlerRefusesAnAgent(t *testing.T) {
	f := newFixture(t, dbtest.WithDB(t))
	ref := f.ticketAt(t, "review")
	h := attemptverdict.NewHandler(f.svc, slogDiscard())
	path := attemptverdict.AttemptPath{ID: ref.TicketID, Attempt: ref.Number}

	_, err := h.SetVerdict(f.ctx, &attemptverdict.SetVerdictRequest{
		AttemptPath: path, Body: attemptverdict.SetVerdictBody{Verdict: attemptverdict.WouldMerge}})
	assert.Equal(t, http.StatusForbidden, statusOf(t, err))
	_, err = h.GetVerdict(f.ctx, &path)
	assert.Equal(t, http.StatusForbidden, statusOf(t, err))

	got, err := f.store.AttemptVerdicts(f.ctx, []ticket.AttemptRef{ref})
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestHandlerMapsRefusalsToStatuses(t *testing.T) {
	f := newFixture(t, dbtest.WithDB(t))
	h := attemptverdict.NewHandler(f.svc, slogDiscard())
	ctx := authn.NewUserContext(f.ctx, f.person(t))
	set := func(ref ticket.AttemptRef, verdict string) error {
		_, err := h.SetVerdict(ctx, &attemptverdict.SetVerdictRequest{
			AttemptPath: attemptverdict.AttemptPath{ID: ref.TicketID, Attempt: ref.Number},
			Body:        attemptverdict.SetVerdictBody{Verdict: verdict}})
		return err
	}
	review := f.ticketAt(t, "review")

	assert.Equal(t, http.StatusConflict, statusOf(t, set(f.ticketAt(t, "queued"), attemptverdict.WouldMerge)))
	assert.Equal(t, http.StatusNotFound, statusOf(t, set(ticket.AttemptRef{TicketID: review.TicketID, Number: 4}, attemptverdict.WouldMerge)))
	assert.Equal(t, http.StatusUnprocessableEntity, statusOf(t, set(review, "bogus")))

	require.NoError(t, set(review, attemptverdict.WouldMerge))
	got, err := h.GetVerdict(ctx, &attemptverdict.AttemptPath{ID: review.TicketID, Attempt: 1})
	require.NoError(t, err)
	assert.Equal(t, attemptverdict.WouldMerge, got.Body.Verdict)
}

func slogDiscard() slog.Handler { return slog.DiscardHandler }
