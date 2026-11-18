package variantcompare

import (
	"math"
	"sort"
)

// Summary is the spread of one measure's per-ticket differences. Q1 and Q3
// bound the interquartile range.
type Summary struct {
	N      int     `json:"n" doc:"the tickets the difference is known for"`
	Median float64 `json:"median"`
	Q1     float64 `json:"q1"`
	Q3     float64 `json:"q3"`
	Min    float64 `json:"min"`
	Max    float64 `json:"max"`
}

// summarize is nil for no values: a measure nobody could read has no spread,
// which is not a spread of zero.
func summarize(values []float64) *Summary {
	if len(values) == 0 {
		return nil
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	return &Summary{
		N: len(sorted), Median: quantile(sorted, 0.5), Q1: quantile(sorted, 0.25), Q3: quantile(sorted, 0.75),
		Min: sorted[0], Max: sorted[len(sorted)-1],
	}
}

// median is the middle of the values; ok is false for none.
func median(values []float64) (m float64, ok bool) {
	if len(values) == 0 {
		return 0, false
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	return quantile(sorted, 0.5), true
}

// quantile interpolates linearly between the closest ranks of sorted values.
func quantile(sorted []float64, q float64) float64 {
	pos := q * float64(len(sorted)-1)
	lo := int(math.Floor(pos))
	hi := int(math.Ceil(pos))
	return sorted[lo] + (sorted[hi]-sorted[lo])*(pos-float64(lo))
}
