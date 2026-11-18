package llmusage_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	llmusage "github.com/lowaa/slop-factory/go-llm-usage"
	"github.com/lowaa/slop-factory/go-llm-usage/llmusagetest"
)

func i64(v int64) *int64 { return &v }

func validRecord() *llmusage.Record {
	return llmusagetest.Sample("anthropic", "claude-opus-4", 3)
}

func TestValidate_ValidRecord(t *testing.T) {
	require.NoError(t, validRecord().Validate())
}

func TestValidate_ValidRecordWithTools(t *testing.T) {
	r := validRecord()
	r.Tools = []llmusage.ToolUsage{
		{Category: llmusage.CategoryBash, Tool: "Bash", Command: "git status", Calls: 3, Errors: 1, DurationMs: 40, ResultBytes: 900},
		{Tool: "Read", Calls: 2},
		{Tool: "mcp__slop-haus__ticket_get", Calls: 1},
	}
	require.NoError(t, r.Validate())
}

func TestValidate_Mutations(t *testing.T) {
	tooMany := make([]llmusage.ToolUsage, llmusage.MaxToolsPerRecord+1)
	for i := range tooMany {
		tooMany[i] = llmusage.ToolUsage{Tool: "t" + strings.Repeat("x", i%50) + string(rune('a'+i%26)) + string(rune('a'+(i/26)%26)) + string(rune('a'+(i/676)%26))}
	}
	withTool := func(u llmusage.ToolUsage) func(*llmusage.Record) {
		return func(r *llmusage.Record) { r.Tools = []llmusage.ToolUsage{u} }
	}
	const over = int64(1e12) + 1

	cases := []struct {
		name   string
		mutate func(*llmusage.Record)
		valid  bool
	}{
		{name: "schema version 0", mutate: func(r *llmusage.Record) { r.SchemaVersion = 0 }},
		{name: "schema version 2", mutate: func(r *llmusage.Record) { r.SchemaVersion = 2 }},
		{name: "provider empty", mutate: func(r *llmusage.Record) { r.Provider = "" }},
		{name: "provider uppercase", mutate: func(r *llmusage.Record) { r.Provider = "Anthropic" }},
		{name: "provider 33 chars", mutate: func(r *llmusage.Record) { r.Provider = strings.Repeat("a", 33) }},
		{name: "provider with space", mutate: func(r *llmusage.Record) { r.Provider = "open router" }},
		{name: "provider 32 chars", mutate: func(r *llmusage.Record) { r.Provider = strings.Repeat("a", 32) }, valid: true},
		{name: "model empty", mutate: func(r *llmusage.Record) { r.Model = "" }},
		{name: "model 129 chars", mutate: func(r *llmusage.Record) { r.Model = strings.Repeat("m", 129) }},
		{name: "model with space", mutate: func(r *llmusage.Record) { r.Model = "claude opus" }},
		{name: "model with newline", mutate: func(r *llmusage.Record) { r.Model = "claude\nopus" }},
		{name: "model 128 chars", mutate: func(r *llmusage.Record) { r.Model = strings.Repeat("m", 128) }, valid: true},
		{name: "harness codex", mutate: func(r *llmusage.Record) { r.Harness = "codex" }},
		{name: "harness app", mutate: func(r *llmusage.Record) { r.Harness = llmusage.HarnessApp }, valid: true},
		{name: "billing mode free", mutate: func(r *llmusage.Record) { r.BillingMode = "free" }},

		{name: "requests negative", mutate: func(r *llmusage.Record) { r.Requests = -1 }},
		{name: "requests too big", mutate: func(r *llmusage.Record) { r.Requests = over }},
		{name: "input negative", mutate: func(r *llmusage.Record) { r.InputTokens = -1 }},
		{name: "input too big", mutate: func(r *llmusage.Record) { r.InputTokens = over }},
		{name: "output negative", mutate: func(r *llmusage.Record) { r.OutputTokens = -1 }},
		{name: "output too big", mutate: func(r *llmusage.Record) { r.OutputTokens = over }},
		{name: "cache read negative", mutate: func(r *llmusage.Record) { r.CacheReadTokens = -1 }},
		{name: "cache read too big", mutate: func(r *llmusage.Record) { r.CacheReadTokens = over }},
		{name: "cache write negative", mutate: func(r *llmusage.Record) { r.CacheWriteTokens = -1 }},
		{name: "cache write too big", mutate: func(r *llmusage.Record) { r.CacheWriteTokens = over }},
		{name: "reasoning negative", mutate: func(r *llmusage.Record) { r.ReasoningTokens = -1 }},
		{name: "reasoning too big", mutate: func(r *llmusage.Record) { r.OutputTokens = over; r.ReasoningTokens = over }},
		{name: "reasoning above output", mutate: func(r *llmusage.Record) { r.OutputTokens = 5; r.ReasoningTokens = 6 }},
		{name: "counts exactly 1e12", mutate: func(r *llmusage.Record) {
			m := int64(1e12)
			r.Requests, r.InputTokens, r.OutputTokens, r.CacheReadTokens, r.CacheWriteTokens, r.ReasoningTokens = m, m, m, m, m, m
		}, valid: true},

		{name: "reported cost negative", mutate: func(r *llmusage.Record) { r.ReportedCostMicros = i64(-1) }},
		{name: "reported cost too big", mutate: func(r *llmusage.Record) { r.ReportedCostMicros = i64(llmusage.MaxCostMicros + 1) }},
		{name: "estimated cost negative", mutate: func(r *llmusage.Record) { r.EstimatedCostMicros = i64(-1) }},
		{name: "estimated cost too big", mutate: func(r *llmusage.Record) { r.EstimatedCostMicros = i64(llmusage.MaxCostMicros + 1) }},
		{name: "costs exactly MaxCostMicros", mutate: func(r *llmusage.Record) {
			r.ReportedCostMicros, r.EstimatedCostMicros = i64(llmusage.MaxCostMicros), i64(llmusage.MaxCostMicros)
		}, valid: true},
		{name: "price version with space", mutate: func(r *llmusage.Record) { r.PriceVersion = "v 1" }},
		{name: "price version 65 chars", mutate: func(r *llmusage.Record) { r.PriceVersion = strings.Repeat("v", 65) }},

		{name: "zero started_at", mutate: func(r *llmusage.Record) { r.StartedAt = time.Time{} }},
		{name: "started_at in 2019", mutate: func(r *llmusage.Record) {
			r.StartedAt = time.Date(2019, 12, 31, 0, 0, 0, 0, time.UTC)
			r.EndedAt = r.StartedAt.Add(time.Minute)
		}},
		{name: "ended before started", mutate: func(r *llmusage.Record) { r.EndedAt = r.StartedAt.Add(-time.Second) }},
		{name: "ended after 2200", mutate: func(r *llmusage.Record) { r.EndedAt = time.Date(2200, 1, 2, 0, 0, 0, 0, time.UTC) }},
		{name: "ended equals started", mutate: func(r *llmusage.Record) { r.EndedAt = r.StartedAt }, valid: true},

		{name: "too many tools", mutate: func(r *llmusage.Record) { r.Tools = tooMany }},
		{name: "tool empty name", mutate: withTool(llmusage.ToolUsage{Tool: ""})},
		{name: "tool name with space", mutate: withTool(llmusage.ToolUsage{Tool: "Bash tool"})},
		{name: "tool name 129 chars", mutate: withTool(llmusage.ToolUsage{Tool: strings.Repeat("t", 129)})},
		{name: "tool category nonsense", mutate: withTool(llmusage.ToolUsage{Tool: "Bash", Category: "nonsense"})},
		{name: "tool category empty mcp server", mutate: withTool(llmusage.ToolUsage{Tool: "Bash", Category: "mcp:"})},
		{name: "command capitalised", mutate: withTool(llmusage.ToolUsage{Tool: "Bash", Command: "Git Status"})},
		{name: "command three words", mutate: withTool(llmusage.ToolUsage{Tool: "Bash", Command: "git status --short"})},
		{name: "command 33 chars", mutate: withTool(llmusage.ToolUsage{Tool: "Bash", Command: strings.Repeat("a", 33)})},
		{name: "calls negative", mutate: withTool(llmusage.ToolUsage{Tool: "Bash", Calls: -1})},
		{name: "errors negative", mutate: withTool(llmusage.ToolUsage{Tool: "Bash", Calls: 1, Errors: -1})},
		{name: "duration negative", mutate: withTool(llmusage.ToolUsage{Tool: "Bash", DurationMs: -1})},
		{name: "result bytes negative", mutate: withTool(llmusage.ToolUsage{Tool: "Bash", ResultBytes: -1})},
		{name: "errors above calls", mutate: withTool(llmusage.ToolUsage{Tool: "Bash", Calls: 1, Errors: 2})},
		{name: "calls too big", mutate: withTool(llmusage.ToolUsage{Tool: "Bash", Calls: over})},
		{name: "duration too big", mutate: withTool(llmusage.ToolUsage{Tool: "Bash", DurationMs: 1e12 + 1})},
		{name: "result bytes too big", mutate: withTool(llmusage.ToolUsage{Tool: "Bash", ResultBytes: 1e15 + 1})},
		{name: "tool limits exactly", mutate: withTool(llmusage.ToolUsage{Tool: "Bash", Calls: 1e12, Errors: 1e12, DurationMs: 1e12, ResultBytes: 1e15}), valid: true},
		{name: "duplicate tool and command", mutate: func(r *llmusage.Record) {
			u := llmusage.ToolUsage{Tool: "Bash", Command: "git status", Calls: 1}
			r.Tools = []llmusage.ToolUsage{u, u}
		}},
		{name: "same tool different commands", mutate: func(r *llmusage.Record) {
			r.Tools = []llmusage.ToolUsage{
				{Tool: "Bash", Command: "git status", Calls: 1},
				{Tool: "Bash", Command: "git diff", Calls: 1},
			}
		}, valid: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := validRecord()
			tc.mutate(r)
			err := r.Validate()
			if tc.valid {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.ErrorIs(t, err, llmusage.ErrInvalid)
		})
	}
}

func TestValidateReport(t *testing.T) {
	a := llmusagetest.Sample("anthropic", "claude-opus-4", 1)
	b := llmusagetest.Sample("anthropic", "claude-sonnet-4", 2)

	t.Run("two distinct records", func(t *testing.T) {
		require.NoError(t, llmusage.ValidateReport([]llmusage.Record{*a, *b}))
	})
	t.Run("too many records", func(t *testing.T) {
		records := make([]llmusage.Record, llmusage.MaxRecordsPerReport+1)
		for i := range records {
			records[i] = *llmusagetest.Sample("anthropic", "model-"+strings.Repeat("x", i), 1)
		}
		err := llmusage.ValidateReport(records)
		require.Error(t, err)
		assert.ErrorIs(t, err, llmusage.ErrInvalid)
	})
	t.Run("exactly the maximum", func(t *testing.T) {
		records := make([]llmusage.Record, llmusage.MaxRecordsPerReport)
		for i := range records {
			records[i] = *llmusagetest.Sample("anthropic", "model-"+strings.Repeat("x", i), 1)
		}
		require.NoError(t, llmusage.ValidateReport(records))
	})
	t.Run("duplicate key", func(t *testing.T) {
		dup := *a
		dup.Requests = 9
		err := llmusage.ValidateReport([]llmusage.Record{*a, dup})
		require.Error(t, err)
		assert.ErrorIs(t, err, llmusage.ErrInvalid)
	})
	t.Run("differ only in harness", func(t *testing.T) {
		other := *a
		other.Harness = llmusage.HarnessPi
		require.NoError(t, llmusage.ValidateReport([]llmusage.Record{*a, other}))
	})
	t.Run("invalid record is wrapped", func(t *testing.T) {
		bad := *b
		bad.Requests = -1
		err := llmusage.ValidateReport([]llmusage.Record{*a, bad})
		require.Error(t, err)
		assert.ErrorIs(t, err, llmusage.ErrInvalid)
		assert.Contains(t, err.Error(), "record 1")
	})
}

// The wire format is a contract with clients in other languages and with the
// central service, so field names and omitempty behaviour are pinned here.
const goldenRecordJSON = `{
  "schema_version": 1,
  "provider": "openrouter",
  "model": "anthropic/claude-opus-4",
  "harness": "pi",
  "billing_mode": "metered",
  "requests": 7,
  "input_tokens": 1000,
  "output_tokens": 500,
  "cache_read_tokens": 4000,
  "cache_write_tokens": 300,
  "reasoning_tokens": 120,
  "reported_cost_microdollars": 123456,
  "estimated_cost_microdollars": 120000,
  "price_version": "2026-10-01",
  "unpriced": true,
  "started_at": "2026-10-01T09:00:00Z",
  "ended_at": "2026-10-01T09:30:00Z",
  "tools": [
    {"category": "bash", "tool": "Bash", "command": "git status", "calls": 5, "errors": 1, "duration_ms": 250, "result_bytes": 2048},
    {"category": "mcp:slop-haus", "tool": "ticket_get", "calls": 2, "errors": 0, "duration_ms": 30, "result_bytes": 512}
  ]
}`

func goldenRecord() llmusage.Record {
	start := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	return llmusage.Record{
		SchemaVersion:       1,
		Provider:            "openrouter",
		Model:               "anthropic/claude-opus-4",
		Harness:             llmusage.HarnessPi,
		BillingMode:         llmusage.BillingMetered,
		Requests:            7,
		InputTokens:         1000,
		OutputTokens:        500,
		CacheReadTokens:     4000,
		CacheWriteTokens:    300,
		ReasoningTokens:     120,
		ReportedCostMicros:  i64(123456),
		EstimatedCostMicros: i64(120000),
		PriceVersion:        "2026-10-01",
		Unpriced:            true,
		StartedAt:           start,
		EndedAt:             start.Add(30 * time.Minute),
		Tools: []llmusage.ToolUsage{
			{Category: "bash", Tool: "Bash", Command: "git status", Calls: 5, Errors: 1, DurationMs: 250, ResultBytes: 2048},
			{Category: "mcp:slop-haus", Tool: "ticket_get", Calls: 2, Errors: 0, DurationMs: 30, ResultBytes: 512},
		},
	}
}

func TestRecordJSON_Golden(t *testing.T) {
	want := goldenRecord()

	var got llmusage.Record
	require.NoError(t, json.Unmarshal([]byte(goldenRecordJSON), &got))
	require.Equal(t, want, got)

	out, err := json.Marshal(&want)
	require.NoError(t, err)
	assert.JSONEq(t, goldenRecordJSON, string(out))
}

func TestRecordJSON_OmitsEmptyOptionalFields(t *testing.T) {
	r := validRecord()
	out, err := json.Marshal(r)
	require.NoError(t, err)

	var m map[string]any
	require.NoError(t, json.Unmarshal(out, &m))
	for _, key := range []string{"reported_cost_microdollars", "estimated_cost_microdollars", "price_version", "unpriced", "tools"} {
		assert.NotContains(t, m, key)
	}
	for _, key := range []string{"schema_version", "provider", "model", "harness", "billing_mode", "requests", "started_at", "ended_at"} {
		assert.Contains(t, m, key)
	}
}

func TestRecordJSON_ToolOmitsEmptyCategoryAndCommand(t *testing.T) {
	out, err := json.Marshal(llmusage.ToolUsage{Tool: "Read", Calls: 1})
	require.NoError(t, err)
	assert.JSONEq(t, `{"tool":"Read","calls":1,"errors":0,"duration_ms":0,"result_bytes":0}`, string(out))
}

func TestRecordClone_IsDeep(t *testing.T) {
	orig := goldenRecord()
	clone := orig.Clone()
	require.Equal(t, orig, *clone)

	clone.Tools[0].Calls = 999
	*clone.ReportedCostMicros = 1
	*clone.EstimatedCostMicros = 2
	clone.Tools = append(clone.Tools, llmusage.ToolUsage{Tool: "extra"})

	want := goldenRecord()
	assert.Equal(t, want, orig)
}

func TestRecordClone_NilFieldsStayNil(t *testing.T) {
	clone := validRecord().Clone()
	assert.Nil(t, clone.ReportedCostMicros)
	assert.Nil(t, clone.EstimatedCostMicros)
	assert.Nil(t, clone.Tools)
}

func TestValidateReport_EmptyIsRefused(t *testing.T) {
	assert.ErrorIs(t, llmusage.ValidateReport(nil), llmusage.ErrInvalid)
}
