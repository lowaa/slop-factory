package llmusage_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/llmusage"
)

func TestTokenCeiling(t *testing.T) {
	assert.Equal(t, int64(2_000_000), llmusage.TokenCeiling("claude-opus-5-5"))
	assert.Equal(t, int64(2_000_000), llmusage.TokenCeiling("anthropic/claude-opus-5.5"))
	assert.Equal(t, int64(4_000_000), llmusage.TokenCeiling("Gemini-3-pro"))
	assert.Equal(t, int64(2_000_000), llmusage.TokenCeiling("some-unknown-model"), "unknown models get the default window")
}

func TestImplausibleReportIsRefusedAndNotStored(t *testing.T) {
	for name, mutate := range map[string]func(n int64) (input, cacheRead int64){
		"cache reads": func(n int64) (int64, int64) { return 100, 2_000_001 * n },
		"input":       func(n int64) (int64, int64) { return 2_000_001 * n, 100 },
	} {
		t.Run(name, func(t *testing.T) {
			w := newWire(t, 100)
			r := piRecord(3)
			r.InputTokens, r.CacheReadTokens = mutate(r.Requests)
			rec := w.post(t, builderToken, piReport("pi-session", r).json(t))
			require.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
			assert.Contains(t, rec.Body.String(), "ceiling")
			usage, tools := w.rowCount(t, w.ctx)
			assert.Zero(t, usage)
			assert.Zero(t, tools)
			assert.Zero(t, w.counter(t, "llm_requests_total"))
		})
	}
}

// An interactive worker re-reads its whole ~150k context on every request.
func TestPlausibleReportIsStored(t *testing.T) {
	w := newWire(t, 100)
	r := piRecord(40)
	r.InputTokens, r.CacheReadTokens = 40*2_000, 40*150_000
	rec := w.post(t, builderToken, piReport("pi-session", r).json(t))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	usage, _ := w.rowCount(t, w.ctx)
	assert.Equal(t, 1, usage)
}

func TestCeilingIsPerModelAndInclusive(t *testing.T) {
	w := newWire(t, 100)

	atCeiling := piRecord(2)
	atCeiling.CacheReadTokens = 2 * 2_000_000
	require.Equal(t, http.StatusOK, w.post(t, builderToken, piReport("at-ceiling", atCeiling).json(t)).Code)

	over := piRecord(2)
	over.CacheReadTokens = 2*2_000_000 + 2
	require.Equal(t, http.StatusUnprocessableEntity, w.post(t, builderToken, piReport("over", over).json(t)).Code)

	// 3M per request is over the default ceiling but within a Gemini's.
	gemini := piRecord(2)
	gemini.Model = "gemini-3-pro"
	gemini.CacheReadTokens = 2 * 3_000_000
	require.Equal(t, http.StatusOK, w.post(t, builderToken, piReport("gemini", gemini).json(t)).Code)
}

func TestOneImplausibleRecordRefusesTheWholeReport(t *testing.T) {
	w := newWire(t, 100)
	good, bad := piRecord(2), piRecord(2)
	bad.Model = "claude-sonnet-5-5"
	bad.CacheReadTokens = 2 * 2_000_001
	rec := w.post(t, builderToken, piReport("mixed", good, bad).json(t))
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code, rec.Body.String())
	usage, _ := w.rowCount(t, w.ctx)
	assert.Zero(t, usage)
}
