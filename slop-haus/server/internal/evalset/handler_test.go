package evalset_test

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/evalset"
)

// server serves the routes as who; nil is a caller that is not a person.
func (n *tenant) server(who *authn.User) *http.ServeMux {
	mux := http.NewServeMux()
	api := humago.New(mux, huma.DefaultConfig("test", "0.0.0"))
	api.UseMiddleware(func(ctx huma.Context, next func(huma.Context)) {
		if who != nil {
			ctx = huma.WithContext(ctx, authn.NewUserContext(ctx.Context(), who))
		}
		next(ctx)
	})
	evalset.RegisterRoutes(api, evalset.NewHandler(n.svc, slog.DiscardHandler))
	return mux
}

func call(mux *http.ServeMux, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestHandler_RunReportsTheBatchItsRefusalsAndItsHistory(t *testing.T) {
	n := newTenant(t, dbtest.WithDB(t), 10)
	ok, open := n.done("Replayable"), n.create("Never claimed")
	person := n.server(n.who)
	rec := call(person, http.MethodPost, "/api/eval_sets", fmt.Sprintf(`{"name":"bench","description":"","ticket_ids":[%d,%d]}`, ok.ID, open.ID))
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var set evalset.EvalSetBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &set))

	rec = call(person, http.MethodPost, "/api/eval_sets/"+set.ID+"/runs", `{"version_ids":[null],"runs":1,"confirm_attempts":2}`)

	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var run evalset.RunEvalSetResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &run.Body))
	require.Len(t, run.Body.Added, 1)
	assert.Equal(t, ok.ID, run.Body.Added[0].TicketID)
	assert.Len(t, run.Body.Added[0].Attempts, 1)
	require.Len(t, run.Body.Refused, 1)
	assert.Equal(t, open.ID, run.Body.Refused[0].TicketID)
	assert.Contains(t, run.Body.Refused[0].Reason, "only a done or parked ticket")

	rec = call(person, http.MethodGet, "/api/eval_sets/"+set.ID+"/batches", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var history evalset.ListEvalSetBatchesResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &history.Body))
	require.Len(t, history.Body.Batches, 1)
	assert.Equal(t, run.Body.BatchID.String(), history.Body.Batches[0].BatchID)
	assert.Equal(t, []*string{nil}, history.Body.Batches[0].VersionIDs)
	assert.Equal(t, 1, history.Body.Batches[0].TicketCount)

	rec = call(person, http.MethodPost, "/api/eval_sets/"+set.ID+"/runs", `{"version_ids":[null],"runs":1,"confirm_attempts":1}`)
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code, "the ticket service's confirmation rule answers 422")
}

func TestHandler_ANonPersonIsRefusedWith403(t *testing.T) {
	n := newTenant(t, dbtest.WithDB(t), 10)
	set := n.pinned("bench")
	agent := n.server(nil)

	for _, c := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/eval_sets", ""},
		{http.MethodPost, "/api/eval_sets", `{"name":"x","description":"","ticket_ids":[]}`},
		{http.MethodDelete, "/api/eval_sets/" + set.ID.String(), ""},
		{http.MethodPost, "/api/eval_sets/" + set.ID.String() + "/runs", `{"version_ids":[null],"runs":1,"confirm_attempts":1}`},
	} {
		rec := call(agent, c.method, c.path, c.body)
		assert.Equal(t, http.StatusForbidden, rec.Code, "%s %s: %s", c.method, c.path, rec.Body.String())
	}
}
