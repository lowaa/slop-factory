package pricegen_test

import (
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-llm-usage/internal/pricegen"
)

// testdata/models.json is a recorded GET /api/v1/models response, trimmed
// to a few models that cover each rule.
func parseRecorded(t *testing.T) []pricegen.Entry {
	f, err := os.Open("testdata/models.json")
	require.NoError(t, err)
	t.Cleanup(func() { _ = f.Close() })
	entries, err := pricegen.Parse(f)
	require.NoError(t, err)
	return entries
}

func TestParse_RecordedResponse(t *testing.T) {
	assert.Equal(t, []pricegen.Entry{
		// Cache prices given; the 1h cache-write price is not used.
		{Model: "anthropic/claude-opus-5.5", Input: 4_000_000, Output: 20_000_000, CacheRead: 200_000, CacheWrite: 5_000_000},
		// Free.
		{Model: "inclusionai/ling-3.0-flash-sante:free", Input: 0, Output: 0, CacheRead: 0, CacheWrite: 0},
		// Tiered overrides ignored: the base prices only.
		{Model: "openai/gpt-5.6-luna", Input: 200_000, Output: 1_200_000, CacheRead: 20_000, CacheWrite: 250_000},
		// No cache prices: cached tokens cost what input does.
		{Model: "perceptron/perceptron-mk1.5", Input: 150_000, Output: 1_500_000, CacheRead: 150_000, CacheWrite: 150_000},
		// Sub-micro-dollar prices stay exact.
		{Model: "tencent/hy-mt2-1.8b", Input: 44_000, Output: 177_000, CacheRead: 44_000, CacheWrite: 44_000},
		// Left out: "~openai/gpt-luna-latest" (a floating alias) and
		// "typesafe/jev-router" (a dynamic router priced -1).
	}, parseRecorded(t))
}

func TestParse_Rejects(t *testing.T) {
	for name, body := range map[string]string{
		"not json":         `nope`,
		"no priced models": `{"data":[{"id":"x/router","pricing":{"prompt":"-1","completion":"-1"}}]}`,
		"empty":            `{"data":[]}`,
		"trailing data":    `{"data":[{"id":"x/y","pricing":{"prompt":"0","completion":"0"}}]} {"data":[]}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := pricegen.Parse(strings.NewReader(body))
			assert.Error(t, err)
		})
	}
}

func TestParse_SkipsUnparseablePrices(t *testing.T) {
	entries, err := pricegen.Parse(strings.NewReader(`{"data":[
		{"id":"x/good","pricing":{"prompt":"0.000001","completion":"0.000002","input_cache_read":"junk"}},
		{"id":"x/bad-prompt","pricing":{"prompt":"not-a-price","completion":"0.000002"}},
		{"id":"x/no-completion","pricing":{"prompt":"0.000001"}},
		{"id":"x/negative","pricing":{"prompt":"0.000001","completion":"-0.000002"}}
	]}`))
	require.NoError(t, err)
	assert.Equal(t, []pricegen.Entry{
		{Model: "x/good", Input: 1_000_000, Output: 2_000_000, CacheRead: 1_000_000, CacheWrite: 1_000_000},
	}, entries, "an unparseable cache price falls back to input; an unparseable prompt or completion skips the model")
}

func TestParse_RoundsBelowAPicodollar(t *testing.T) {
	entries, err := pricegen.Parse(strings.NewReader(
		`{"data":[{"id":"x/y","pricing":{"prompt":"0.0000000000005","completion":"0.0000000000004999"}}]}`))
	require.NoError(t, err)
	assert.Equal(t, int64(1), entries[0].Input)
	assert.Equal(t, int64(0), entries[0].Output)
}

func TestRender_ValidGoWithVersion(t *testing.T) {
	entries := parseRecorded(t)
	day := time.Date(2026, 10, 1, 23, 0, 0, 0, time.UTC)
	src, err := pricegen.Render(entries, day)
	require.NoError(t, err)

	_, err = parser.ParseFile(token.NewFileSet(), "prices_gen.go", src, 0)
	require.NoError(t, err)
	s := string(src)
	assert.Contains(t, s, "DO NOT EDIT")
	assert.Contains(t, s, "package pricing")
	assert.Contains(t, s, `const generatedVersion = "`+pricegen.Version(entries, day)+`"`)
	for _, e := range entries {
		assert.Regexp(t, fmt.Sprintf(`%q: +\{Input: %d, Output: %d, CacheRead: %d, CacheWrite: %d\},`,
			e.Model, e.Input, e.Output, e.CacheRead, e.CacheWrite), s)
	}
	assert.Equal(t, len(entries), strings.Count(s, "{Input: "), "every entry and nothing else")
	assert.NotContains(t, s, "jev-router")
}

func TestVersion(t *testing.T) {
	entries := parseRecorded(t)
	day := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	v := pricegen.Version(entries, day)
	assert.Regexp(t, `^2026-10-01\.[0-9a-f]{12}$`, v)
	assert.Equal(t, v, pricegen.Version(entries, day.Add(time.Hour)), "same day, same prices, same version")

	for name, change := range map[string]func(e *pricegen.Entry){
		"model":       func(e *pricegen.Entry) { e.Model += "x" },
		"input":       func(e *pricegen.Entry) { e.Input++ },
		"output":      func(e *pricegen.Entry) { e.Output++ },
		"cache read":  func(e *pricegen.Entry) { e.CacheRead++ },
		"cache write": func(e *pricegen.Entry) { e.CacheWrite++ },
	} {
		changed := append([]pricegen.Entry(nil), entries...)
		change(&changed[0])
		assert.NotEqual(t, v, pricegen.Version(changed, day), "a change to %s changes the hash", name)
	}
}
