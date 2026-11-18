package llmusage_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	llmusage "github.com/lowaa/slop-factory/go-llm-usage"
	"github.com/lowaa/slop-factory/go-llm-usage/llmusagetest"
)

func ptr(v int64) *int64 { return &v }

func withTools(r *llmusage.Record, tools ...llmusage.ToolUsage) *llmusage.Record {
	r.Tools = tools
	return r
}

func TestDelta_FirstReportIsTheWholeRecord(t *testing.T) {
	r := withTools(llmusagetest.Sample("anthropic", "claude-opus-5-5", 3),
		llmusage.ToolUsage{Tool: "Bash", Command: "ls", Calls: 2, DurationMs: 10, ResultBytes: 50})
	d, err := llmusage.Delta(nil, r)
	require.NoError(t, err)
	assert.Equal(t, r, d)
	d.Tools[0].Calls = 99
	assert.Equal(t, int64(2), r.Tools[0].Calls, "the delta doesn't share the record's tools")
}

func TestDelta_SubtractsEveryCount(t *testing.T) {
	old := llmusagetest.Sample("anthropic", "claude-opus-5-5", 2)
	old.ReportedCostMicros, old.EstimatedCostMicros = ptr(100), ptr(1000)
	old.Tools = []llmusage.ToolUsage{
		{Tool: "Bash", Command: "git status", Calls: 2, Errors: 1, DurationMs: 20, ResultBytes: 200},
		{Tool: "Read", Calls: 4, DurationMs: 8, ResultBytes: 4000},
	}
	cur := llmusagetest.Sample("anthropic", "claude-opus-5-5", 5)
	cur.ReportedCostMicros, cur.EstimatedCostMicros = ptr(250), ptr(2500)
	cur.Tools = []llmusage.ToolUsage{
		{Tool: "Bash", Command: "git status", Calls: 3, Errors: 1, DurationMs: 35, ResultBytes: 260},
		{Tool: "Bash", Command: "go test", Calls: 1, DurationMs: 900, ResultBytes: 30},
		{Tool: "Read", Calls: 4, DurationMs: 8, ResultBytes: 4000},
	}

	d, err := llmusage.Delta(old, cur)
	require.NoError(t, err)

	assert.Equal(t, int64(3), d.Requests)
	assert.Equal(t, int64(300), d.InputTokens)
	assert.Equal(t, int64(150), d.OutputTokens)
	assert.Equal(t, int64(3000), d.CacheReadTokens)
	assert.Equal(t, int64(600), d.CacheWriteTokens)
	assert.Equal(t, int64(30), d.ReasoningTokens)
	assert.Equal(t, ptr(150), d.ReportedCostMicros)
	assert.Equal(t, ptr(1500), d.EstimatedCostMicros)
	assert.Equal(t, []llmusage.ToolUsage{
		{Tool: "Bash", Command: "git status", Calls: 1, DurationMs: 15, ResultBytes: 60},
		{Tool: "Bash", Command: "go test", Calls: 1, DurationMs: 900, ResultBytes: 30},
	}, d.Tools, "unchanged tools are left out, new ones come in whole")
	assert.False(t, d.Shrank())
	assert.False(t, d.IsZero())
	assert.Equal(t, cur.EndedAt, d.EndedAt)
	assert.Equal(t, int64(2), old.Requests, "the inputs are untouched")
	assert.Equal(t, int64(5), cur.Requests)
}

func TestDelta_RepeatIsZero(t *testing.T) {
	r := withTools(llmusagetest.Sample("anthropic", "claude-opus-5-5", 3),
		llmusage.ToolUsage{Tool: "Bash", Command: "ls", Calls: 2})
	r.ReportedCostMicros = ptr(7)
	d, err := llmusage.Delta(r, r.Clone())
	require.NoError(t, err)
	assert.True(t, d.IsZero())
	assert.False(t, d.Shrank())
}

func TestDelta_Costs(t *testing.T) {
	for name, tc := range map[string]struct {
		old, new, want *int64
	}{
		"neither has one":   {nil, nil, nil},
		"first appears":     {nil, ptr(40), ptr(40)},
		"grows":             {ptr(10), ptr(40), ptr(30)},
		"disappears shrank": {ptr(10), nil, ptr(-10)},
	} {
		t.Run(name, func(t *testing.T) {
			old := llmusagetest.Sample("openrouter", "anthropic/claude-opus-5.5", 1)
			cur := llmusagetest.Sample("openrouter", "anthropic/claude-opus-5.5", 1)
			old.ReportedCostMicros, cur.ReportedCostMicros = tc.old, tc.new
			d, err := llmusage.Delta(old, cur)
			require.NoError(t, err)
			assert.Equal(t, tc.want, d.ReportedCostMicros)
		})
	}
}

func TestDelta_Shrink(t *testing.T) {
	big := withTools(llmusagetest.Sample("anthropic", "claude-opus-5-5", 5),
		llmusage.ToolUsage{Tool: "Bash", Command: "ls", Calls: 2},
		llmusage.ToolUsage{Tool: "Read", Calls: 1, ResultBytes: 10})

	t.Run("counts", func(t *testing.T) {
		d, err := llmusage.Delta(big, llmusagetest.Sample("anthropic", "claude-opus-5-5", 4))
		require.NoError(t, err)
		assert.True(t, d.Shrank())
		assert.Equal(t, int64(-1), d.Requests)
	})
	t.Run("a tool vanished", func(t *testing.T) {
		cur := withTools(llmusagetest.Sample("anthropic", "claude-opus-5-5", 6),
			llmusage.ToolUsage{Tool: "Bash", Command: "ls", Calls: 3})
		d, err := llmusage.Delta(big, cur)
		require.NoError(t, err)
		assert.True(t, d.Shrank())
		assert.Contains(t, d.Tools, llmusage.ToolUsage{Tool: "Read", Calls: -1, ResultBytes: -10})
	})
	t.Run("one tool field", func(t *testing.T) {
		cur := big.Clone()
		cur.Tools[1].ResultBytes = 9
		d, err := llmusage.Delta(big, cur)
		require.NoError(t, err)
		assert.True(t, d.Shrank())
	})
}

func TestDelta_KeyMismatch(t *testing.T) {
	_, err := llmusage.Delta(
		llmusagetest.Sample("anthropic", "claude-opus-5-5", 1),
		llmusagetest.Sample("anthropic", "claude-sonnet-5-5", 1))
	assert.ErrorIs(t, err, llmusage.ErrKeyMismatch)
}
