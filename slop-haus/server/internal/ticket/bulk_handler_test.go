package ticket_test

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-auth/authn/authntest"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

// The board's bulk actions over the wire, against the real store: their
// point is the transaction's all-or-nothing, which a fake would only
// imitate.

// cappedBoardMux is boardMux with a bulk cap of max.
func (f *fixture) cappedBoardMux(max int) http.Handler {
	svc := ticket.NewService(newStore(f.database), slog.DiscardHandler, ticket.WithBulkMax(max))
	mux := http.NewServeMux()
	api := humago.New(mux, huma.DefaultConfig("test", "0.0.0"))
	api.UseMiddleware(authn.UserMiddleware(api, func(_ context.Context, _ string) (*authn.User, error) {
		return &authn.User{ID: f.human, TenantID: f.tenant}, nil
	}, authntest.Guard(), slog.New(slog.DiscardHandler)))
	ticket.RegisterRoutes(api, ticket.NewHandler(svc, slog.DiscardHandler))
	return mux
}

func decodeBulk(t *testing.T, body []byte) []ticket.TicketBody {
	t.Helper()
	var got struct {
		Tickets []ticket.TicketBody `json:"tickets"`
	}
	require.NoError(t, json.Unmarshal(body, &got))
	return got.Tickets
}

func decodeRefused(t *testing.T, body []byte) ticket.BulkRefusedBody {
	t.Helper()
	var got ticket.BulkRefusedBody
	require.NoError(t, json.Unmarshal(body, &got))
	require.Equal(t, ticket.BulkRefusedCode, got.Code, string(body))
	return got
}

func TestBulkWire_EachActionReturnsTheTicketsItChanged(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	mux := f.boardMux()
	a, b := f.preparing(t, "A"), f.preparing(t, "B")

	rec := send(t, mux, http.MethodPost, "/api/tickets/bulk/priority",
		fmt.Sprintf(`{"ids": [%d, %d], "priority": "high"}`, b.ID, a.ID))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	got := decodeBulk(t, rec.Body.Bytes())
	require.Len(t, got, 2)
	assert.Equal(t, []int64{b.ID, a.ID}, []int64{got[0].ID, got[1].ID}, "in the order asked for")
	assert.Equal(t, []model.Priority{"high", "high"}, []model.Priority{got[0].Priority, got[1].Priority})

	rec = send(t, mux, http.MethodPost, "/api/tickets/bulk/ready", fmt.Sprintf(`{"ids": [%d, %d]}`, a.ID, b.ID))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	for _, tk := range decodeBulk(t, rec.Body.Bytes()) {
		assert.Equal(t, ticket.StatusOpen, tk.Status)
	}

	rec = send(t, mux, http.MethodPost, "/api/tickets/bulk/status",
		fmt.Sprintf(`{"ids": [%d, %d], "status": "parked"}`, a.ID, b.ID))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	for _, tk := range decodeBulk(t, rec.Body.Bytes()) {
		assert.Equal(t, ticket.StatusParked, tk.Status)
	}
}

// A refused batch is a 409 listing every refused ticket, each with its
// status and the rule, and nothing changes.
func TestBulkWire_ARefusalListsEveryReason(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	mux := f.boardMux()
	a := f.agent(t, "builder")
	held := f.ticket(t, "Held", "server/")
	f.claim(t, a)
	drafting := f.preparing(t, "Drafting")
	parked := f.parked(t, "Parked")
	queued := f.ticket(t, "Queued", "web/")

	rec := send(t, mux, http.MethodPost, "/api/tickets/bulk/status",
		fmt.Sprintf(`{"ids": [%d, %d, %d, %d], "status": "open"}`, held.ID, drafting.ID, parked.ID, queued.ID))

	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	refused := decodeRefused(t, rec.Body.Bytes())
	assert.Equal(t, http.StatusConflict, refused.Status)
	assert.Equal(t, "2 ticket(s) refused; nothing was changed.", refused.Detail)
	assert.Equal(t, []ticket.BulkRefusalEntry{
		{ID: drafting.ID, Status: ptr(ticket.StatusPreparing), Message: fmt.Sprintf(
			"#%d preparing: a preparing ticket enters the queue only by being opened", drafting.ID)},
		{ID: parked.ID, Status: ptr(ticket.StatusParked), Message: fmt.Sprintf(
			"#%d parked: a parked ticket is unparked back to preparing, not into the queue", parked.ID)},
	}, refused.Refusals)
	assert.Equal(t, ticket.StatusInProgress, f.status(t, held.ID), "not requeued: the batch was refused")

	rec = send(t, mux, http.MethodPost, "/api/tickets/bulk/status",
		fmt.Sprintf(`{"ids": [%d, %d], "status": "parked"}`, held.ID, queued.ID))
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	assert.Equal(t, []ticket.BulkRefusalEntry{{ID: held.ID, Status: ptr(ticket.StatusInProgress), Message: fmt.Sprintf(
		"#%d in progress: only a preparing or open ticket can be parked -- "+
			"release a held ticket, or send it back to preparing, first", held.ID)}},
		decodeRefused(t, rec.Body.Bytes()).Refusals)
	assert.Equal(t, ticket.StatusOpen, f.status(t, queued.ID))

	rec = send(t, mux, http.MethodPost, "/api/tickets/bulk/ready", fmt.Sprintf(`{"ids": [%d, %d]}`, drafting.ID, queued.ID))
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	assert.Equal(t, []ticket.BulkRefusalEntry{{ID: queued.ID, Status: ptr(ticket.StatusOpen), Message: fmt.Sprintf(
		"#%d open: ticket is not preparing", queued.ID)}}, decodeRefused(t, rec.Body.Bytes()).Refusals)
	assert.Equal(t, ticket.StatusPreparing, f.status(t, drafting.ID))
}

// A ticket the tenant does not have -- never filed, or another tenant's --
// makes the batch a 404, still listing every refusal.
func TestBulkWire_AMissingTicketIsA404(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	mux := f.boardMux()
	drafting := f.preparing(t, "Drafting")
	queued := f.ticket(t, "Queued")

	missing := ticket.BulkRefusalEntry{ID: 999, Message: "#999: not found"}
	for path, tc := range map[string]struct {
		body string
		want []ticket.BulkRefusalEntry
	}{
		// A mixed batch lists the rule refusals beside the missing ticket.
		"/api/tickets/bulk/status": {
			fmt.Sprintf(`{"ids": [999, %d, %d], "status": "open"}`, drafting.ID, queued.ID),
			[]ticket.BulkRefusalEntry{{ID: drafting.ID, Status: ptr(ticket.StatusPreparing), Message: fmt.Sprintf(
				"#%d preparing: a preparing ticket enters the queue only by being opened", drafting.ID)}, missing},
		},
		"/api/tickets/bulk/ready":    {fmt.Sprintf(`{"ids": [%d, 999]}`, drafting.ID), []ticket.BulkRefusalEntry{missing}},
		"/api/tickets/bulk/priority": {fmt.Sprintf(`{"ids": [%d, 999], "priority": "low"}`, queued.ID), []ticket.BulkRefusalEntry{missing}},
	} {
		rec := send(t, mux, http.MethodPost, path, tc.body)
		require.Equal(t, http.StatusNotFound, rec.Code, "%s: %s", path, rec.Body.String())
		assert.Equal(t, tc.want, decodeRefused(t, rec.Body.Bytes()).Refusals, path)
	}
	assert.Equal(t, ticket.StatusPreparing, f.status(t, drafting.ID))
	assert.Equal(t, model.PriorityNormal, f.get(t, queued.ID).Priority)
}

func TestBulkWire_TheBatchIsShapedBeforeAnythingIsWritten(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	mux := f.cappedBoardMux(2)
	a, b, c := f.ticket(t, "A"), f.ticket(t, "B"), f.ticket(t, "C")

	rec := send(t, mux, http.MethodPost, "/api/tickets/bulk/priority",
		fmt.Sprintf(`{"ids": [%d, %d, %d], "priority": "high"}`, a.ID, b.ID, c.ID))
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "too many tickets for one bulk action: 3 tickets, at most 2")
	assert.Equal(t, model.PriorityNormal, f.get(t, a.ID).Priority)

	rec = send(t, mux, http.MethodPost, "/api/tickets/bulk/status",
		fmt.Sprintf(`{"ids": [%d, %d, %d, %d], "status": "parked"}`, a.ID, b.ID, a.ID, b.ID))
	require.Equal(t, http.StatusOK, rec.Code, "duplicates count once against the cap: %s", rec.Body.String())
	assert.Len(t, decodeBulk(t, rec.Body.Bytes()), 2, "and come back once")

	for path, body := range map[string]string{
		"/api/tickets/bulk/status":   `{"ids": [], "status": "open"}`,
		"/api/tickets/bulk/ready":    `{"ids": []}`,
		"/api/tickets/bulk/priority": `{"ids": [], "priority": "low"}`,
	} {
		rec := send(t, mux, http.MethodPost, path, body)
		assert.Equal(t, http.StatusUnprocessableEntity, rec.Code, "%s: %s", path, rec.Body.String())
	}

	rec = send(t, mux, http.MethodPost, "/api/tickets/bulk/status", fmt.Sprintf(`{"ids": [%d], "status": "gone"}`, c.ID))
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
	rec = send(t, mux, http.MethodPost, "/api/tickets/bulk/priority", fmt.Sprintf(`{"ids": [%d], "priority": "asap"}`, c.ID))
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
}

// The bulk routes are a signed-in person's: with no session each is a 401
// from the middleware, before the handler (and the fake store behind it)
// is reached.
func TestBulkWire_RequiresASignedInUser(t *testing.T) {
	t.Parallel()
	mux := newUnauthenticatedTestMux()
	for path, body := range map[string]string{
		"/api/tickets/bulk/status":   `{"ids": [1], "status": "open"}`,
		"/api/tickets/bulk/ready":    `{"ids": [1]}`,
		"/api/tickets/bulk/priority": `{"ids": [1], "priority": "low"}`,
	} {
		rec := send(t, mux, http.MethodPost, path, body)
		assert.Equal(t, http.StatusUnauthorized, rec.Code, "%s: %s", path, rec.Body.String())
	}
}

// Agents get no bulk action: an agent's token is refused on each route,
// and nothing it names changes.
func TestBulkWire_AnAgentIsRefused(t *testing.T) {
	t.Parallel()
	f := newRoleFixture(t)
	mux, _ := f.rolesMux(t, true)
	queued := f.ticket(t, "Queued")

	for path, body := range map[string]string{
		"/api/tickets/bulk/status":   fmt.Sprintf(`{"ids": [%d], "status": "parked"}`, queued.ID),
		"/api/tickets/bulk/ready":    fmt.Sprintf(`{"ids": [%d]}`, queued.ID),
		"/api/tickets/bulk/priority": fmt.Sprintf(`{"ids": [%d], "priority": "urgent"}`, queued.ID),
	} {
		rec := sendAs(t, mux, http.MethodPost, path, rolesAgentToken, body)
		assert.Equal(t, http.StatusUnauthorized, rec.Code, "%s: %s", path, rec.Body.String())
	}
	got := f.get(t, queued.ID)
	assert.Equal(t, ticket.StatusOpen, got.Status)
	assert.Equal(t, model.PriorityNormal, got.Priority)

	// The token itself works: it is refused for what it is.
	rec := sendAs(t, mux, http.MethodPost, "/api/claims", rolesAgentToken, `{}`)
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
}
