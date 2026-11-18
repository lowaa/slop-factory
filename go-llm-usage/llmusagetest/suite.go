package llmusagetest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	llmusage "github.com/lowaa/slop-factory/go-llm-usage"
)

// ConcurrentUpserts is how many upserts StoreSuite's concurrency test runs
// on one key at once. A store needs at least this many connections free.
const ConcurrentUpserts = 8

// StoreSuite is the test every llmusage.Store implementation must pass.
// An app runs it against its real store (a dockerized Postgres, as its
// other store tests do); newStore returns a store for one subtest, and may
// share a database between them: every subtest uses keys of its own.
func StoreSuite(t *testing.T, newStore func(t *testing.T) llmusage.Store) {
	t.Run("FirstUpsertReturnsNothing", func(t *testing.T) {
		s, key := newStore(t), newKey(t)
		previous, err := s.Upsert(context.Background(), key, []llmusage.Record{*Sample("anthropic", "claude-opus-5-5", 1)})
		require.NoError(t, err)
		assert.Empty(t, previous)
	})

	t.Run("RepeatReturnsWhatWasStored", func(t *testing.T) {
		s, key := newStore(t), newKey(t)
		report := []llmusage.Record{*Sample("anthropic", "claude-opus-5-5", 1)}
		upsert(t, s, key, report)
		assertSame(t, report, upsert(t, s, key, report))
	})

	t.Run("OverwriteReplaces", func(t *testing.T) {
		s, key := newStore(t), newKey(t)
		upsert(t, s, key, []llmusage.Record{*Sample("anthropic", "claude-opus-5-5", 10)})
		second := []llmusage.Record{*Sample("anthropic", "claude-opus-5-5", 3)}
		upsert(t, s, key, second)
		assertSame(t, second, upsert(t, s, key, second))
	})

	t.Run("SeveralModelsPerKey", func(t *testing.T) {
		s, key := newStore(t), newKey(t)
		all := []llmusage.Record{
			*Sample("anthropic", "claude-opus-5-5", 1),
			*Sample("anthropic", "claude-sonnet-5-5", 2),
			*Sample("openrouter", "anthropic/claude-opus-5.5", 3),
		}
		upsert(t, s, key, all)
		one := []llmusage.Record{*Sample("anthropic", "claude-sonnet-5-5", 4)}
		assertSame(t, all, upsert(t, s, key, one))
		assertSame(t, one, upsert(t, s, key, one), "a model missing from the new totals is gone")
	})

	t.Run("ToolsReplacedWithTheirRecord", func(t *testing.T) {
		s, key := newStore(t), newKey(t)
		first := Sample("anthropic", "claude-opus-5-5", 1)
		first.Tools = []llmusage.ToolUsage{
			{Category: "bash", Tool: "Bash", Command: "git status", Calls: 2, Errors: 1, DurationMs: 30, ResultBytes: 400},
			{Category: "file_read", Tool: "Read", Calls: 5, DurationMs: 7, ResultBytes: 9000},
		}
		upsert(t, s, key, []llmusage.Record{*first})
		second := Sample("anthropic", "claude-opus-5-5", 2)
		second.Tools = []llmusage.ToolUsage{
			{Category: "file_read", Tool: "Read", Calls: 6, DurationMs: 8, ResultBytes: 9100},
			{Category: "mcp:slop-haus", Tool: "mcp__slop-haus__ticket_get", Calls: 1, DurationMs: 50, ResultBytes: 2000},
		}
		assertSame(t, []llmusage.Record{*first}, upsert(t, s, key, []llmusage.Record{*second}))
		assertSame(t, []llmusage.Record{*second}, upsert(t, s, key, []llmusage.Record{*second}))
	})

	t.Run("OptionalFieldsRoundTrip", func(t *testing.T) {
		s, key := newStore(t), newKey(t)
		priced := Sample("openrouter", "anthropic/claude-opus-5.5", 1)
		priced.ReportedCostMicros = ptr(123)
		priced.EstimatedCostMicros = ptr(0)
		priced.PriceVersion = "2026-10-01.0123456789ab"
		unpriced := Sample("anthropic", "mystery-model", 1)
		unpriced.Unpriced = true
		unpriced.PriceVersion = "2026-10-01.0123456789ab"
		report := []llmusage.Record{*priced, *unpriced}
		upsert(t, s, key, report)
		assertSame(t, report, upsert(t, s, key, report))
	})

	t.Run("ConcurrentUpsertsSeeAConsistentPrevious", func(t *testing.T) {
		s := newStore(t)
		// A start barrier makes overlap likely, not certain, so several
		// rounds; against a real database the round trips overlap anyway.
		for round := 0; round < concurrentRounds; round++ {
			concurrentRound(t, s, newKey(t))
		}
	})

	t.Run("KeysAreIsolated", func(t *testing.T) {
		s, a, b := newStore(t), newKey(t), newKey(t)
		ra := []llmusage.Record{*Sample("anthropic", "claude-opus-5-5", 1)}
		rb := []llmusage.Record{*Sample("anthropic", "claude-opus-5-5", 2)}
		upsert(t, s, a, ra)
		assert.Empty(t, upsert(t, s, b, rb), "a fresh key sees nothing of another")
		assertSame(t, ra, upsert(t, s, a, ra))
		assertSame(t, rb, upsert(t, s, b, rb))
	})
}

const concurrentRounds = 5

// concurrentRound checks that ConcurrentUpserts upserts racing on one key
// form one chain: exactly one saw nothing, every other saw one earlier
// report as it was submitted, no report was seen twice, and the one nobody
// saw is what the store holds now.
func concurrentRound(t *testing.T, s llmusage.Store, key llmusage.TranscriptKey) {
	t.Helper()
	type result struct {
		requests int64
		previous []llmusage.Record
		err      error
	}
	report := func(n int64) []llmusage.Record { return []llmusage.Record{*Sample("anthropic", "claude-opus-5-5", n)} }
	start := make(chan struct{})
	results := make(chan result, ConcurrentUpserts)
	var wg sync.WaitGroup
	for n := int64(1); n <= ConcurrentUpserts; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			previous, err := s.Upsert(context.Background(), key, report(n))
			results <- result{requests: n, previous: previous, err: err}
		}()
	}
	close(start)
	wg.Wait()
	close(results)

	seenBy := map[int64]int64{}
	firsts := 0
	for r := range results {
		require.NoError(t, r.err)
		if len(r.previous) == 0 {
			firsts++
			continue
		}
		require.Len(t, r.previous, 1)
		prev := r.previous[0].Requests
		require.True(t, prev >= 1 && prev <= ConcurrentUpserts, "previous %d is not a submitted report", prev)
		assertSame(t, report(prev), r.previous, "previous is the report as submitted")
		require.NotContains(t, seenBy, prev, "upserts %d and %d both saw %d as previous", seenBy[prev], r.requests, prev)
		seenBy[prev] = r.requests
	}
	require.Equal(t, 1, firsts, "exactly one upsert sees an empty key")
	final := upsert(t, s, key, report(ConcurrentUpserts+1))
	require.Len(t, final, 1)
	last := final[0].Requests
	require.True(t, last >= 1 && last <= ConcurrentUpserts, "stored %d is not a submitted report", last)
	require.NotContains(t, seenBy, last, "the stored totals were already overwritten")
	assert.Len(t, seenBy, ConcurrentUpserts-1)
	assertSame(t, report(ConcurrentUpserts+1), upsert(t, s, key, report(ConcurrentUpserts+1)), "the final report was stored")
}

// Sample is a valid record for model whose counts all scale with n, so
// records from different reports tell themselves apart.
func Sample(provider, model string, n int64) *llmusage.Record {
	start := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	return &llmusage.Record{
		SchemaVersion:    llmusage.SchemaVersion,
		Provider:         provider,
		Model:            model,
		Harness:          llmusage.HarnessClaude,
		BillingMode:      llmusage.BillingSubscription,
		Requests:         n,
		InputTokens:      100 * n,
		OutputTokens:     50 * n,
		CacheReadTokens:  1000 * n,
		CacheWriteTokens: 200 * n,
		ReasoningTokens:  10 * n,
		StartedAt:        start,
		EndedAt:          start.Add(time.Duration(n) * time.Minute),
	}
}

func ptr(v int64) *int64 { return &v }

func newKey(t *testing.T) llmusage.TranscriptKey {
	b := make([]byte, 8)
	_, err := rand.Read(b)
	require.NoError(t, err)
	return llmusage.TranscriptKey("suite-" + hex.EncodeToString(b))
}

func upsert(t *testing.T, s llmusage.Store, key llmusage.TranscriptKey, records []llmusage.Record) []llmusage.Record {
	t.Helper()
	previous, err := s.Upsert(context.Background(), key, records)
	require.NoError(t, err)
	return previous
}

// assertSame compares records as a store may return them: in any order,
// tools in any order, an empty tool list as nil, times in any zone.
func assertSame(t *testing.T, want, got []llmusage.Record, msgAndArgs ...any) {
	t.Helper()
	assert.Equal(t, normalize(want), normalize(got), msgAndArgs...)
}

func normalize(records []llmusage.Record) []llmusage.Record {
	out := cloneAll(records)
	for i := range out {
		r := &out[i]
		r.StartedAt, r.EndedAt = r.StartedAt.UTC(), r.EndedAt.UTC()
		if len(r.Tools) == 0 {
			r.Tools = nil
		}
		sort.Slice(r.Tools, func(a, b int) bool {
			if r.Tools[a].Tool != r.Tools[b].Tool {
				return r.Tools[a].Tool < r.Tools[b].Tool
			}
			return r.Tools[a].Command < r.Tools[b].Command
		})
	}
	sort.Slice(out, func(a, b int) bool {
		ka, kb := out[a].Key(), out[b].Key()
		if ka.Provider != kb.Provider {
			return ka.Provider < kb.Provider
		}
		if ka.Model != kb.Model {
			return ka.Model < kb.Model
		}
		if ka.Harness != kb.Harness {
			return ka.Harness < kb.Harness
		}
		return ka.BillingMode < kb.BillingMode
	})
	if len(out) == 0 {
		return nil
	}
	return out
}
