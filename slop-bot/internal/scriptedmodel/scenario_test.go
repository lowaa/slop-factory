package scriptedmodel

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseScenarioReadsTurns(t *testing.T) {
	t.Parallel()
	scenario, err := ParseScenario([]byte(`{"name": "s", "turns": [
		{"tool_calls": [{"name": "bash", "input": {"command": "ls"}}], "usage": {"input_tokens": 5}},
		{"text": "done", "gate": "g"}
	]}`))
	require.NoError(t, err)
	assert.Equal(t, "s", scenario.Name)
	assert.Equal(t, []*ToolCall{call("bash", map[string]any{"command": "ls"})}, scenario.Turns[0].ToolCalls)
	assert.Equal(t, Usage{InputTokens: 5}, scenario.Turns[0].Usage)
	assert.Equal(t, "done", scenario.Turns[1].Text)
	assert.Equal(t, "g", scenario.Turns[1].Gate)
}

func TestParseScenarioNamesAnUnnamedOne(t *testing.T) {
	t.Parallel()
	scenario, err := ParseScenario([]byte(`{"turns": [{"text": "x"}]}`))
	require.NoError(t, err)
	assert.Equal(t, "unnamed", scenario.Name)
}

func TestParseScenarioRefuses(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, json, message string
	}{
		{"an empty turn", `{"turns": [{}]}`, "needs text or at least one tool call"},
		{"an unknown turn field", `{"turns": [{"text": "x", "tools": []}]}`, `unknown field "tools"`},
		{"an unknown usage field", `{"turns": [{"text": "x", "usage": {"tokens": 1}}]}`, `unknown field "tokens"`},
		{"an unknown tool call field", `{"turns": [{"tool_calls": [{"name": "b", "args": {}}]}]}`, `unknown field "args"`},
		{"a tool call with no name", `{"turns": [{"tool_calls": [{"input": {}}]}]}`, "tool call with no name"},
		{"no turns", `{"name": "s"}`, "a scenario is {name, turns: [...]}"},
		{"not an object", `[]`, "cannot unmarshal"},
		{"a stray brace's trailing data", `{"turns": [{"text": "a"}]}, {"text": "b"}]}`, "after the scenario"},
		{"two scenarios", `{"turns": [{"text": "a"}]} {"turns": [{"text": "b"}]}`, "after the scenario"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := ParseScenario([]byte(tc.json))
			require.ErrorIs(t, err, ErrScenario)
			assert.Contains(t, err.Error(), tc.message)
		})
	}
}
