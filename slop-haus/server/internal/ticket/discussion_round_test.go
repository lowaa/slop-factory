package ticket_test

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

type discussionRecorder struct {
	mu      sync.Mutex
	entries []ticket.DiscussionEntry
}

func (r *discussionRecorder) Entered(_ context.Context, e *ticket.DiscussionEntry) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries = append(r.entries, *e)
	return nil
}

func (r *discussionRecorder) rounds() []int {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []int
	for _, e := range r.entries {
		out = append(out, e.Round)
	}
	return out
}

func (f *fixture) withDiscussionHook() *discussionRecorder {
	rec := &discussionRecorder{}
	f.with(ticket.WithDiscussionHook(rec))
	return rec
}

func (f *fixture) discussionRound(t *testing.T, ticketID int64) int {
	t.Helper()
	var round int
	require.NoError(t, f.database.QueryRow(f.ctx,
		`SELECT discussion_round FROM attempts WHERE ticket_id = $1 AND number = 1`, ticketID).Scan(&round))
	return round
}

func TestDiscussionRound_EachEntryIntoDiscussionOpensTheNext(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	hook := f.withDiscussionHook()
	a := f.agent(t, "builder-1")
	created := f.ticket(t, "Asks twice", "server/")
	assert.Zero(t, f.discussionRound(t, created.ID), "no discussion, no round")

	f.claim(t, a)
	f.discuss(t, a)
	assert.Equal(t, 1, f.discussionRound(t, created.ID))

	f.humanReply(t, created.ID)
	f.closeAffinity(t, created.ID)
	f.claim(t, a)
	assert.Equal(t, 1, f.discussionRound(t, created.ID), "working again does not close the count")
	f.discuss(t, a)
	assert.Equal(t, 2, f.discussionRound(t, created.ID))

	f.svc.WaitDiscussionHooks()
	assert.ElementsMatch(t, []int{1, 2}, hook.rounds(), "the hook is told of each round once")
}

func TestDiscussionRound_ARepeatedMoveIntoDiscussionOpensNothing(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	store := newStore(f.database)
	a := f.agent(t, "builder-1")
	created := f.ticket(t, "Re-sent", "server/")
	f.claim(t, a)
	f.discuss(t, a)

	for range 2 {
		require.NoError(t, store.InTransition(f.ctx, func(tx ticket.TransitionTx) error {
			return tx.WriteTicket(f.ctx, created.ID, &ticket.TicketWrite{Status: ticket.To(ticket.StatusDiscussion)})
		}))
	}

	assert.Equal(t, 1, f.discussionRound(t, created.ID))
}

func (f *fixture) insertLabel(t *testing.T, ticketID int64, round int, label, category string) {
	t.Helper()
	assessed := "now()"
	if label == "not_assessed" {
		assessed = "NULL"
	}
	_, err := f.database.Exec(f.ctx, fmt.Sprintf(`
		INSERT INTO steward_discussion_labels (id, ticket_id, attempt, round, label, category, reason, assessed_at)
		VALUES ($1, $2, 1, $3, $4, $5, 'because', %s)`, assessed), uuid.New(), ticketID, round, label, category)
	require.NoError(t, err)
}

// discussionMux serves the ticket routes with the discussion reader; a person
// is signed in only when asPerson.
func (f *fixture) discussionMux(asPerson bool) http.Handler {
	if asPerson {
		return f.boardMuxWith(ticket.WithAttemptDiscussions(ticket.NewPostgresAttemptDiscussions(f.database)))
	}
	mux := http.NewServeMux()
	api := humago.New(mux, huma.DefaultConfig("test", "0.0.0"))
	ticket.RegisterRoutes(api, ticket.NewHandler(f.svc, slog.DiscardHandler,
		ticket.WithAttemptDiscussions(ticket.NewPostgresAttemptDiscussions(f.database))))
	// Not a person, but still within the tenant, as a worker's request is.
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mux.ServeHTTP(w, r.WithContext(db.WithTenant(r.Context(), f.tenant)))
	})
}

func (f *fixture) attemptsVia(t *testing.T, mux http.Handler, id int64) []ticket.AttemptBody {
	t.Helper()
	rec := send(t, mux, http.MethodGet, fmt.Sprintf("/api/tickets/%d", id), "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	return decodeTicket(t, rec.Body.Bytes()).Attempts
}

func TestAttemptDiscussion_TheBoardSeesTheCurrentRoundAndItsLabel(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")
	created := f.ticket(t, "Labelled", "server/")
	mux := f.discussionMux(true)
	f.claim(t, a)
	assert.Nil(t, f.attemptsVia(t, mux, created.ID)[0].Discussion, "not in discussion")

	f.discuss(t, a)
	got := f.attemptsVia(t, mux, created.ID)[0].Discussion
	require.NotNil(t, got)
	assert.Equal(t, 1, got.Round)
	assert.Nil(t, got.Label, "no label made yet")

	f.insertLabel(t, created.ID, 1, "routine", "merge_continue")
	got = f.attemptsVia(t, mux, created.ID)[0].Discussion
	require.NotNil(t, got.Label)
	assert.Equal(t, "routine", *got.Label)
	assert.Equal(t, "merge_continue", got.Category)
	assert.Equal(t, "because", got.Reason)

	f.humanReply(t, created.ID)
	f.closeAffinity(t, created.ID)
	f.claim(t, a)
	f.discuss(t, a)
	got = f.attemptsVia(t, mux, created.ID)[0].Discussion
	assert.Equal(t, 2, got.Round)
	assert.Nil(t, got.Label, "round 1's label is not round 2's")
}

func TestAttemptDiscussion_ADiscussionFromBeforeRoundsIsRoundOne(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")
	created := f.ticket(t, "Legacy", "server/")
	f.claim(t, a)
	f.discuss(t, a)
	_, err := f.database.Exec(f.ctx, `UPDATE attempts SET discussion_round = 0 WHERE ticket_id = $1`, created.ID)
	require.NoError(t, err)

	got := f.attemptsVia(t, f.discussionMux(true), created.ID)[0].Discussion

	require.NotNil(t, got)
	assert.Equal(t, 1, got.Round)
}

func TestAttemptDiscussion_TheKeyIsOnTheWireEvenWhenNull(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	created := f.ticket(t, "Idle", "server/")

	rec := send(t, f.discussionMux(true), http.MethodGet, fmt.Sprintf("/api/tickets/%d", created.ID), "")

	assert.Contains(t, rec.Body.String(), `"discussion":null`)
}

func TestAttemptDiscussion_AWorkerNeverSeesTheLabel(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	a := f.agent(t, "builder-1")
	created := f.ticket(t, "Hidden", "server/")
	f.claim(t, a)
	f.discuss(t, a)
	f.insertLabel(t, created.ID, 1, "yours", "")

	assert.Nil(t, f.attemptsVia(t, f.discussionMux(false), created.ID)[0].Discussion)
	assert.NotNil(t, f.attemptsVia(t, f.discussionMux(true), created.ID)[0].Discussion)
}
