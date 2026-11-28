package fake

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/lowaa/slop-factory/slop-bot/internal/harness"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/rpcdriver"
)

// codec is the fake's protocol for rpcdriver.
type codec struct{}

func (codec) Open(ctx context.Context, w *rpcdriver.Wire, next func(context.Context) ([]byte, error)) (string, error) {
	if err := writeJSON(w, inMsg{Op: "hello"}); err != nil {
		return "", err
	}
	for {
		line, err := next(ctx)
		if err != nil {
			return "", err
		}
		var m outMsg
		if json.Unmarshal(line, &m) == nil && m.Ev == "hello" {
			return m.ID, nil
		}
	}
}

func (codec) Send(_ context.Context, w *rpcdriver.Wire, msg *harness.Message, _ bool) error {
	return writeJSON(w, inMsg{Op: "user", Text: msg.Text, From: msg.From.String()})
}

func (codec) Interrupt(_ context.Context, w *rpcdriver.Wire) error {
	return writeJSON(w, inMsg{Op: "interrupt"})
}

func (codec) Decode(line []byte, _ *rpcdriver.Wire) ([]harness.Event, error) {
	var m outMsg
	if err := json.Unmarshal(line, &m); err != nil {
		return nil, err
	}
	switch m.Ev {
	case "turn_start":
		return []harness.Event{{Kind: harness.TurnStarted}}, nil
	case "turn_end":
		return []harness.Event{{Kind: harness.TurnEnded, Outcome: harness.TurnOutcome(m.Outcome)}}, nil
	case "tool_start":
		return []harness.Event{{Kind: harness.ToolStarted, Tool: m.Tool}}, nil
	case "tool_end":
		return []harness.Event{{Kind: harness.ToolEnded, Tool: m.Tool}}, nil
	case "text":
		return []harness.Event{{Kind: harness.Text, Text: m.Text}}, nil
	case "limit":
		l := &harness.Limit{UsedPercent: m.Used}
		if m.ResetsAt != 0 {
			l.ResetsAt = time.Unix(m.ResetsAt, 0).UTC()
		}
		return []harness.Event{{Kind: harness.LimitReport, Limit: l}}, nil
	case "usage":
		return []harness.Event{{Kind: harness.UsageReport, Usage: &harness.Usage{InputTokens: m.Input, OutputTokens: m.Output}}}, nil
	default:
		return nil, fmt.Errorf("fake: unknown event %q", m.Ev)
	}
}

func writeJSON(w *rpcdriver.Wire, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return w.WriteLine(b)
}
