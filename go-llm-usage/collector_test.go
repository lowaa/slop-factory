package llmusage_test

import (
	"fmt"
	"sort"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	llmusage "github.com/lowaa/slop-factory/go-llm-usage"
	"github.com/lowaa/slop-factory/go-llm-usage/llmusagetest"
)

type metrics struct {
	t        *testing.T
	registry *prometheus.Registry
}

func newMetrics(t *testing.T) (*llmusage.Collector, *metrics) {
	c, err := llmusage.NewCollector(&llmusage.CollectorOptions{Models: testPrices})
	require.NoError(t, err)
	reg := prometheus.NewRegistry()
	require.NoError(t, reg.Register(c))
	return c, &metrics{t: t, registry: reg}
}

func (m *metrics) families() map[string]*dto.MetricFamily {
	m.t.Helper()
	got, err := m.registry.Gather()
	require.NoError(m.t, err)
	out := map[string]*dto.MetricFamily{}
	for _, f := range got {
		out[f.GetName()] = f
	}
	return out
}

// value sums the series of name whose labels include want; 0 if none.
func (m *metrics) value(name string, want map[string]string) float64 {
	m.t.Helper()
	f, ok := m.families()[name]
	if !ok {
		return 0
	}
	var sum float64
	for _, metric := range f.GetMetric() {
		labels := map[string]string{}
		for _, l := range metric.GetLabel() {
			labels[l.GetName()] = l.GetValue()
		}
		match := true
		for k, v := range want {
			if labels[k] != v {
				match = false
			}
		}
		if match {
			sum += metric.GetCounter().GetValue()
		}
	}
	return sum
}

var opusLabels = map[string]string{
	"provider": "anthropic", "model": "claude-opus-5-5", "harness": "claude", "billing_mode": "subscription",
}

func with(base map[string]string, extra ...string) map[string]string {
	out := map[string]string{}
	for k, v := range base {
		out[k] = v
	}
	for i := 0; i+1 < len(extra); i += 2 {
		out[extra[i]] = extra[i+1]
	}
	return out
}

func TestCollector_NamesAndLabels(t *testing.T) {
	c, m := newMetrics(t)
	d := llmusagetest.Sample("anthropic", "claude-opus-5-5", 1)
	d.ReportedCostMicros, d.EstimatedCostMicros = ptr(1), ptr(2)
	d.Unpriced = true
	d.Tools = []llmusage.ToolUsage{{Tool: "Bash", Command: "git status", Calls: 1, Errors: 1, DurationMs: 1, ResultBytes: 1}}
	require.NoError(t, c.Add(d))

	base := []string{"billing_mode", "harness", "model", "provider"}
	tool := append(append([]string(nil), base...), "command", "tool_category")
	sort.Strings(tool)
	want := map[string][]string{
		"llm_requests_total":              base,
		"llm_tokens_total":                sorted(append(append([]string(nil), base...), "type")),
		"llm_cost_microdollars_total":     sorted(append(append([]string(nil), base...), "cost")),
		"llm_unpriced_requests_total":     base,
		"llm_tool_calls_total":            tool,
		"llm_tool_errors_total":           tool,
		"llm_tool_duration_seconds_total": tool,
		"llm_tool_result_bytes_total":     tool,
	}
	families := m.families()
	require.Len(t, families, len(want))
	for name, labels := range want {
		f, ok := families[name]
		require.True(t, ok, name)
		assert.Equal(t, dto.MetricType_COUNTER, f.GetType(), name)
		for _, metric := range f.GetMetric() {
			var got []string
			for _, l := range metric.GetLabel() {
				got = append(got, l.GetName())
			}
			assert.Equal(t, labels, sorted(got), name)
		}
	}
}

func sorted(s []string) []string {
	sort.Strings(s)
	return s
}

func TestCollector_Add(t *testing.T) {
	c, m := newMetrics(t)
	d := llmusagetest.Sample("anthropic", "claude-opus-5-5", 2)
	d.EstimatedCostMicros = ptr(1500)
	d.Tools = []llmusage.ToolUsage{
		{Tool: "Bash", Command: "git status", Calls: 3, Errors: 1, DurationMs: 2500, ResultBytes: 100},
		{Tool: "Bash", Command: "rogue-tool --x", Calls: 1},
		{Tool: "mcp__slop-haus__ticket_get", Calls: 4, ResultBytes: 10},
		{Category: "web", Tool: "Read", Calls: 1},
	}
	require.NoError(t, c.Add(d))
	require.NoError(t, c.Add(d))

	assert.Equal(t, 4.0, m.value("llm_requests_total", opusLabels))
	for typ, want := range map[string]float64{"input": 400, "output": 200, "cache_read": 4000, "cache_write": 800, "reasoning": 40} {
		assert.Equal(t, want, m.value("llm_tokens_total", with(opusLabels, "type", typ)), typ)
	}
	assert.Equal(t, 3000.0, m.value("llm_cost_microdollars_total", with(opusLabels, "cost", "estimated")))
	assert.Equal(t, 0.0, m.value("llm_cost_microdollars_total", with(opusLabels, "cost", "reported")), "no reported cost, no series")
	assert.Equal(t, 0.0, m.value("llm_unpriced_requests_total", opusLabels))

	gitStatus := with(opusLabels, "tool_category", "bash", "command", "git status")
	assert.Equal(t, 6.0, m.value("llm_tool_calls_total", gitStatus))
	assert.Equal(t, 2.0, m.value("llm_tool_errors_total", gitStatus))
	assert.Equal(t, 5.0, m.value("llm_tool_duration_seconds_total", gitStatus))
	assert.Equal(t, 200.0, m.value("llm_tool_result_bytes_total", gitStatus))
	assert.Equal(t, 2.0, m.value("llm_tool_calls_total", with(opusLabels, "tool_category", "bash", "command", "other")),
		"a command outside the allowlist is labelled other")
	assert.Equal(t, 8.0, m.value("llm_tool_calls_total", with(opusLabels, "tool_category", "mcp:slop-haus", "command", "")))
	assert.Equal(t, 2.0, m.value("llm_tool_calls_total", with(opusLabels, "tool_category", "file_read")),
		"the collector categorises the raw tool name itself, whatever Category says")
}

func TestCollector_Unpriced(t *testing.T) {
	c, m := newMetrics(t)
	d := llmusagetest.Sample("anthropic", "mystery-model", 3)
	d.Unpriced = true
	require.NoError(t, c.Add(d))
	labels := with(opusLabels, "model", "other")
	assert.Equal(t, 3.0, m.value("llm_unpriced_requests_total", labels), "an unknown model has no series of its own")
	assert.Equal(t, 0.0, m.value("llm_cost_microdollars_total", labels))
}

func TestCollector_RefusesNegativeDeltaWhole(t *testing.T) {
	c, m := newMetrics(t)
	d := llmusagetest.Sample("anthropic", "claude-opus-5-5", 2)
	d.Tools = []llmusage.ToolUsage{{Tool: "Read", Calls: -1}}
	assert.ErrorIs(t, c.Add(d), llmusage.ErrNegativeDelta)
	assert.Equal(t, 0.0, m.value("llm_requests_total", nil), "nothing of a shrunk delta is counted")
}

func TestCollector_RefusesUnboundedLabels(t *testing.T) {
	for name, mutate := range map[string]func(r *llmusage.Record){
		"harness":  func(r *llmusage.Record) { r.Harness = "codex" },
		"billing":  func(r *llmusage.Record) { r.BillingMode = "free" },
		"model":    func(r *llmusage.Record) { r.Model = "bad model\n" },
		"provider": func(r *llmusage.Record) { r.Provider = "" },
		"tool":     func(r *llmusage.Record) { r.Tools = []llmusage.ToolUsage{{Tool: "bad tool", Calls: 1}} },
	} {
		t.Run(name, func(t *testing.T) {
			c, m := newMetrics(t)
			d := llmusagetest.Sample("anthropic", "claude-opus-5-5", 1)
			mutate(d)
			assert.ErrorIs(t, c.Add(d), llmusage.ErrInvalid)
			assert.Equal(t, 0.0, m.value("llm_requests_total", nil))
		})
	}
}

func TestCollector_BoundsReporterChosenLabels(t *testing.T) {
	c, m := newMetrics(t)
	for i := 0; i < 3; i++ {
		d := llmusagetest.Sample(fmt.Sprintf("provider-%d", i), fmt.Sprintf("model-%d", i), 1)
		require.NoError(t, c.Add(d))
	}
	assert.Equal(t, 3.0, m.value("llm_requests_total", map[string]string{"provider": "other", "model": "other"}))
	assert.Len(t, m.families()["llm_requests_total"].GetMetric(), 1, "three made-up providers and models, one series")

	known := llmusagetest.Sample("openrouter", "anthropic/claude-opus-5.5", 1)
	require.NoError(t, c.Add(known))
	assert.Equal(t, 1.0, m.value("llm_requests_total", map[string]string{"provider": "openrouter", "model": "anthropic/claude-opus-5.5"}))
}

func TestCollector_BoundsMCPServers(t *testing.T) {
	c, m := newMetrics(t)
	add := func(server string) {
		d := llmusagetest.Sample("anthropic", "claude-opus-5-5", 1)
		d.Tools = []llmusage.ToolUsage{{Tool: "mcp__" + server + "__x", Calls: 1}}
		require.NoError(t, c.Add(d))
	}
	// slop-haus is pre-admitted, so this fills the rest of the budget.
	for i := 0; i < llmusage.MaxMCPServerLabels-1; i++ {
		add(fmt.Sprintf("server-%d", i))
	}
	add("one-too-many")
	add("server-0")
	add("slop-haus")

	calls := func(category string) float64 {
		return m.value("llm_tool_calls_total", map[string]string{"tool_category": category})
	}
	assert.Equal(t, 0.0, calls("mcp:one-too-many"), "past the budget a server has no series")
	assert.Equal(t, 1.0, calls("mcp:other"))
	assert.Equal(t, 2.0, calls("mcp:server-0"), "admitted servers keep their series")
	assert.Equal(t, 1.0, calls("mcp:slop-haus"))
}

func TestNewCollector_RequiresModels(t *testing.T) {
	_, err := llmusage.NewCollector(&llmusage.CollectorOptions{})
	assert.Error(t, err)
}
