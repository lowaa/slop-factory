package claudecode

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/lowaa/slop-factory/slop-bot/internal/harness"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/rpcdriver"
)

// codec speaks Claude Code's stream-json: one JSON record per line, a user
// message per input record, and a `result` record ending each turn.
type codec struct {
	id string
	// inTurn is touched only by Decode, which rpcdriver calls from a single
	// goroutine.
	inTurn bool
	// metered is set when system/init says the credential is an API key:
	// only then is total_cost_usd what is billed. On the subscription login
	// (the default: agentproc keeps API keys out) it is an estimate.
	metered bool
	request atomic.Int64
}

// Open has no handshake to wait for: Claude Code prints nothing until the
// first message arrives, and slop-bot chose the session id itself
// (--session-id), or is resuming a known one.
func (c *codec) Open(context.Context, *rpcdriver.Wire, func(context.Context) ([]byte, error)) (string, error) {
	return c.id, nil
}

type userRecord struct {
	Type    string      `json:"type"`
	Message userMessage `json:"message"`
}

type userMessage struct {
	Role    string        `json:"role"`
	Content []textContent `json:"content"`
}

type textContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// Send writes a user record. Mid-turn it is the same record: Claude Code
// absorbs it into the running turn at the next model call (protocol spike),
// so it steers rather than queueing a new turn.
func (c *codec) Send(_ context.Context, w *rpcdriver.Wire, msg *harness.Message, _ bool) error {
	return write(w, userRecord{
		Type:    "user",
		Message: userMessage{Role: "user", Content: []textContent{{Type: "text", Text: msg.Text}}},
	})
}

type controlRequest struct {
	Type      string         `json:"type"`
	RequestID string         `json:"request_id"`
	Request   map[string]any `json:"request"`
}

func (c *codec) Interrupt(_ context.Context, w *rpcdriver.Wire) error {
	id := "slop-bot-" + strconv.FormatInt(c.request.Add(1), 10)
	return write(w, controlRequest{Type: "control_request", RequestID: id, Request: map[string]any{"subtype": "interrupt"}})
}

type record struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype"`
	// system/init
	APIKeySource string `json:"apiKeySource"`
	Message      struct {
		Content []struct {
			Type string `json:"type"`
			Name string `json:"name"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"message"`
	// result
	IsError        bool    `json:"is_error"`
	Result         string  `json:"result"`
	TerminalReason string  `json:"terminal_reason"`
	TotalCostUSD   float64 `json:"total_cost_usd"`
	Usage          struct {
		InputTokens              int64 `json:"input_tokens"`
		OutputTokens             int64 `json:"output_tokens"`
		CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
		CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
	} `json:"usage"`
	// rate_limit_event
	RateLimitInfo *struct {
		Status         string `json:"status"`
		ResetsAt       int64  `json:"resetsAt"`
		RateLimitType  string `json:"rateLimitType"`
		UnifiedWindows map[string]struct {
			Utilization float64 `json:"utilization"`
			ResetsAt    int64   `json:"resetsAt"`
		} `json:"unifiedWindows"`
	} `json:"rate_limit_info"`
}

func (c *codec) Decode(line []byte, _ *rpcdriver.Wire) ([]harness.Event, error) {
	var r record
	if err := json.Unmarshal(line, &r); err != nil {
		return nil, err
	}
	var evs []harness.Event
	// Every record of a turn means the turn is running: system/init opens
	// each turn, but a resumed session's first record may be another kind.
	if !c.inTurn && r.Type != "control_response" && r.Type != "result" {
		c.inTurn = true
		evs = append(evs, harness.Event{Kind: harness.TurnStarted})
	}
	switch r.Type {
	case "system":
		if r.Subtype == "init" {
			c.metered = r.APIKeySource != "" && r.APIKeySource != "none"
		}
	case "assistant":
		for _, b := range r.Message.Content {
			switch b.Type {
			case "text":
				evs = append(evs, harness.Event{Kind: harness.Text, Text: b.Text})
			case "tool_use":
				evs = append(evs, harness.Event{Kind: harness.ToolStarted, Tool: b.Name})
			}
		}
	case "user":
		for _, b := range r.Message.Content {
			if b.Type == "tool_result" {
				evs = append(evs, harness.Event{Kind: harness.ToolEnded})
			}
		}
	case "rate_limit_event":
		for _, l := range limitsFrom(&r) {
			evs = append(evs, harness.Event{Kind: harness.LimitReport, Limit: l})
		}
	case "result":
		c.inTurn = false
		evs = append(evs,
			harness.Event{Kind: harness.UsageReport, Usage: &harness.Usage{
				InputTokens:      r.Usage.InputTokens,
				OutputTokens:     r.Usage.OutputTokens,
				CacheReadTokens:  r.Usage.CacheReadInputTokens,
				CacheWriteTokens: r.Usage.CacheCreationInputTokens,
				CostUSD:          r.TotalCostUSD,
				CostEstimated:    !c.metered,
			}},
			turnEnded(&r),
		)
	}
	return evs, nil
}

func turnEnded(r *record) harness.Event {
	switch {
	case !r.IsError && r.Subtype == "success":
		return harness.Event{Kind: harness.TurnEnded, Outcome: harness.TurnCompleted}
	case r.TerminalReason == "aborted_tools" || r.TerminalReason == "aborted_streaming":
		return harness.Event{Kind: harness.TurnEnded, Outcome: harness.TurnInterrupted}
	default:
		return harness.Event{Kind: harness.TurnEnded, Outcome: harness.TurnFailed, Text: r.Result}
	}
}

// limitsFrom reports each of the plan's windows, sorted by name. A
// rejection is a limit reached whatever the windows say (it may name one
// they don't carry, such as a model's weekly limit, or come with none), so
// it reports its limit type as full until its reset.
func limitsFrom(r *record) []*harness.Limit {
	info := r.RateLimitInfo
	if info == nil {
		return nil
	}
	names := make([]string, 0, len(info.UnifiedWindows))
	for name := range info.UnifiedWindows {
		names = append(names, name)
	}
	slices.Sort(names)
	var out []*harness.Limit
	for _, name := range names {
		w := info.UnifiedWindows[name]
		out = append(out, &harness.Limit{Window: name, UsedPercent: w.Utilization * 100, ResetsAt: unixOrZero(w.ResetsAt)})
	}
	if info.Status == "rejected" {
		window := info.RateLimitType
		if window == "" {
			window = "rejected"
		}
		out = append(out, &harness.Limit{Window: window, UsedPercent: 100, ResetsAt: unixOrZero(info.ResetsAt)})
	}
	return out
}

// unixOrZero is a reset time, or zero when the harness gave none.
func unixOrZero(s int64) time.Time {
	if s == 0 {
		return time.Time{}
	}
	return time.Unix(s, 0).UTC()
}

func write(w *rpcdriver.Wire, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("claudecode: encoding: %w", err)
	}
	return w.WriteLine(b)
}
