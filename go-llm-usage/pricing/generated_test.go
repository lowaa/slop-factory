package pricing

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-llm-usage/internal/pricegen"
)

// The committed table must be what its version says: a hand edit to a
// price, or a regeneration whose version wasn't updated, fails here.
func TestGenerated_VersionMatchesPrices(t *testing.T) {
	date, _, ok := strings.Cut(generatedVersion, ".")
	require.True(t, ok)
	day, err := time.Parse("2006-01-02", date)
	require.NoError(t, err)

	entries := make([]pricegen.Entry, 0, len(generatedPrices))
	for model, p := range generatedPrices {
		require.GreaterOrEqual(t, min(p.Input, p.Output, p.CacheRead, p.CacheWrite), int64(0), model)
		entries = append(entries, pricegen.Entry{Model: model, Input: p.Input, Output: p.Output, CacheRead: p.CacheRead, CacheWrite: p.CacheWrite})
	}
	pricegen.SortEntries(entries)
	assert.Equal(t, pricegen.Version(entries, day), generatedVersion)
}
