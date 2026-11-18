package llmusage_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	llmusage "github.com/lowaa/slop-factory/go-llm-usage"
	"github.com/lowaa/slop-factory/go-llm-usage/llmusagetest"
	"github.com/lowaa/slop-factory/go-llm-usage/pricing"
)

var testPrices = pricing.NewTable("2026-10-01.test",
	map[string]pricing.Price{"anthropic/claude-opus-5.5": {Input: 1_000_000, Output: 2_000_000, CacheRead: 100_000, CacheWrite: 1_500_000}},
	map[string]string{"claude-opus-5-5": "anthropic/claude-opus-5.5"})

type failingStore struct{ err error }

func (s *failingStore) Upsert(context.Context, llmusage.TranscriptKey, []llmusage.Record) ([]llmusage.Record, error) {
	return nil, s.err
}

type serviceFixture struct {
	svc     *llmusage.Service
	store   *llmusagetest.MemStore
	metrics *metrics
	logs    *bytes.Buffer
}

func newService(t *testing.T, store llmusage.Store, pricer llmusage.Pricer) *serviceFixture {
	collector, m := newMetrics(t)
	logs := &bytes.Buffer{}
	svc, err := llmusage.NewService(&llmusage.Options{
		Store:     store,
		Collector: collector,
		Pricer:    pricer,
		Logger:    slog.New(slog.NewTextHandler(logs, nil)),
	})
	require.NoError(t, err)
	mem, _ := store.(*llmusagetest.MemStore)
	return &serviceFixture{svc: svc, store: mem, metrics: m, logs: logs}
}

// ingest ingests and commits one report.
func (f *serviceFixture) ingest(t *testing.T, key llmusage.TranscriptKey, records ...*llmusage.Record) *llmusage.Pending {
	t.Helper()
	report := make([]llmusage.Record, len(records))
	for i, r := range records {
		report[i] = *r
	}
	p, err := f.svc.Ingest(context.Background(), key, report)
	require.NoError(t, err)
	f.svc.Commit(p)
	return p
}

func (f *serviceFixture) requests() float64 { return f.metrics.value("llm_requests_total", nil) }

func opus(n int64) *llmusage.Record {
	r := llmusagetest.Sample("anthropic", "claude-opus-5-5", n)
	r.Tools = []llmusage.ToolUsage{{Tool: "Bash", Command: "git status", Calls: n}}
	return r
}

func TestService_DeltaAppliedOnce(t *testing.T) {
	f := newService(t, llmusagetest.NewMemStore(), testPrices)

	f.ingest(t, "t1", opus(3))
	assert.Equal(t, 3.0, f.requests())

	f.ingest(t, "t1", opus(3))
	assert.Equal(t, 3.0, f.requests(), "a repeated report adds nothing")

	f.ingest(t, "t1", opus(5))
	assert.Equal(t, 5.0, f.requests(), "growing totals add only the difference")
	assert.Equal(t, 5.0, f.metrics.value("llm_tool_calls_total", map[string]string{"command": "git status"}))
	assert.Equal(t, 500.0, f.metrics.value("llm_tokens_total", map[string]string{"type": "input"}))

	f.ingest(t, "t2", opus(2))
	assert.Equal(t, 7.0, f.requests(), "another transcript counts on its own")
}

func TestService_ShrinkingTotalsAddNothing(t *testing.T) {
	f := newService(t, llmusagetest.NewMemStore(), testPrices)
	f.ingest(t, "t1", opus(5))

	f.ingest(t, "t1", opus(4))
	assert.Equal(t, 5.0, f.requests())
	assert.Contains(t, f.logs.String(), "shrank")

	f.ingest(t, "t1", llmusagetest.Sample("anthropic", "claude-sonnet-5-5", 1))
	assert.Equal(t, 6.0, f.requests(), "a dropped model adds nothing; the new one counts")
	assert.Contains(t, f.logs.String(), "dropped")
}

func TestService_UpsertFailureAddsNothing(t *testing.T) {
	boom := errors.New("boom")
	f := newService(t, &failingStore{err: boom}, testPrices)
	p, err := f.svc.Ingest(context.Background(), "t1", []llmusage.Record{*opus(3)})
	require.ErrorIs(t, err, boom)
	assert.Nil(t, p)
	assert.Equal(t, 0.0, f.requests())
}

func TestService_NothingCountedUntilCommit(t *testing.T) {
	f := newService(t, llmusagetest.NewMemStore(), testPrices)
	p, err := f.svc.Ingest(context.Background(), "t1", []llmusage.Record{*opus(3)})
	require.NoError(t, err)
	assert.Equal(t, 0.0, f.requests())

	f.svc.Commit(p)
	f.svc.Commit(p)
	assert.Equal(t, 3.0, f.requests(), "a second Commit of one Pending adds nothing")
}

func TestService_InvalidReportIsNotStored(t *testing.T) {
	f := newService(t, llmusagetest.NewMemStore(), testPrices)
	bad := opus(1)
	bad.Requests = -1
	_, err := f.svc.Ingest(context.Background(), "t1", []llmusage.Record{*bad})
	require.ErrorIs(t, err, llmusage.ErrInvalid)

	_, err = f.svc.Ingest(context.Background(), "bad key with spaces", []llmusage.Record{*opus(1)})
	require.ErrorIs(t, err, llmusage.ErrInvalid)

	previous, err := f.store.Upsert(context.Background(), "t1", nil)
	require.NoError(t, err)
	assert.Empty(t, previous)
}

func TestService_StoresCategorisedAndPricedTotals(t *testing.T) {
	f := newService(t, llmusagetest.NewMemStore(), testPrices)
	report := opus(1)
	report.EstimatedCostMicros = ptr(999_999)
	report.Tools = append(report.Tools, llmusage.ToolUsage{Category: "web", Tool: "mcp__slop-haus__ticket_get", Calls: 1})

	p := f.ingest(t, "t1", report)

	require.Len(t, p.Records, 1)
	got := p.Records[0]
	// 100 input, 50 output, 1000 cache read, 200 cache write tokens.
	assert.Equal(t, ptr(100+100+100+300), got.EstimatedCostMicros, "the client's estimate is replaced")
	assert.Equal(t, "2026-10-01.test", got.PriceVersion)
	assert.Equal(t, "bash", got.Tools[0].Category)
	assert.Equal(t, "mcp:slop-haus", got.Tools[1].Category, "the client's category is replaced")
	assert.Equal(t, ptr(999_999), report.EstimatedCostMicros, "the caller's record is untouched")
	assert.Empty(t, report.Tools[0].Category)

	stored, err := f.store.Upsert(context.Background(), "t1", nil)
	require.NoError(t, err)
	assert.Equal(t, p.Records, stored)
	assert.Equal(t, 600.0, f.metrics.value("llm_cost_microdollars_total", map[string]string{"cost": "estimated"}))
}

func TestService_PriceChangeIsNotAShrink(t *testing.T) {
	store := llmusagetest.NewMemStore()
	f := newService(t, store, testPrices)
	f.ingest(t, "t1", opus(2))

	cheaper := pricing.NewTable("2026-11-01.test",
		map[string]pricing.Price{"anthropic/claude-opus-5.5": {Input: 500_000, Output: 1_000_000, CacheRead: 50_000, CacheWrite: 750_000}},
		map[string]string{"claude-opus-5-5": "anthropic/claude-opus-5.5"})
	g := newService(t, store, cheaper)
	g.ingest(t, "t1", opus(3))

	assert.Equal(t, 1.0, g.requests(), "the growth counts even though the stored estimate was dearer")
	assert.Equal(t, 300.0, g.metrics.value("llm_cost_microdollars_total", map[string]string{"cost": "estimated"}),
		"the delta is priced at today's prices on both sides")
	assert.NotContains(t, g.logs.String(), "shrank")
}

func TestService_UnknownModelIsCountedAsUnpriced(t *testing.T) {
	f := newService(t, llmusagetest.NewMemStore(), testPrices)
	p := f.ingest(t, "t1", llmusagetest.Sample("anthropic", "mystery-model", 2))
	assert.True(t, p.Records[0].Unpriced)
	assert.Nil(t, p.Records[0].EstimatedCostMicros)
	assert.Equal(t, 2.0, f.metrics.value("llm_unpriced_requests_total", nil))
	assert.Equal(t, 0.0, f.metrics.value("llm_cost_microdollars_total", nil))
}

func TestNewService_RequiresEveryOption(t *testing.T) {
	_, err := llmusage.NewService(&llmusage.Options{Store: llmusagetest.NewMemStore()})
	assert.Error(t, err)
}

func TestService_ModelDroppedFromPriceTableStillCounts(t *testing.T) {
	store := llmusagetest.NewMemStore()
	f := newService(t, store, testPrices)
	f.ingest(t, "t1", opus(2))

	empty := pricing.NewTable("2026-12-01.test", nil, nil)
	g := newService(t, store, empty)
	p := g.ingest(t, "t1", opus(3))

	assert.True(t, p.Records[0].Unpriced)
	assert.Equal(t, 1.0, g.requests(), "losing the price loses the estimate, not the usage")
	assert.NotContains(t, g.logs.String(), "shrank")
}

type badVersionPricer struct{}

func (badVersionPricer) Estimate(r *llmusage.Record) { r.PriceVersion = "not a version" }

func TestService_PricedRecordIsValidatedAgain(t *testing.T) {
	f := newService(t, llmusagetest.NewMemStore(), badVersionPricer{})
	_, err := f.svc.Ingest(context.Background(), "t1", []llmusage.Record{*opus(1)})
	require.ErrorIs(t, err, llmusage.ErrInvalid)
	previous, err := f.store.Upsert(context.Background(), "t1", nil)
	require.NoError(t, err)
	assert.Empty(t, previous, "nothing was stored")
}
