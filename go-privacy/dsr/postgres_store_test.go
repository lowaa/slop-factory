package dsr_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-db/dbtest"
	"github.com/lowaa/slop-factory/go-privacy/dsr"
	"github.com/lowaa/slop-factory/go-privacy/dsr/dsrtest"
)

func TestPostgresStore(t *testing.T) {
	dsrtest.StoreSuite(t, dsr.NewPostgresStore(dbtest.AppEngine()))
}

func TestEventsAreAppendOnlyButGoWithTheirRequest(t *testing.T) {
	ctx := t.Context()
	f := newFixture(t)
	r, err := f.svc.Create(ctx, &dsr.NewRequest{RequesterEmail: "a@example.test", Type: dsr.TypeAccess, Source: dsr.SourceEmail, Actor: "op"})
	require.NoError(t, err)
	eng := dbtest.AppEngine()

	_, err = eng.ExecContext(ctx, `UPDATE global_privacy_request_events SET detail = 'rewritten' WHERE request_id = $1`, r.ID)
	require.Error(t, err, "history can't be rewritten, even by the app role")

	_, err = eng.ExecContext(ctx, `DELETE FROM global_privacy_request_events WHERE request_id = $1`, r.ID)
	require.Error(t, err, "nor can a single entry be removed from it")

	_, err = eng.ExecContext(ctx, `DELETE FROM global_privacy_requests WHERE id = $1`, r.ID)
	require.NoError(t, err)
	var n int
	require.NoError(t, eng.QueryRowContext(ctx, `SELECT count(*) FROM global_privacy_request_events WHERE request_id = $1`, r.ID).Scan(&n))
	assert.Zero(t, n, "retention deleting a request takes its trail")
}

func TestStoreErrorsCarryNoRowValues(t *testing.T) {
	store := dsr.NewPostgresStore(dbtest.AppEngine())
	id := uuid.New()
	// The database's own message for this failure quotes the whole row.
	err := store.Insert(t.Context(), &dsr.Request{
		ID: id, RequesterEmail: "canary-person@example.test", Type: "bogus", Source: dsr.SourceEmail,
		Status: dsr.StatusReceived, ReceivedAt: time.Now(), DueAt: time.Now(),
	}, &dsr.Event{RequestID: id, Actor: "x", Kind: dsr.EventReceived})
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "canary-person")
	assert.Contains(t, err.Error(), "insert request")
	assert.Contains(t, err.Error(), "sqlstate 23514")
}

func TestSchemaRefusesInconsistentRows(t *testing.T) {
	ctx := t.Context()
	eng := dbtest.AppEngine()
	insert := func(status string, closed any, verified bool, method any) error {
		_, err := eng.ExecContext(ctx, `INSERT INTO global_privacy_requests
			(requester_email, request_type, source, status, received_at, due_at, closed_at, verified, verification_method, verified_at)
			VALUES ('x@example.test', 'access', 'email', $1, now(), now(), $2, $3, $4, CASE WHEN $3 THEN now() END)`,
			status, closed, verified, method)
		return err
	}
	require.NoError(t, insert("received", nil, false, nil))
	assert.Error(t, insert("completed", nil, false, nil), "a closed status needs closed_at")
	assert.Error(t, insert("received", time.Now(), false, nil), "closed_at needs a closed status")
	assert.Error(t, insert("received", nil, true, nil), "verified needs a method")
	assert.Error(t, insert("received", nil, false, "email_reply"), "a method needs verified")

	_, err := eng.ExecContext(ctx, `INSERT INTO global_privacy_requests
		(requester_email, request_type, source, status, received_at, due_at) VALUES ('x@example.test', 'access', 'email', 'received', now(), now() - interval '1 day')`)
	assert.Error(t, err, "a request is not due before it was received")
	_, err = eng.ExecContext(ctx, `INSERT INTO global_privacy_requests
		(requester_email, request_type, source, status, received_at, due_at, verified, verification_method)
		VALUES ('x@example.test', 'access', 'email', 'received', now(), now(), true, 'email_reply')`)
	assert.Error(t, err, "a verification needs its time")
	_, err = eng.ExecContext(ctx, `INSERT INTO global_privacy_requests
		(requester_email, request_type, source, status, received_at, due_at, verified_at)
		VALUES ('x@example.test', 'access', 'email', 'received', now(), now(), now())`)
	assert.Error(t, err, "a verification time needs a verification")
}
