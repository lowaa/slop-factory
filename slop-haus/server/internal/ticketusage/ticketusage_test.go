package ticketusage_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2/humatest"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/reports"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticketusage"
)

type spec struct {
	key      string
	mode     string
	requests int64
	in       int64
	reported *int64
	est      *int64
}

func micros(v int64) *int64 { return &v }

type fixture struct {
	database *db.DB
	ctx      context.Context
	agent    uuid.UUID
}

func newFixture(t *testing.T, database *db.DB) *fixture {
	t.Helper()
	f := &fixture{database: database, ctx: db.WithTenant(context.Background(), dbtest.NewTenant(t, database)), agent: uuid.New()}
	_, err := database.Exec(f.ctx, `INSERT INTO agents (id, name) VALUES ($1, 'a')`, f.agent)
	require.NoError(t, err)
	return f
}

func (f *fixture) ticket(t *testing.T) int64 {
	t.Helper()
	var id int64
	require.NoError(t, f.database.QueryRow(f.ctx, `INSERT INTO tickets (title, status) VALUES ('t', 'open') RETURNING id`).Scan(&id))
	return id
}

func (f *fixture) worker(t *testing.T, ticketID int64, s spec) {
	t.Helper()
	if s.mode == "" {
		s.mode = "subscription"
	}
	_, err := f.database.Exec(f.ctx, `
INSERT INTO llm_usage (agent_id, transcript_key, harness, session_id, scope_hint, scope, ticket_id, provider, model,
    billing_mode, requests, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, reasoning_tokens,
    reported_cost_microdollars, estimated_cost_microdollars, price_version, unpriced, started_at, ended_at)
VALUES ($1, $2, 'pi', 's', 'main', 'ticket', $3, 'p', 'm1', $4, $5, $6, 0, 0, 0, 0, $7, $8, 'v', $7::bigint IS NULL AND $8::bigint IS NULL, $9, $9)`,
		f.agent, s.key, ticketID, s.mode, s.requests, s.in, s.reported, s.est, time.Now())
	require.NoError(t, err)
}

func (f *fixture) guidance(t *testing.T, ticketID int64, s spec) {
	t.Helper()
	if s.mode == "" {
		s.mode = "subscription"
	}
	_, err := f.database.Exec(f.ctx, `
INSERT INTO llm_guidance_usage (agent_id, transcript_key, ticket_id, harness, provider, model, billing_mode, requests,
    input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, reasoning_tokens,
    reported_cost_microdollars, estimated_cost_microdollars, price_version, unpriced, ended_at)
VALUES ($1, $2, $3, 'claude', 'p', 'm1', $4, $5, $6, 0, 0, 0, 0, $7, $8, 'v', ($7::bigint IS NULL AND $8::bigint IS NULL), $9)`,
		f.agent, s.key, ticketID, s.mode, s.requests, s.in, s.reported, s.est, time.Now())
	require.NoError(t, err)
}

func TestTicketUsageKeepsWorkerAndGuidanceApart(t *testing.T) {
	f := newFixture(t, dbtest.WithDB(t))
	tk := f.ticket(t)
	f.worker(t, tk, spec{key: "w1", requests: 2, in: 100, est: micros(10)})
	f.worker(t, tk, spec{key: "w2", mode: "metered", requests: 1, in: 7, reported: micros(300), est: micros(1)})
	f.worker(t, tk, spec{key: "w3", requests: 4, in: 1})
	f.guidance(t, tk, spec{key: "g1", requests: 3, in: 5, est: micros(50)})

	got, err := ticketusage.NewPostgresStore(f.database).TicketUsage(f.ctx, tk)
	require.NoError(t, err)

	require.Len(t, got.Worker, 2)
	met, sub := got.Worker[0], got.Worker[1]
	assert.Equal(t, "metered", met.BillingMode)
	assert.EqualValues(t, 300, *met.CostMicros, "reported wins over the estimate")
	assert.Equal(t, "subscription", sub.BillingMode)
	assert.EqualValues(t, 101, sub.InputTokens, "guidance's 5 is not in it")
	assert.EqualValues(t, 10, *sub.CostMicros, "the unpriced row adds nothing")
	assert.EqualValues(t, 1, sub.UnpricedRecords, "and is counted, not zeroed")
	assert.EqualValues(t, 4, sub.UnpricedRequests)

	require.Len(t, got.Guidance, 1)
	assert.EqualValues(t, 5, got.Guidance[0].InputTokens)
	assert.EqualValues(t, 3, got.Guidance[0].Requests)
	assert.EqualValues(t, 50, *got.Guidance[0].CostMicros)
}

func TestTicketUsageOneKindOrNone(t *testing.T) {
	f := newFixture(t, dbtest.WithDB(t))
	store := ticketusage.NewPostgresStore(f.database)
	onlyWorker, onlyGuidance, none := f.ticket(t), f.ticket(t), f.ticket(t)
	f.worker(t, onlyWorker, spec{key: "w", in: 1, est: micros(1)})
	f.guidance(t, onlyGuidance, spec{key: "g", in: 1, est: micros(1)})

	got, err := store.TicketUsage(f.ctx, onlyWorker)
	require.NoError(t, err)
	assert.Len(t, got.Worker, 1)
	assert.Empty(t, got.Guidance)

	got, err = store.TicketUsage(f.ctx, onlyGuidance)
	require.NoError(t, err)
	assert.Empty(t, got.Worker)
	assert.Len(t, got.Guidance, 1)

	got, err = store.TicketUsage(f.ctx, none)
	require.NoError(t, err)
	assert.Empty(t, got.Worker)
	assert.Empty(t, got.Guidance)
}

func TestTicketUsageOfAnotherTenantIsNotFound(t *testing.T) {
	app, _ := dbtest.WithOwnerDB(t)
	a, b := newFixture(t, app), newFixture(t, app)
	tb := b.ticket(t)
	b.worker(t, tb, spec{key: "w", in: 1000, est: micros(1)})
	b.guidance(t, tb, spec{key: "g", in: 1000, est: micros(1)})
	store := ticketusage.NewPostgresStore(app)

	_, err := store.TicketUsage(a.ctx, tb)
	require.ErrorIs(t, err, ticket.ErrNotFound)
	_, err = store.TicketUsage(a.ctx, 999999)
	require.ErrorIs(t, err, ticket.ErrNotFound)

	got, err := store.TicketUsage(b.ctx, tb)
	require.NoError(t, err)
	assert.EqualValues(t, 1000, got.Worker[0].InputTokens)
}

type fakeStore struct {
	usage *ticketusage.Usage
	err   error
}

func (s *fakeStore) TicketUsage(context.Context, int64) (*ticketusage.Usage, error) {
	return s.usage, s.err
}

func serve(t *testing.T, store ticketusage.Store) humatest.TestAPI {
	t.Helper()
	_, api := humatest.New(t)
	ticketusage.RegisterRoutes(api, ticketusage.NewHandler(store, slog.DiscardHandler))
	return api
}

func TestRouteServesWorkerAndGuidance(t *testing.T) {
	cost := int64(5)
	api := serve(t, &fakeStore{usage: &ticketusage.Usage{
		Worker:   []reports.UsageSums{{BillingMode: "metered", InputTokens: 9}},
		Guidance: []reports.UsageSums{{BillingMode: "subscription", CostMicros: &cost}},
	}})
	res := api.Get("/api/tickets/3/llm-usage")
	require.Equal(t, http.StatusOK, res.Code)
	var body ticketusage.TicketUsageBody
	require.NoError(t, json.Unmarshal(res.Body.Bytes(), &body))
	require.Len(t, body.Worker, 1)
	assert.Equal(t, "metered", body.Worker[0].BillingMode)
	assert.EqualValues(t, 9, body.Worker[0].InputTokens)
	require.Len(t, body.Guidance, 1)
	assert.EqualValues(t, 5, *body.Guidance[0].CostMicros)
}

func TestRouteEmptyUsageIsEmptyListsNotNull(t *testing.T) {
	res := serve(t, &fakeStore{usage: &ticketusage.Usage{}}).Get("/api/tickets/3/llm-usage")
	require.Equal(t, http.StatusOK, res.Code)
	assert.JSONEq(t, `{"worker":[],"guidance":[]}`, res.Body.String())
}

func TestRouteMapsErrors(t *testing.T) {
	assert.Equal(t, http.StatusNotFound, serve(t, &fakeStore{err: ticket.ErrNotFound}).Get("/api/tickets/3/llm-usage").Code)
	assert.Equal(t, http.StatusInternalServerError, serve(t, &fakeStore{err: assert.AnError}).Get("/api/tickets/3/llm-usage").Code)
}
