package llmusage

import (
	"errors"
	"strings"
	"sync"

	"github.com/prometheus/client_golang/prometheus"
)

// Metric label values for llm_tokens_total's type and
// llm_cost_microdollars_total's cost.
const (
	TokenTypeInput      = "input"
	TokenTypeOutput     = "output"
	TokenTypeCacheRead  = "cache_read"
	TokenTypeCacheWrite = "cache_write"
	// TokenTypeReasoning is part of TokenTypeOutput, not in addition to it:
	// never sum the two.
	TokenTypeReasoning = "reasoning"

	CostReported  = "reported"
	CostEstimated = "estimated"
)

// ErrNegativeDelta is returned by Collector.Add for a delta that would
// decrement a counter.
var ErrNegativeDelta = errors.New("llmusage: negative delta")

var (
	baseLabels = []string{"provider", "model", "harness", "billing_mode"}
	toolLabels = append(append([]string(nil), baseLabels...), "tool_category", "command")
)

// LabelOther is the label value for a provider, model or MCP server outside
// the collector's bounded sets. The database keeps the real value.
const LabelOther = "other"

// MaxMCPServerLabels is how many distinct MCP servers get a tool_category
// of their own; tools of any further server are labelled mcp:other.
const MaxMCPServerLabels = 32

// labelProviders are the providers that keep their own label value: those
// the harnesses report today.
var labelProviders = map[string]bool{
	"anthropic": true, "openai": true, "openai-codex": true, "openrouter": true, "chatgpt": true,
	"google": true, "github-copilot": true,
}

// ModelSet says which model names are known. pricing.Table is one.
type ModelSet interface {
	Known(model string) bool
}

// CollectorOptions configure a Collector.
type CollectorOptions struct {
	// Models are the model names that keep their own label value: pass the
	// Pricer's table, so every model with an estimate has its own series and
	// any other is LabelOther. Required.
	Models ModelSet
}

// Collector is the Prometheus side of LLM usage, the same in every app so
// one Grafana view covers them all. Register it on the app's own registry.
//
// Every label comes from a bounded set, because a reporter chooses the
// values and each new one would be a series held until restart: provider
// from a fixed list, model from the known models, harness and billing_mode
// from their enums, tool_category from Categorize with at most
// MaxMCPServerLabels MCP servers, and command through CommandLabel.
// Anything else is LabelOther. The app label comes from the scrape job.
// Nothing is labelled by ticket, run, session or agent id: those are
// unbounded, and per-piece-of-work usage is a database query, not a metric.
//
// Its methods are safe for concurrent use.
type Collector struct {
	models ModelSet

	mu         sync.Mutex
	mcpServers map[string]bool

	requests    *prometheus.CounterVec
	tokens      *prometheus.CounterVec
	cost        *prometheus.CounterVec
	unpriced    *prometheus.CounterVec
	toolCalls   *prometheus.CounterVec
	toolErrors  *prometheus.CounterVec
	toolSeconds *prometheus.CounterVec
	toolBytes   *prometheus.CounterVec
}

// NewCollector builds the counters, unregistered.
func NewCollector(opts *CollectorOptions) (*Collector, error) {
	if opts == nil || opts.Models == nil {
		return nil, errors.New("llmusage: CollectorOptions needs Models")
	}
	counter := func(name, help string, labels []string) *prometheus.CounterVec {
		return prometheus.NewCounterVec(prometheus.CounterOpts{Name: name, Help: help}, labels)
	}
	return &Collector{
		models: opts.Models,
		// slop-haus's own server is pre-admitted so others can't crowd it out.
		mcpServers: map[string]bool{SlopHausMCPServer: true},
		requests:   counter("llm_requests_total", "LLM requests.", baseLabels),
		tokens: counter("llm_tokens_total",
			"LLM tokens by type. input excludes cache reads and writes; reasoning is part of output, not in addition to it.",
			append(append([]string(nil), baseLabels...), "type")),
		cost: counter("llm_cost_microdollars_total",
			"LLM cost in micro-dollars, as the provider reported it or estimated from the price table. Subscription cost is an API-equivalent estimate, not money spent: never add billing modes together.",
			append(append([]string(nil), baseLabels...), "cost")),
		unpriced: counter("llm_unpriced_requests_total",
			"LLM requests for a model the price table doesn't know, so their cost has no estimate.", baseLabels),
		toolCalls:   counter("llm_tool_calls_total", "Tool calls.", toolLabels),
		toolErrors:  counter("llm_tool_errors_total", "Tool calls that failed.", toolLabels),
		toolSeconds: counter("llm_tool_duration_seconds_total", "Time spent in tool calls.", toolLabels),
		toolBytes:   counter("llm_tool_result_bytes_total", "Bytes of tool results returned to the model.", toolLabels),
	}, nil
}

func (c *Collector) vecs() []*prometheus.CounterVec {
	return []*prometheus.CounterVec{c.requests, c.tokens, c.cost, c.unpriced, c.toolCalls, c.toolErrors, c.toolSeconds, c.toolBytes}
}

// Describe implements prometheus.Collector.
func (c *Collector) Describe(ch chan<- *prometheus.Desc) {
	for _, v := range c.vecs() {
		v.Describe(ch)
	}
}

// Collect implements prometheus.Collector.
func (c *Collector) Collect(ch chan<- prometheus.Metric) {
	for _, v := range c.vecs() {
		v.Collect(ch)
	}
}

// Add counts a delta: what an upsert actually changed, never a report's
// full totals. Reports are totals that overwrite earlier ones and can
// arrive more than once (a hook firing twice for one transcript), so a
// caller passes Delta(stored, new) -- the difference its upsert applied --
// and a retry or repeat then adds nothing. Service does this for you.
//
// Counters only go up, so a delta that shrank is refused whole with
// ErrNegativeDelta rather than partly applied.
func (c *Collector) Add(delta *Record) error {
	if err := delta.validateLabels(); err != nil {
		return err
	}
	if delta.Shrank() {
		return ErrNegativeDelta
	}
	base := []string{c.providerLabel(delta.Provider), c.modelLabel(delta.Model), string(delta.Harness), string(delta.BillingMode)}
	with := func(extra ...string) []string { return append(append([]string(nil), base...), extra...) }

	c.requests.WithLabelValues(base...).Add(float64(delta.Requests))
	for typ, n := range map[string]int64{
		TokenTypeInput:      delta.InputTokens,
		TokenTypeOutput:     delta.OutputTokens,
		TokenTypeCacheRead:  delta.CacheReadTokens,
		TokenTypeCacheWrite: delta.CacheWriteTokens,
		TokenTypeReasoning:  delta.ReasoningTokens,
	} {
		c.tokens.WithLabelValues(with(typ)...).Add(float64(n))
	}
	if delta.ReportedCostMicros != nil {
		c.cost.WithLabelValues(with(CostReported)...).Add(float64(*delta.ReportedCostMicros))
	}
	if delta.EstimatedCostMicros != nil {
		c.cost.WithLabelValues(with(CostEstimated)...).Add(float64(*delta.EstimatedCostMicros))
	}
	if delta.Unpriced {
		c.unpriced.WithLabelValues(base...).Add(float64(delta.Requests))
	}
	for i := range delta.Tools {
		u := &delta.Tools[i]
		// Categorised here rather than trusting u.Category, so the label
		// is bounded whatever the caller filled in.
		category, _ := Categorize(delta.Harness, u.Tool)
		labels := with(c.categoryLabel(category), CommandLabel(u.Command))
		c.toolCalls.WithLabelValues(labels...).Add(float64(u.Calls))
		c.toolErrors.WithLabelValues(labels...).Add(float64(u.Errors))
		c.toolSeconds.WithLabelValues(labels...).Add(float64(u.DurationMs) / 1000)
		c.toolBytes.WithLabelValues(labels...).Add(float64(u.ResultBytes))
	}
	return nil
}

func (c *Collector) providerLabel(provider string) string {
	if labelProviders[provider] {
		return provider
	}
	return LabelOther
}

func (c *Collector) modelLabel(model string) string {
	if c.models.Known(model) {
		return model
	}
	return LabelOther
}

// categoryLabel admits MCP servers first come, first served, up to
// MaxMCPServerLabels: the servers agents use are configured per machine, so
// there is no fixed list to check against.
func (c *Collector) categoryLabel(category string) string {
	server, ok := strings.CutPrefix(category, CategoryMCPPrefix)
	if !ok {
		return category
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.mcpServers[server] {
		return category
	}
	if len(c.mcpServers) < MaxMCPServerLabels {
		c.mcpServers[server] = true
		return category
	}
	return CategoryMCPPrefix + LabelOther
}
