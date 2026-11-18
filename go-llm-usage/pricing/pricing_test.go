package pricing_test

import (
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	llmusage "github.com/lowaa/slop-factory/go-llm-usage"
	"github.com/lowaa/slop-factory/go-llm-usage/llmusagetest"
	"github.com/lowaa/slop-factory/go-llm-usage/pricing"
)

func ptr(v int64) *int64 { return &v }

func record(model string, input, output, cacheRead, cacheWrite, reasoning int64) *llmusage.Record {
	r := llmusagetest.Sample("anthropic", model, 1)
	r.InputTokens, r.OutputTokens, r.CacheReadTokens, r.CacheWriteTokens, r.ReasoningTokens =
		input, output, cacheRead, cacheWrite, reasoning
	return r
}

var table = pricing.NewTable("v-test",
	map[string]pricing.Price{
		// $3, $15, $0.30 and $3.75 per million.
		"vendor/model": {Input: 3_000_000, Output: 15_000_000, CacheRead: 300_000, CacheWrite: 3_750_000},
		"vendor/cheap": {Input: 1, Output: 1, CacheRead: 1, CacheWrite: 1},
		"vendor/dear":  {Input: 1e9, Output: 1e9, CacheRead: 1e9, CacheWrite: 1e9},
		"vendor/free":  {},
	},
	map[string]string{"model-alias": "vendor/model", "dangling": "vendor/gone"})

func TestEstimate_Arithmetic(t *testing.T) {
	r := record("vendor/model", 1_000_000, 100_000, 2_000_000, 10_000, 50_000)
	table.Estimate(r)
	// 3_000_000 + 1_500_000 + 600_000 + 37_500; reasoning is inside output.
	assert.Equal(t, ptr(5_137_500), r.EstimatedCostMicros)
	assert.Equal(t, "v-test", r.PriceVersion)
	assert.False(t, r.Unpriced)
}

func TestEstimate_RoundsHalfUp(t *testing.T) {
	for tokens, want := range map[int64]int64{499_999: 0, 500_000: 1, 1_499_999: 1, 1_500_000: 2} {
		r := record("vendor/cheap", tokens, 0, 0, 0, 0)
		table.Estimate(r)
		assert.Equal(t, ptr(want), r.EstimatedCostMicros, "%d tokens", tokens)
	}
}

func TestEstimate_LargeCountsDoNotOverflow(t *testing.T) {
	// 2e12 tokens at 1e9 micro-dollars per million: the products overflow
	// int64, the result (2e15) does not.
	r := record("vendor/dear", 1e12, 1e12, 0, 0, 0)
	table.Estimate(r)
	assert.Equal(t, ptr(2e15), r.EstimatedCostMicros)
	assert.NoError(t, r.Validate())
}

func TestEstimate_PastTheCostLimitIsUnpriced(t *testing.T) {
	dearer := pricing.NewTable("v-test", map[string]pricing.Price{"vendor/x": {Input: 1e13}}, nil)
	for name, tokens := range map[string]int64{
		"above MaxCostMicros": 1e10, // 1e17
		"beyond int64":        1e12, // 1e19
	} {
		t.Run(name, func(t *testing.T) {
			r := record("vendor/x", tokens, 0, 0, 0, 0)
			dearer.Estimate(r)
			assert.Nil(t, r.EstimatedCostMicros)
			assert.True(t, r.Unpriced)
		})
	}
}

func TestEstimate_FreeModelIsPricedAtZero(t *testing.T) {
	r := record("vendor/free", 1000, 1000, 1000, 1000, 0)
	table.Estimate(r)
	assert.Equal(t, ptr(0), r.EstimatedCostMicros)
	assert.False(t, r.Unpriced, "free is a price, not an unknown")
}

func TestKnown(t *testing.T) {
	assert.True(t, table.Known("vendor/model"))
	assert.True(t, table.Known("model-alias"))
	assert.False(t, table.Known("dangling"), "an alias to a missing entry is unknown")
	assert.False(t, table.Known("vendor/unknown"))
}

func TestNewTable_CopiesItsMaps(t *testing.T) {
	prices := map[string]pricing.Price{"a/b": {Input: 1}}
	aliases := map[string]string{"b": "a/b"}
	tbl := pricing.NewTable("v", prices, aliases)
	delete(prices, "a/b")
	delete(aliases, "b")
	assert.True(t, tbl.Known("b"))
}

func TestEstimate_Alias(t *testing.T) {
	direct, aliased := record("vendor/model", 10, 20, 30, 40, 0), record("model-alias", 10, 20, 30, 40, 0)
	table.Estimate(direct)
	table.Estimate(aliased)
	require.NotNil(t, aliased.EstimatedCostMicros)
	assert.Equal(t, direct.EstimatedCostMicros, aliased.EstimatedCostMicros)
	entry, _, ok := table.Lookup("model-alias")
	assert.True(t, ok)
	assert.Equal(t, "vendor/model", entry)
}

func TestEstimate_UnknownModelIsUnpricedNotGuessed(t *testing.T) {
	for _, model := range []string{"vendor/unknown", "dangling", "vendor/model-2", "VENDOR/MODEL"} {
		t.Run(model, func(t *testing.T) {
			r := record(model, 1000, 1000, 0, 0, 0)
			r.EstimatedCostMicros = ptr(42)
			table.Estimate(r)
			assert.Nil(t, r.EstimatedCostMicros, "a stale or client-sent estimate is cleared")
			assert.True(t, r.Unpriced)
			assert.Equal(t, "v-test", r.PriceVersion)
		})
	}
}

func TestEstimate_ReEstimateClearsUnpriced(t *testing.T) {
	r := record("vendor/model", 1, 0, 0, 0, 0)
	r.Unpriced = true
	table.Estimate(r)
	assert.False(t, r.Unpriced)
}

// wantAliases is written out independently of aliases.go, so a mistyped
// target there fails here rather than being its own oracle.
var wantAliases = map[string]string{
	"claude-opus-5-5":           "anthropic/claude-opus-5.5",
	"claude-opus-5":             "anthropic/claude-opus-5",
	"claude-opus-4-8":           "anthropic/claude-opus-4.8",
	"claude-opus-4-7":           "anthropic/claude-opus-4.7",
	"claude-opus-4-6":           "anthropic/claude-opus-4.6",
	"claude-opus-4-5":           "anthropic/claude-opus-4.5",
	"claude-sonnet-5-5":         "anthropic/claude-sonnet-5.5",
	"claude-sonnet-5":           "anthropic/claude-sonnet-5",
	"claude-sonnet-4-6":         "anthropic/claude-sonnet-4.6",
	"claude-sonnet-4-5":         "anthropic/claude-sonnet-4.5",
	"claude-fable-5-1":          "anthropic/claude-fable-5.1",
	"claude-fable-5":            "anthropic/claude-fable-5",
	"claude-haiku-4-5":          "anthropic/claude-haiku-4.5",
	"claude-haiku-4-5-20251001": "anthropic/claude-haiku-4.5",
	"gpt-6-luna":                "openai/gpt-6-luna",
	"gpt-6-luna-pro":            "openai/gpt-6-luna-pro",
	"gpt-5.6-luna":              "openai/gpt-5.6-luna",
	"gpt-5.6-luna-pro":          "openai/gpt-5.6-luna-pro",
	"gpt-5.6-terra":             "openai/gpt-5.6-terra",
	"gpt-5.6-terra-pro":         "openai/gpt-5.6-terra-pro",
	"gpt-5.6-sol":               "openai/gpt-5.6-sol",
	"gpt-5.6-sol-pro":           "openai/gpt-5.6-sol-pro",
	"gpt-5.5":                   "openai/gpt-5.5",
	"gpt-5.5-pro":               "openai/gpt-5.5-pro",
}

func TestDefault_Aliases(t *testing.T) {
	assert.Equal(t, wantAliases, pricing.Aliases())
	mine := pricing.Aliases()
	delete(mine, "claude-opus-5-5")
	assert.Contains(t, pricing.Aliases(), "claude-opus-5-5", "Aliases hands out a copy")
	d := pricing.Default()
	for alias, target := range wantAliases {
		entry, _, ok := d.Lookup(alias)
		if assert.True(t, ok, "alias %s -> %s is not in the generated table", alias, target) {
			assert.Equal(t, target, entry)
		}
	}
	entry, _, ok := d.Lookup("anthropic/claude-opus-5.5")
	assert.True(t, ok)
	assert.Equal(t, "anthropic/claude-opus-5.5", entry, "OpenRouter ids need no alias")
}

func TestDefault_KnownPrice(t *testing.T) {
	// anthropic/claude-opus-5.5 as recorded on 2026-10-01: $4 in, $20 out,
	// $0.20 cache read, $5 cache write per million tokens. Update with the
	// table when Anthropic's prices change.
	_, p, ok := pricing.Default().Lookup("claude-opus-5-5")
	require.True(t, ok)
	assert.Equal(t, pricing.Price{Input: 4_000_000, Output: 20_000_000, CacheRead: 200_000, CacheWrite: 5_000_000}, p)
}

func TestDefault_Version(t *testing.T) {
	assert.Regexp(t, regexp.MustCompile(`^\d{4}-\d{2}-\d{2}\.[0-9a-f]{12}$`), pricing.Default().Version())
	r := record("claude-opus-5-5", 1, 1, 1, 1, 0)
	pricing.Default().Estimate(r)
	assert.NoError(t, r.Validate(), "the version passes Validate's price_version check")
}
