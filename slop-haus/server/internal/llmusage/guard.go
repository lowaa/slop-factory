package llmusage

import (
	"fmt"
	"strings"

	usagelib "github.com/lowaa/slop-factory/go-llm-usage"
)

// ErrImplausible is a report whose tokens per request no model could have
// used. It is an ErrInvalid: refused and not stored, so an over-count in a
// harness or in the delta shows up at once rather than in a chart days
// later.
var ErrImplausible = fmt.Errorf("%w: implausible token counts", ErrInvalid)

// ceilingFactor is how far above a model's context window one request's
// input or cache-read tokens may be. A request reads at most one window, so
// the factor is slack for how a harness counts (e.g. a request retried
// within one counted turn), not a real allowance.
const ceilingFactor = 2

// defaultContextWindow applies to a model not in knownContextWindows. It is
// the largest window in the table other than the 2M ones, so an unknown
// model is not held to a smaller window than a mainstream one has.
const defaultContextWindow = 1_000_000

// knownContextWindows are the largest context windows, in tokens, of the
// model families agents report, matched by lowercase model-name prefix with
// the longest prefix winning. The price table has no context windows, so
// this is slop-haus's own; a family missing here gets the default, which
// errs towards accepting.
var knownContextWindows = map[string]int64{
	"claude":   1_000_000,
	"gpt":      1_000_000,
	"gemini":   2_000_000,
	"grok":     2_000_000,
	"deepseek": 1_000_000,
	"qwen":     1_000_000,
	"llama":    1_000_000,
}

func contextWindow(model string) int64 {
	model = strings.ToLower(model)
	// Provider-prefixed names ("anthropic/claude-...") match on the part
	// after the last slash.
	if i := strings.LastIndex(model, "/"); i >= 0 {
		model = model[i+1:]
	}
	best, window := 0, int64(defaultContextWindow)
	for prefix, w := range knownContextWindows {
		if len(prefix) > best && strings.HasPrefix(model, prefix) {
			best, window = len(prefix), w
		}
	}
	return window
}

// TokenCeiling is the most input or cache-read tokens one request of model
// may report.
func TokenCeiling(model string) int64 { return ceilingFactor * contextWindow(model) }

// increment is what r adds to the totals stored for its key. Totals that did
// not grow in every counter are a transcript started again (the store keeps
// them whole, and go-llm-usage counts nothing for them), so they are judged as
// they stand.
func increment(prev, r *usagelib.Record) usagelib.Record {
	if prev == nil || r.Requests < prev.Requests || r.InputTokens < prev.InputTokens || r.CacheReadTokens < prev.CacheReadTokens {
		return *r
	}
	inc := *r
	inc.Requests -= prev.Requests
	inc.InputTokens -= prev.InputTokens
	inc.CacheReadTokens -= prev.CacheReadTokens
	return inc
}

// checkPlausible refuses a report that adds more input or cache-read tokens
// per request than its model's ceiling, judged on what the report adds to the
// stored totals and not on the totals: a transcript that carries an inflated
// past (a Remote Control session kept across the over-count fix) would
// otherwise never be accepted again, however honest its new turns. Requests
// below one count as one, so an increment that reports tokens without
// requests is judged on its tokens.
func checkPlausible(previous, records []usagelib.Record) error {
	stored := make(map[usagelib.RecordKey]*usagelib.Record, len(previous))
	for i := range previous {
		stored[previous[i].Key()] = &previous[i]
	}
	for i := range records {
		inc := increment(stored[records[i].Key()], &records[i])
		requests := max(inc.Requests, 1)
		ceiling := TokenCeiling(inc.Model)
		for _, c := range []struct {
			name   string
			tokens int64
		}{{"input", inc.InputTokens}, {"cache-read", inc.CacheReadTokens}} {
			if perRequest := c.tokens / requests; perRequest > ceiling {
				return fmt.Errorf("%w: model %q adds %d %s tokens over %d requests (%d per request), above the ceiling of %d",
					ErrImplausible, inc.Model, c.tokens, c.name, inc.Requests, perRequest, ceiling)
			}
		}
	}
	return nil
}
