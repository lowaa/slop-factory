package llmusage_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-auth/ratelimit"
	usagelib "github.com/lowaa/slop-factory/go-llm-usage"
	"github.com/lowaa/slop-factory/go-llm-usage/pricing"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/agent"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/apiserver"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/llmusage"
)

// wire is the usage hook over HTTP, behind the agent auth middleware
// cmd/api installs, against the real store. Tenant A has two agents,
// tenant B one.
type wire struct {
	*fixture
	mux      *http.ServeMux
	registry *prometheus.Registry
	svc      *llmusage.Service
	builder  *agent.Agent
	reviewer *agent.Agent
	outsider *agent.Agent
	otherCtx context.Context
	// extra are more agents' credentials, by token.
	extra map[string]authn.Identity
}

const (
	builderToken  = "agent-builder"
	reviewerToken = "agent-reviewer"
	outsiderToken = "agent-outsider"
)

func newWire(t *testing.T, limit int) *wire {
	t.Helper()
	f := newFixture(t)
	w := &wire{fixture: f, otherCtx: db.WithTenant(context.Background(), dbtest.NewTenant(t, f.database)), extra: map[string]authn.Identity{}}
	w.builder, w.reviewer = f.agent(t, f.ctx, "builder"), f.agent(t, f.ctx, "reviewer")
	w.outsider = f.agent(t, w.otherCtx, "outsider")

	prices := pricing.Default()
	collector, err := usagelib.NewCollector(&usagelib.CollectorOptions{Models: prices})
	require.NoError(t, err)
	w.registry = prometheus.NewRegistry()
	require.NoError(t, w.registry.Register(collector))
	svc, err := llmusage.NewService(&llmusage.Options{
		Store:     f.store,
		Collector: collector,
		Pricer:    prices,
		Limit:     &ratelimit.Config{Name: "llm_usage", Limit: limit, Window: time.Hour, MaxKeys: 100},
	}, slog.DiscardHandler)
	require.NoError(t, err)
	w.svc = svc

	w.mux = http.NewServeMux()
	api := humago.New(w.mux, apiserver.NewConfig("test", "0.0.0"))
	logger := slog.New(slog.DiscardHandler)
	api.UseMiddleware(authn.DefaultDeny(api, logger))
	api.UseMiddleware(authn.HumaMiddleware(api, func(_ context.Context, token string) (authn.Identity, error) {
		if id, ok := w.extra[token]; ok {
			return id, nil
		}
		switch token {
		case builderToken:
			return w.builder, nil
		case reviewerToken:
			return w.reviewer, nil
		case outsiderToken:
			return w.outsider, nil
		}
		return nil, errors.New("unknown agent token")
	}, audit.New(slog.DiscardHandler, nil), logger))
	llmusage.RegisterRoutes(api, llmusage.NewHandler(svc, slog.DiscardHandler))
	return w
}

func (w *wire) post(t *testing.T, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/hooks/usage", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	w.mux.ServeHTTP(rec, req)
	return rec
}

// counter sums one llm_* counter across its series.
func (w *wire) counter(t *testing.T, name string) float64 {
	t.Helper()
	families, err := w.registry.Gather()
	require.NoError(t, err)
	total := 0.0
	for _, family := range families {
		if family.GetName() != name {
			continue
		}
		for _, m := range family.GetMetric() {
			total += m.GetCounter().GetValue()
		}
	}
	return total
}

type reportBody struct {
	TranscriptKey string            `json:"transcript_key"`
	Harness       string            `json:"harness"`
	SessionID     string            `json:"session_id"`
	Scope         string            `json:"scope"`
	Records       []usagelib.Record `json:"records"`

	Source           string `json:"source,omitempty"`
	ReporterRevision int    `json:"reporter_revision,omitempty"`
}

func piReport(key string, records ...usagelib.Record) *reportBody {
	return &reportBody{TranscriptKey: key, Harness: "pi", SessionID: "pi-session", Scope: "main", Records: records}
}

// piRecord is a pi record of n requests with a bash tool, over minutes
// 10..20 after t0.
func piRecord(n int64) usagelib.Record {
	r := spanning(10, 20, n)
	cost := 1000 * n
	r.ReportedCostMicros = &cost
	r.Tools = []usagelib.ToolUsage{{Tool: "bash", Command: "git status", Calls: n, DurationMs: 10 * n, ResultBytes: 100 * n}}
	return r
}

func (b *reportBody) json(t *testing.T) string {
	t.Helper()
	raw, err := json.Marshal(b)
	require.NoError(t, err)
	return string(raw)
}

func (w *wire) rowCount(t *testing.T, ctx context.Context) (usage, tools int) {
	t.Helper()
	require.NoError(t, w.database.QueryRow(ctx, `SELECT count(*) FROM llm_usage`).Scan(&usage))
	require.NoError(t, w.database.QueryRow(ctx, `SELECT count(*) FROM llm_tool_usage`).Scan(&tools))
	return usage, tools
}

func TestUsageHookStoresTheReportUnderTheCallersAgent(t *testing.T) {
	w := newWire(t, 100)
	ticket := w.ticket(t, w.ctx)
	w.lease(t, w.ctx, w.builder.ID, lease{ticket: ticket, from: 0, to: 60})

	rec := w.post(t, builderToken, piReport("pi-session", piRecord(2)).json(t))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.JSONEq(t, fmt.Sprintf(`{"scope":"ticket","ticket_id":%d}`, ticket), rec.Body.String())

	var (
		agentID              string
		estimated, reported  *int64
		priceVersion, cat    string
		scopeHint, sessionID string
	)
	require.NoError(t, w.database.QueryRow(w.ctx, `
		SELECT u.agent_id, u.estimated_cost_microdollars, u.reported_cost_microdollars, u.price_version,
		       u.scope_hint, u.session_id, t.category
		  FROM llm_usage u JOIN llm_tool_usage t USING (tenant_id, agent_id, transcript_key, provider, model, harness, billing_mode)`).
		Scan(&agentID, &estimated, &reported, &priceVersion, &scopeHint, &sessionID, &cat))
	assert.Equal(t, w.builder.ID.String(), agentID)
	require.NotNil(t, estimated, "the server prices what it stores")
	assert.Positive(t, *estimated)
	assert.Equal(t, pricing.Default().Version(), priceVersion)
	assert.Equal(t, int64(2000), *reported)
	assert.Equal(t, "main", scopeHint)
	assert.Equal(t, "pi-session", sessionID)
	assert.Equal(t, "bash", cat, "the server categorises the raw tool name")
}

// A hook that fires twice, or a retry, is the same full totals again: the
// rows and the metrics stay as they were.
func TestRepeatingAReportChangesNothing(t *testing.T) {
	w := newWire(t, 100)
	body := piReport("pi-session", piRecord(2)).json(t)
	require.Equal(t, http.StatusOK, w.post(t, builderToken, body).Code)
	requests, calls := w.counter(t, "llm_requests_total"), w.counter(t, "llm_tool_calls_total")
	assert.Equal(t, 2.0, requests)
	assert.Equal(t, 2.0, calls)

	require.Equal(t, http.StatusOK, w.post(t, builderToken, body).Code)
	assert.Equal(t, requests, w.counter(t, "llm_requests_total"))
	assert.Equal(t, calls, w.counter(t, "llm_tool_calls_total"))
	usage, tools := w.rowCount(t, w.ctx)
	assert.Equal(t, 1, usage)
	assert.Equal(t, 1, tools)

	// And grown totals add only the growth.
	require.Equal(t, http.StatusOK, w.post(t, builderToken, piReport("pi-session", piRecord(5)).json(t)).Code)
	assert.Equal(t, 5.0, w.counter(t, "llm_requests_total"))
}

// Who a report is for is the token's business alone.
func TestTheTokenDecidesTenantAndAgent(t *testing.T) {
	w := newWire(t, 100)
	body := piReport("shared-key", piRecord(1)).json(t)
	require.Equal(t, http.StatusOK, w.post(t, builderToken, body).Code)
	require.Equal(t, http.StatusOK, w.post(t, outsiderToken, body).Code)

	usage, _ := w.rowCount(t, w.ctx)
	assert.Equal(t, 1, usage, "tenant A holds only its own agent's report")
	var agentID string
	require.NoError(t, w.database.QueryRow(w.otherCtx, `SELECT agent_id FROM llm_usage`).Scan(&agentID))
	assert.Equal(t, w.outsider.ID.String(), agentID, "tenant B's report is stored in tenant B, as its agent")

	t.Run("the body cannot name an agent", func(t *testing.T) {
		withAgent := strings.Replace(body, `{`, `{"agent_id":"`+w.reviewer.ID.String()+`",`, 1)
		rec := w.post(t, builderToken, withAgent)
		assert.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
	})
	t.Run("no token, no report", func(t *testing.T) {
		assert.Equal(t, http.StatusUnauthorized, w.post(t, "", body).Code)
		assert.Equal(t, http.StatusUnauthorized, w.post(t, "forged", body).Code)
	})
}

func TestInvalidReportsAreRefused(t *testing.T) {
	tooManyTools := piRecord(1)
	for i := range 1025 {
		tooManyTools.Tools = append(tooManyTools.Tools, usagelib.ToolUsage{Tool: fmt.Sprintf("tool_%d", i), Calls: 1})
	}
	split := func(r usagelib.Record, n int) []usagelib.Record {
		// MaxToolsPerRecord is 512, so spread them over records to reach
		// the per-report cap rather than the per-record one.
		var out []usagelib.Record
		for i := 0; i < n; i++ {
			c := *r.Clone()
			c.Model = fmt.Sprintf("model-%d", i)
			c.Tools = r.Tools[i*len(r.Tools)/n : (i+1)*len(r.Tools)/n]
			out = append(out, c)
		}
		return out
	}
	for _, tc := range []struct {
		name   string
		mutate func(b *reportBody)
	}{
		{"negative tokens", func(b *reportBody) { b.Records[0].InputTokens = -1 }},
		{"a full command line", func(b *reportBody) {
			b.Records[0].Tools[0].Command = "curl -H 'Authorization: Bearer x' https://example"
		}},
		{"an unknown harness", func(b *reportBody) { b.Harness = "other"; b.Records[0].Harness = "other" }},
		{"a record from another harness", func(b *reportBody) { b.Records[0].Harness = usagelib.HarnessClaude }},
		{"a bad scope", func(b *reportBody) { b.Scope = "ticket:12" }},
		{"a bad session id", func(b *reportBody) { b.SessionID = "has spaces" }},
		{"a bad transcript key", func(b *reportBody) { b.TranscriptKey = "../../etc" + strings.Repeat("x", 300) }},
		{"no records", func(b *reportBody) { b.Records = nil }},
		{"too many records", func(b *reportBody) {
			b.Records = nil
			for i := range 33 {
				r := piRecord(1)
				r.Model = fmt.Sprintf("model-%d", i)
				b.Records = append(b.Records, r)
			}
		}},
		{"too many tools", func(b *reportBody) { b.Records = split(tooManyTools, 3) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newWire(t, 100)
			b := piReport("k", piRecord(1))
			tc.mutate(b)
			rec := w.post(t, builderToken, b.json(t))
			assert.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
			usage, _ := w.rowCount(t, w.ctx)
			assert.Zero(t, usage, "nothing is stored")
			assert.Zero(t, w.counter(t, "llm_requests_total"), "nothing is counted")
		})
	}
}

// Reporters from before the rename still send self-review, at the top level
// and on each record; the server stores it as slop-review.
func TestALegacySelfReviewReportIsStoredAsSlopReview(t *testing.T) {
	w := newWire(t, 100)
	b := piReport("review-session", piRecord(1))
	b.Harness = "self-review"
	b.Records[0].Harness = "self-review"
	b.Records[0].Tools = nil

	rec := w.post(t, builderToken, b.json(t))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	rows, err := w.database.Query(w.ctx, `SELECT harness FROM llm_usage`)
	require.NoError(t, err)
	defer rows.Close()
	var stored []string
	for rows.Next() {
		var h string
		require.NoError(t, rows.Scan(&h))
		stored = append(stored, h)
	}
	require.NoError(t, rows.Err())
	assert.Equal(t, []string{"slop-review"}, stored)
}

func TestAnOversizedBodyIsRefusedUnread(t *testing.T) {
	w := newWire(t, 100)
	b := piReport("k", piRecord(1))
	b.SessionID = strings.Repeat("x", llmusage.MaxBodyBytes)
	assert.Equal(t, http.StatusRequestEntityTooLarge, w.post(t, builderToken, b.json(t)).Code)
}

func TestReportsAreRateLimitedPerAgent(t *testing.T) {
	w := newWire(t, 2)
	body := piReport("k", piRecord(1)).json(t)
	require.Equal(t, http.StatusOK, w.post(t, builderToken, body).Code)
	require.Equal(t, http.StatusOK, w.post(t, builderToken, body).Code)
	assert.Equal(t, http.StatusTooManyRequests, w.post(t, builderToken, body).Code)
	assert.Equal(t, http.StatusOK, w.post(t, reviewerToken, body).Code, "another agent has a budget of its own")
}

type failingStore struct{}

func (failingStore) Upsert(context.Context, *llmusage.Transcript, []usagelib.Record) ([]usagelib.Record, *llmusage.Attribution, error) {
	return nil, nil, errors.New("database down")
}

// A store that fails is the server's problem, not the report's: a 500 the
// client drops, with nothing counted and nothing said about why.
func TestAStoreFailureIsAServerError(t *testing.T) {
	prices := pricing.Default()
	collector, err := usagelib.NewCollector(&usagelib.CollectorOptions{Models: prices})
	require.NoError(t, err)
	registry := prometheus.NewRegistry()
	require.NoError(t, registry.Register(collector))
	svc, err := llmusage.NewService(&llmusage.Options{
		Store: failingStore{}, Collector: collector, Pricer: prices,
		Limit: &ratelimit.Config{Name: "llm_usage", Limit: 10, Window: time.Hour, MaxKeys: 10},
	}, slog.DiscardHandler)
	require.NoError(t, err)
	caller := &agent.Agent{ID: uuid.New(), Tenant: uuid.New()}
	ctx := authn.NewContext(context.Background(), caller)

	b := piReport("k", piRecord(1))
	_, err = llmusage.NewHandler(svc, slog.DiscardHandler).IngestUsage(ctx, &llmusage.IngestUsageRequest{Body: llmusage.UsageBody{
		TranscriptKey: b.TranscriptKey, Harness: llmusage.ReportHarness(b.Harness), SessionID: b.SessionID, Scope: b.Scope, Records: b.Records,
	}})
	var status huma.StatusError
	require.ErrorAs(t, err, &status)
	assert.Equal(t, http.StatusInternalServerError, status.GetStatus())
	assert.NotContains(t, err.Error(), "database down")
	families, err := registry.Gather()
	require.NoError(t, err)
	for _, family := range families {
		for _, m := range family.GetMetric() {
			assert.Zero(t, m.GetCounter().GetValue(), family.GetName())
		}
	}
}

func (failingStore) Declare(context.Context, uuid.UUID, *int64, *int) (*llmusage.Declaration, error) {
	return nil, errors.New("database down")
}
