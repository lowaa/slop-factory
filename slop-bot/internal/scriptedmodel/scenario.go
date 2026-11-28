// Package scriptedmodel is a model server that plays back a script, so the
// real harness CLIs can be run in tests with no real model behind them.
// README.md describes the design and the harnesses it was validated against.
package scriptedmodel

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

// A Scenario is the ordered turns the scripted model plays back. A turn is
// text and/or tool calls, independent of any wire API: that is what lets a
// scenario written for one harness run unchanged against another.
type Scenario struct {
	Name  string  `json:"name"`
	Turns []*Turn `json:"turns"`
}

// A Turn is one model response.
type Turn struct {
	Text      string      `json:"text,omitempty"`
	ToolCalls []*ToolCall `json:"tool_calls,omitempty"`
	Usage     Usage       `json:"usage"`
	// Gate holds the turn back until the test releases it: how a test acts
	// between two turns, with a synchronisation point where a sleep would be
	// a race.
	Gate string `json:"gate,omitempty"`
}

// A ToolCall is a call the model makes in a turn.
type ToolCall struct {
	Name  string         `json:"name"`
	Input map[string]any `json:"input"`
}

// Usage is the token counts a turn reports, in Anthropic's terms; each
// adapter maps them onto its API's fields.
type Usage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
}

// ErrScenario is what every invalid scenario's error wraps.
var ErrScenario = errors.New("invalid scenario")

// ParseScenario reads a scenario's JSON. Unknown fields are refused, so a
// typo in a script fails loudly instead of playing a turn without its gate.
func ParseScenario(data []byte) (*Scenario, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var scenario Scenario
	if err := dec.Decode(&scenario); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrScenario, err)
	}
	if scenario.Turns == nil {
		return nil, fmt.Errorf("%w: a scenario is {name, turns: [...]}", ErrScenario)
	}
	if scenario.Name == "" {
		scenario.Name = "unnamed"
	}
	if err := scenario.Validate(); err != nil {
		return nil, err
	}
	return &scenario, nil
}

// Validate reports the first turn that cannot be played.
func (s *Scenario) Validate() error {
	for i, turn := range s.Turns {
		if turn == nil || (turn.Text == "" && len(turn.ToolCalls) == 0) {
			return fmt.Errorf("%w: turn %d needs text or at least one tool call", ErrScenario, i+1)
		}
		for _, call := range turn.ToolCalls {
			if call == nil || call.Name == "" {
				return fmt.Errorf("%w: turn %d has a tool call with no name", ErrScenario, i+1)
			}
			if _, err := json.Marshal(call.Input); err != nil {
				return fmt.Errorf("%w: turn %d tool call %q input: %w", ErrScenario, i+1, call.Name, err)
			}
		}
	}
	return nil
}

func (c *ToolCall) inputJSON() string {
	if c.Input == nil {
		return "{}"
	}
	// Validate, which every loaded scenario passed, marshalled it already.
	data, _ := json.Marshal(c.Input)
	return string(data)
}

func (c *ToolCall) inputOrEmpty() map[string]any {
	if c.Input == nil {
		return map[string]any{}
	}
	return c.Input
}
