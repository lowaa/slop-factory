package ticket_test

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost/codehosttest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// withBatchMax swaps in a service that caps a replay batch at max attempts.
func (r *pinRig) withBatchMax(max int) {
	r.svc = ticket.NewService(newStore(r.database), slog.DiscardHandler,
		ticket.WithCodeHosts(codehosttest.Registry(r.host)), ticket.WithWorkspace(r.ws), ticket.WithReplayBatchMax(max))
}

// doneReplayableAs is doneReplayable for a second ticket: agent names are unique.
func (r *pinRig) doneReplayableAs(t *testing.T, name string) *ticket.Ticket {
	t.Helper()
	original := r.agent(t, name)
	tk := r.createWithBody(t, "Original title", "Original body", "a/")
	require.NotNil(t, r.claim(t, original))
	_, err := r.svc.Done(r.ctx, original, &ticket.DoneParams{Summary: "Done."})
	require.NoError(t, err)
	return tk
}

func (r *pinRig) batchCount(t *testing.T, id int64) int {
	t.Helper()
	var n int
	require.NoError(t, r.database.QueryRow(r.ctx, `SELECT count(*) FROM attempts WHERE ticket_id = $1 AND replay`, id).Scan(&n))
	return n
}

func TestReplayBatch_AddsEveryVariantAndRunUnderOneBatchID(t *testing.T) {
	t.Parallel()
	r := newPinRig(t)
	a, b := r.doneReplayable(t), r.doneReplayableAs(t, "second")

	res, err := r.svc.AddReplayBatch(r.ctx, r.human, &ticket.ReplayBatchRequest{
		TicketIDs: []int64{a.ID, b.ID}, VersionIDs: []*uuid.UUID{nil}, Runs: 2, ConfirmAttempts: 4,
	})

	require.NoError(t, err)
	assert.Empty(t, res.Refused)
	require.Len(t, res.Added, 2)
	assert.Len(t, res.Added[0].Attempts, 2)
	var n int
	require.NoError(t, r.database.QueryRow(r.ctx,
		`SELECT count(*) FROM attempts WHERE replay_batch_id = $1 AND replay AND shadow`, res.BatchID).Scan(&n))
	assert.Equal(t, 4, n)
}

func TestReplayBatch_IsRefusedWholeWithoutAddingAnything(t *testing.T) {
	t.Parallel()
	setup := func(t *testing.T, max int) (*pinRig, *ticket.Ticket) {
		r := newPinRig(t)
		r.withBatchMax(max)
		return r, r.doneReplayable(t)
	}
	cases := []struct {
		name string
		max  int
		req  ticket.ReplayBatchRequest
		want error
	}{
		{"a confirmation that names another number", 10, ticket.ReplayBatchRequest{VersionIDs: []*uuid.UUID{nil}, Runs: 2, ConfirmAttempts: 1}, ticket.ErrReplayBatchUnconfirmed},
		{"no confirmation", 10, ticket.ReplayBatchRequest{VersionIDs: []*uuid.UUID{nil}, Runs: 1}, ticket.ErrReplayBatchUnconfirmed},
		{"more attempts than the maximum", 2, ticket.ReplayBatchRequest{VersionIDs: []*uuid.UUID{nil}, Runs: 3, ConfirmAttempts: 3}, ticket.ErrReplayBatchTooLarge},
		{"more attempts per ticket than a ticket holds", 100, ticket.ReplayBatchRequest{VersionIDs: []*uuid.UUID{nil}, Runs: ticket.MaxLiveAttempts + 1, ConfirmAttempts: ticket.MaxLiveAttempts + 1}, ticket.ErrReplayBatchShape},
		{"no variants", 10, ticket.ReplayBatchRequest{Runs: 1}, ticket.ErrReplayBatchShape},
		{"no runs", 10, ticket.ReplayBatchRequest{VersionIDs: []*uuid.UUID{nil}}, ticket.ErrReplayBatchShape},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, tk := setup(t, c.max)
			c.req.TicketIDs = []int64{tk.ID}

			_, err := r.svc.AddReplayBatch(r.ctx, r.human, &c.req)

			require.ErrorIs(t, err, c.want)
			assert.Zero(t, r.batchCount(t, tk.ID))
		})
	}
}

func TestReplayBatch_ReportsTheTicketsItRefusedAndAddsTheRest(t *testing.T) {
	t.Parallel()
	r := newPinRig(t)
	ok := r.doneReplayable(t)
	full := r.doneReplayableAs(t, "second")
	for i := 0; i < ticket.MaxLiveAttempts; i++ {
		r.addReplay(t, full.ID)
	}
	live := r.ticket(t, "Live", "b/")

	res, err := r.svc.AddReplayBatch(r.ctx, r.human, &ticket.ReplayBatchRequest{
		TicketIDs: []int64{ok.ID, full.ID, live.ID, 999999}, VersionIDs: []*uuid.UUID{nil}, Runs: 1, ConfirmAttempts: 4,
	})

	require.NoError(t, err)
	require.Len(t, res.Added, 1)
	assert.Equal(t, ok.ID, res.Added[0].TicketID)
	refused := map[int64]string{}
	for _, f := range res.Refused {
		refused[f.TicketID] = f.Reason
	}
	require.Len(t, refused, 3)
	assert.Contains(t, refused[full.ID], "live attempts")
	assert.Contains(t, refused[live.ID], "only a done or parked ticket")
	assert.Contains(t, refused, int64(999999))
	assert.Equal(t, ticket.MaxLiveAttempts, r.batchCount(t, full.ID), "a refused ticket keeps only what it had")
}

func TestReplayBatch_DuplicateVariantsAndTicketsCountOnce(t *testing.T) {
	t.Parallel()
	r := newPinRig(t)
	tk := r.doneReplayable(t)

	res, err := r.svc.AddReplayBatch(r.ctx, r.human, &ticket.ReplayBatchRequest{
		TicketIDs: []int64{tk.ID, tk.ID}, VersionIDs: []*uuid.UUID{nil, nil}, Runs: 1, ConfirmAttempts: 1,
	})

	require.NoError(t, err)
	require.Len(t, res.Added, 1)
	assert.Equal(t, 1, r.batchCount(t, tk.ID))
}

func TestReplayBatch_HandlerIsGatedLimitedAndConfirmed(t *testing.T) {
	t.Parallel()
	r := newPinRig(t)
	r.withBatchMax(3)
	tk := r.doneReplayable(t)
	body := func(confirm int) string {
		return fmt.Sprintf(`{"ticket_ids":[%d],"version_ids":[null],"runs":2,"confirm_attempts":%d}`, tk.ID, confirm)
	}

	off := send(t, r.replayMux(false), http.MethodPost, "/api/replay_batches", body(2))
	assert.Equal(t, http.StatusNotFound, off.Code, off.Body.String())
	assert.Equal(t, ticket.FeatureDisabledCode, errorCode(t, off.Body.Bytes()))
	offLimits := send(t, r.replayMux(false), http.MethodGet, "/api/replay_batches/limits", "")
	assert.Equal(t, http.StatusNotFound, offLimits.Code)

	limits := send(t, r.replayMux(true), http.MethodGet, "/api/replay_batches/limits", "")
	require.Equal(t, http.StatusOK, limits.Code, limits.Body.String())
	assert.Contains(t, limits.Body.String(), `"max_attempts":3`)
	assert.Contains(t, limits.Body.String(), fmt.Sprintf(`"max_per_ticket":%d`, ticket.MaxLiveAttempts))

	unconfirmed := send(t, r.replayMux(true), http.MethodPost, "/api/replay_batches", body(1))
	assert.Equal(t, http.StatusUnprocessableEntity, unconfirmed.Code, unconfirmed.Body.String())
	assert.Zero(t, r.batchCount(t, tk.ID))

	on := send(t, r.replayMux(true), http.MethodPost, "/api/replay_batches", body(2))
	require.Equal(t, http.StatusCreated, on.Code, on.Body.String())
	assert.Equal(t, 2, r.batchCount(t, tk.ID))
}

func TestReplayBatch_RoutesEachVariantRunAndTheHandlerReturnsTheResult(t *testing.T) {
	t.Parallel()
	r := newPinRig(t)
	tk := r.doneReplayable(t)
	v := r.namedVariantVersion(t, "careful", 1)
	body := fmt.Sprintf(`{"ticket_ids":[%d,999999],"version_ids":[null,%q],"runs":2,"confirm_attempts":8}`, tk.ID, v)

	resp := send(t, r.replayMux(true), http.MethodPost, "/api/replay_batches", body)

	require.Equal(t, http.StatusCreated, resp.Code, resp.Body.String())
	var got struct {
		BatchID string `json:"batch_id"`
		Added   []struct {
			TicketID int64 `json:"ticket_id"`
			Attempts []int `json:"attempts"`
		} `json:"added"`
		Refused []struct {
			TicketID int64  `json:"ticket_id"`
			Reason   string `json:"reason"`
		} `json:"refused"`
	}
	require.NoError(t, json.Unmarshal(resp.Body.Bytes(), &got))
	require.Len(t, got.Added, 1)
	assert.Equal(t, tk.ID, got.Added[0].TicketID)
	assert.Equal(t, []int{2, 3, 4, 5}, got.Added[0].Attempts)
	require.Len(t, got.Refused, 1)
	assert.EqualValues(t, 999999, got.Refused[0].TicketID)
	assert.NotEmpty(t, got.Refused[0].Reason)
	var routed, unrouted int
	require.NoError(t, r.database.QueryRow(r.ctx, `SELECT count(*) FILTER (WHERE variant_version_id = $2), count(*) FILTER (WHERE variant_version_id IS NULL AND replay)
		FROM attempts WHERE replay_batch_id::text = $1`, got.BatchID, v).Scan(&routed, &unrouted))
	assert.Equal(t, 2, routed)
	assert.Equal(t, 2, unrouted)
}
