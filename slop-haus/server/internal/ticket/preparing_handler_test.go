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
)

// The board's side of Preparing over the wire, against the real store:
// the rules live in PostgresStore, so a fake here would only be testing
// itself.

// boardMux wires the ticket routes as cmd/api does, signed in as the
// fixture's human in the fixture's tenant.
func (f *fixture) boardMux() http.Handler { return f.boardMuxWith() }

func (f *fixture) boardMuxWith(opts ...ticket.HandlerOption) http.Handler {
	mux := http.NewServeMux()
	api := humago.New(mux, huma.DefaultConfig("test", "0.0.0"))
	api.UseMiddleware(authn.UserMiddleware(api, func(_ context.Context, _ string) (*authn.User, error) {
		return &authn.User{ID: f.human, TenantID: f.tenant}, nil
	}, authntest.Guard(), slog.New(slog.DiscardHandler)))
	ticket.RegisterRoutes(api, ticket.NewHandler(f.svc, slog.DiscardHandler, opts...))
	return mux
}

func decodeTicket(t *testing.T, body []byte) ticket.TicketBody {
	t.Helper()
	var got ticket.TicketBody
	require.NoError(t, json.Unmarshal(body, &got))
	return got
}

func TestCreateTicket_TheBoardChoosesPreparingOrOpen(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	mux := f.boardMux()

	for body, want := range map[string]ticket.Status{
		`{"title": "Draft", "status": "preparing"}`: ticket.StatusPreparing,
		`{"title": "Go", "status": "open"}`:         ticket.StatusOpen,
		// What every client did before the board had two buttons.
		`{"title": "Legacy"}`: ticket.StatusOpen,
	} {
		rec := send(t, mux, http.MethodPost, "/api/tickets", body)
		require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
		assert.Equal(t, want, decodeTicket(t, rec.Body.Bytes()).Status, body)
	}

	rec := send(t, mux, http.MethodPost, "/api/tickets", `{"title": "Skip the queue", "status": "in_progress"}`)
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
}

func TestMarkTicketReady_Wire(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	mux := f.boardMux()
	dep := f.ticket(t, "Unfinished dependency")
	free := f.preparing(t, "Nothing pending")
	waiting := f.preparing(t, "Waits on the dependency")
	f.blockedBy(t, waiting.ID, dep.ID)

	rec := send(t, mux, http.MethodPost, fmt.Sprintf("/api/tickets/%d/ready", free.ID), "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, ticket.StatusOpen, decodeTicket(t, rec.Body.Bytes()).Status)

	rec = send(t, mux, http.MethodPost, fmt.Sprintf("/api/tickets/%d/ready", waiting.ID), "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, ticket.StatusBlocked, decodeTicket(t, rec.Body.Bytes()).Status)

	rec = send(t, mux, http.MethodPost, fmt.Sprintf("/api/tickets/%d/ready", free.ID), "")
	assert.Equal(t, http.StatusConflict, rec.Code, "a second Ready: %s", rec.Body.String())

	rec = send(t, mux, http.MethodPost, "/api/tickets/9999/ready", "")
	assert.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
}

func TestUpdateTicket_LeavingPreparingForTheQueueIsAConflict(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	mux := f.boardMux()
	drafting := f.preparing(t, "Draft")
	path := fmt.Sprintf("/api/tickets/%d", drafting.ID)

	rec := send(t, mux, http.MethodPatch, path, `{"status": "open"}`)
	assert.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "opened", "the refusal says what to do instead")

	rec = send(t, mux, http.MethodPatch, path, `{"status": "cancelled"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, ticket.StatusCancelled, decodeTicket(t, rec.Body.Bytes()).Status)
}
