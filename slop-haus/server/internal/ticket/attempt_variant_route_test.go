package ticket_test

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

func TestSetAttemptVariantAt_NamesTheAttempt(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	v1 := f.namedVariantVersion(t, "fast", 1)
	tk := f.ticket(t, "several", "a/")
	f.addBatch(t, tk.ID, nil, nil) // 1 is demoted, none promoted
	variantOf := func(n int) *uuid.UUID {
		var id *uuid.UUID
		require.NoError(t, f.database.QueryRow(f.ctx,
			`SELECT variant_version_id FROM attempts WHERE ticket_id = $1 AND number = $2`, tk.ID, n).Scan(&id))
		return id
	}

	_, err := f.svc.SetAttemptVariant(f.ctx, f.human, tk.ID, &v1)
	require.ErrorIs(t, err, ticket.ErrAttemptRequired, "the ticket-level route cannot pick among them")

	change, err := f.svc.SetAttemptVariantAt(f.ctx, f.human, tk.ID, 2, &v1)
	require.NoError(t, err)
	assert.True(t, change.Changed)
	assert.Equal(t, 2, change.Attempt.Number)
	require.NotNil(t, variantOf(2))
	assert.Equal(t, v1, *variantOf(2))
	assert.Nil(t, variantOf(1))
	assert.Nil(t, variantOf(3))

	_, err = f.svc.SetAttemptVariantAt(f.ctx, f.human, tk.ID, 3, &v1)
	require.NoError(t, err)
	change, err = f.svc.SetAttemptVariantAt(f.ctx, f.human, tk.ID, 3, nil)
	require.NoError(t, err)
	assert.True(t, change.Changed)
	assert.Nil(t, variantOf(3), "clearing the named attempt")
	require.NotNil(t, variantOf(2), "leaves the others alone")

	_, err = f.svc.SetAttemptVariantAt(f.ctx, f.human, tk.ID, 9, &v1)
	require.ErrorIs(t, err, ticket.ErrNoSuchAttempt)
	_, err = f.svc.SetAttemptVariantAt(f.ctx, f.human, 999999, 2, &v1)
	require.ErrorIs(t, err, ticket.ErrNotFound)
	archived := f.namedVariantVersion(t, "old", 1)
	f.archiveVariant(t, archived)
	_, err = f.svc.SetAttemptVariantAt(f.ctx, f.human, tk.ID, 3, &archived)
	require.ErrorIs(t, err, ticket.ErrInvalidVariant)

	// Attempt 2 is routed to v1: only an agent on v1 claims it, and then
	// its variant is locked while 3's is not.
	_, err = f.svc.Claim(f.ctx, f.agentOnVariant(t, "holder", v1), &tk.ID)
	require.NoError(t, err)
	_, err = f.svc.SetAttemptVariantAt(f.ctx, f.human, tk.ID, 2, nil)
	require.ErrorIs(t, err, ticket.ErrVariantLocked, "after its first lease")
	_, err = f.svc.SetAttemptVariantAt(f.ctx, f.human, tk.ID, 3, &v1)
	require.NoError(t, err, "another attempt is still open to change")
}

func TestPutTicketAttemptVariant_Handler(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	mux := f.attemptsMux(true, f.human)
	v1 := f.namedVariantVersion(t, "fast", 1)
	tk := f.ticket(t, "handled", "a/")
	f.addBatch(t, tk.ID, nil)
	put := func(n int, body string) int {
		return send(t, mux, http.MethodPut, fmt.Sprintf("/api/tickets/%d/attempts/%d/variant", tk.ID, n), body).Code
	}

	rec := send(t, mux, http.MethodPut, fmt.Sprintf("/api/tickets/%d/attempts/2/variant", tk.ID),
		fmt.Sprintf(`{"version_id": %q}`, v1))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	got := decodeTicket(t, rec.Body.Bytes())
	require.Len(t, got.Attempts, 2)
	assert.Nil(t, got.Attempts[0].Variant)
	require.NotNil(t, got.Attempts[1].Variant)
	assert.Equal(t, v1.String(), got.Attempts[1].Variant.VersionID)

	assert.Equal(t, http.StatusUnprocessableEntity, put(2, `{"version_id": "nope"}`))
	assert.Equal(t, http.StatusNotFound, put(2, fmt.Sprintf(`{"version_id": %q}`, uuid.New())))
	assert.Equal(t, http.StatusNotFound, put(7, `{"version_id": null}`), "unknown attempt")
	assert.Equal(t, http.StatusUnprocessableEntity, put(0, `{"version_id": null}`), "attempt numbers start at 1")
	archived := f.namedVariantVersion(t, "old", 1)
	f.archiveVariant(t, archived)
	assert.Equal(t, http.StatusUnprocessableEntity, put(1, fmt.Sprintf(`{"version_id": %q}`, archived)))
	assert.Equal(t, http.StatusNotFound,
		send(t, mux, http.MethodPut, "/api/tickets/999999/attempts/1/variant", `{"version_id": null}`).Code)

	_, err := f.svc.Claim(f.ctx, f.agentOnVariant(t, "holder", v1), &tk.ID)
	require.NoError(t, err)
	assert.Equal(t, http.StatusConflict, put(2, `{"version_id": null}`), "leased")

	// The ticket-level route still answers for a ticket with one attempt.
	single := f.ticket(t, "single", "b/")
	rec = send(t, mux, http.MethodPut, fmt.Sprintf("/api/tickets/%d/variant", single.ID), fmt.Sprintf(`{"version_id": %q}`, v1))
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, http.StatusConflict,
		send(t, mux, http.MethodPut, fmt.Sprintf("/api/tickets/%d/variant", tk.ID), `{"version_id": null}`).Code,
		"several attempts and none promoted")
}
