package llmusage_test

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	llmusage "github.com/lowaa/slop-factory/go-llm-usage"
)

// The clients (the Claude hook, the supervisor, the pi extension) each carry
// their own copy of the label patterns and read this same file, so a value one
// accepts and the server refuses cannot slip in: the server refuses the whole report.
func TestRecordLabelsFixture_ServerValidation(t *testing.T) {
	data, err := os.ReadFile("fixtures/record_labels.json")
	require.NoError(t, err)
	var fixture map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(data, &fixture))

	type cases struct {
		Valid   []string `json:"valid"`
		Invalid []string `json:"invalid"`
	}
	build := map[string]func(value string) *llmusage.Record{
		"provider": func(v string) *llmusage.Record { r := validRecord(); r.Provider = v; return r },
		"model":    func(v string) *llmusage.Record { r := validRecord(); r.Model = v; return r },
		"tool": func(v string) *llmusage.Record {
			r := validRecord()
			r.Tools = []llmusage.ToolUsage{{Tool: v, Calls: 1}}
			return r
		},
	}
	for label, newRecord := range build {
		var c cases
		require.NoError(t, json.Unmarshal(fixture[label], &c), label)
		require.NotEmpty(t, c.Valid, label)
		require.NotEmpty(t, c.Invalid, label)
		for _, v := range c.Valid {
			assert.NoError(t, newRecord(v).Validate(), "%s %q", label, v)
		}
		for _, v := range c.Invalid {
			assert.Error(t, newRecord(v).Validate(), "%s %q", label, v)
		}
	}
}
