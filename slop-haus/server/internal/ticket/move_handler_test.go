package ticket_test

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// The board's status edit against the real store: a status change is a
// transition, and transitions are tested against PostgresStore, not a fake
// of it.

func TestUpdateTicket_PatchesStatus(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	created := f.ticket(t, "one")

	rec := send(t, f.boardMux(), http.MethodPatch, fmt.Sprintf("/api/tickets/%d", created.ID),
		`{"status": "in_progress"}`)

	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	body := decodeTicket(t, rec.Body.Bytes())
	assert.Equal(t, ticket.StatusInProgress, body.Status)
	assert.Equal(t, "one", body.Title, "unset fields are left alone")
}

// Status and priority are enums in the spec, so huma refuses any other value
// before the handler runs -- including an empty one, which is no status.
func TestUpdateTicket_RefusesAValueOutsideTheEnum(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	created := f.ticket(t, "one")

	for _, body := range []string{`{"status": "finished"}`, `{"priority": "critical"}`, `{"priority": ""}`} {
		rec := send(t, f.boardMux(), http.MethodPatch, fmt.Sprintf("/api/tickets/%d", created.ID), body)
		assert.Equal(t, http.StatusUnprocessableEntity, rec.Code, "%s: %s", body, rec.Body.String())
	}
	assert.Equal(t, ticket.StatusOpen, f.status(t, created.ID))
}
