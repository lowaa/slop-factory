package variantcompare

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSummarizeInterpolatesBetweenRanks(t *testing.T) {
	got := summarize([]float64{4, 1, 3, 2})

	assert.Equal(t, Summary{N: 4, Median: 2.5, Q1: 1.75, Q3: 3.25, Min: 1, Max: 4}, *got)
	assert.Nil(t, summarize(nil))
}
