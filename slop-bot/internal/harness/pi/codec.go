package pi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/lowaa/slop-factory/slop-bot/internal/harness"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/rpcdriver"
)

// handshakeID is the get_state request Open waits on.
const handshakeID = "slop-bot-handshake"

// dialogs are the extension UI methods that block until answered.
var dialogs = []string{"select", "confirm", "input", "editor"}

// codec speaks pi's rpc protocol (docs/rpc.md, rpc-commands.md, json.md in
// the pi install). slop-bot's turn is pi's session-level run: from the first
// agent_start to agent_settled. agent_end isn't the end, because retries,
// compaction and queued steers can follow it within the same run.
type codec struct {
	id      string
	resumed bool
	log     *slog.Logger
	request atomic.Int64

	// prompts are the ids of prompt commands awaiting their response, so a
	// rejected or handled one can be told apart from other commands'.
	mu      sync.Mutex
	prompts map[string]bool

	// The rest is touched only by Decode, which rpcdriver calls from a single
	// goroutine.
	inTurn bool
	usage  *harness.Usage
	// failure is the run's last model error, cleared when a later request
	// succeeds (a retry), and reported if the run ends on it.
	failure string
}

func newCodec(id string, resumed bool, log *slog.Logger) *codec {
	return &codec{id: id, resumed: resumed, log: log, prompts: map[string]bool{}}
}

func (c *codec) nextID() string { return "slop-bot-" + strconv.FormatInt(c.request.Add(1), 10) }

type command struct {
	ID                string `json:"id,omitempty"`
	Type              string `json:"type"`
	Message           string `json:"message,omitempty"`
	StreamingBehavior string `json:"streamingBehavior,omitempty"`
}

type stateData struct {
	SessionID    string `json:"sessionId"`
	MessageCount int    `json:"messageCount"`
	Model        *struct {
		Provider string `json:"provider"`
		ID       string `json:"id"`
	} `json:"model"`
	ThinkingLevel string `json:"thinkingLevel"`
}

// Open sends get_state and waits for its answer: pi has no ready event and
// prints nothing until asked, so the answer is the handshake. It also
// re-checks the session id pi opened.
func (c *codec) Open(ctx context.Context, w *rpcdriver.Wire, next func(context.Context) ([]byte, error)) (string, error) {
	if err := write(w, command{ID: handshakeID, Type: "get_state"}); err != nil {
		return "", err
	}
	for {
		line, err := next(ctx)
		if err != nil {
			return "", err
		}
		var r record
		if err := json.Unmarshal(line, &r); err != nil || r.Type != "response" || r.ID != handshakeID {
			continue // nothing else is expected before the answer; nothing is lost by skipping it
		}
		if !r.Success {
			return "", fmt.Errorf("get_state failed: %s", r.Error)
		}
		var st stateData
		if err := json.Unmarshal(r.Data, &st); err != nil {
			return "", fmt.Errorf("decoding get_state: %w", err)
		}
		if st.SessionID != c.id {
			return "", fmt.Errorf("pi opened session %q, not %q", st.SessionID, c.id)
		}
		if st.Model == nil {
			return "", errors.New("pi has no model selected")
		}
		if c.resumed && st.MessageCount == 0 {
			// --session-id creates a missing session rather than failing, so a
			// resume that lost its session file starts afresh without context.
			c.log.Warn("pi: resumed session is empty", "session", c.id)
		}
		c.log.Info("pi: session open", "session", c.id, "model", st.Model.Provider+"/"+st.Model.ID,
			"thinking", st.ThinkingLevel, "messages", st.MessageCount)
		return c.id, nil
	}
}

// Send always uses `prompt` with streamingBehavior "steer". pi decides
// atomically: idle, it starts a run; mid-run, it queues the message as a
// steer, delivered after the current tool calls and before the next model
// call. The turn state slop-bot last decoded can be a moment stale either
// way, and the alternatives fail in that window: a plain prompt is rejected
// mid-run, and a `steer` sent just after the run settled sits in the queue
// until something else starts a run. follow_up isn't used: it waits for the
// whole run to finish, and a message should reach the agent at the next
// model call.
func (c *codec) Send(_ context.Context, w *rpcdriver.Wire, msg *harness.Message, _ bool) error {
	id := c.nextID()
	c.mu.Lock()
	c.prompts[id] = true
	c.mu.Unlock()
	return write(w, command{ID: id, Type: "prompt", Message: msg.Text, StreamingBehavior: "steer"})
}

// Interrupt sends abort: pi kills the running tool, ends the run with
// agent_settled {aborted:true}, and keeps the session usable. A steer still
// queued is delivered into the transcript, not lost, though no run follows
// it.
func (c *codec) Interrupt(_ context.Context, w *rpcdriver.Wire) error {
	return write(w, command{ID: c.nextID(), Type: "abort"})
}

type record struct {
	Type    string          `json:"type"`
	ID      string          `json:"id"`
	Command string          `json:"command"`
	Success bool            `json:"success"`
	Error   string          `json:"error"`
	Data    json.RawMessage `json:"data"`

	// message_end. Raw, because the extension UI's notify reuses the key
	// for a string.
	Message json.RawMessage `json:"message"`
	// tool_execution_*
	ToolName string `json:"toolName"`
	IsError  bool   `json:"isError"`
	// agent_settled
	Aborted bool `json:"aborted"`
	// auto_retry_end
	FinalError string `json:"finalError"`
	// extension_ui_request
	Method string `json:"method"`
	// compaction_end
	Result *struct {
		Usage *usage `json:"usage"`
	} `json:"result"`
	// extension_error
	Event string `json:"event"`
}

type message struct {
	Role string `json:"role"`
	// Content is a string on user messages and blocks on assistant ones.
	Content      json.RawMessage `json:"content"`
	StopReason   string          `json:"stopReason"`
	ErrorMessage string          `json:"errorMessage"`
	Usage        *usage          `json:"usage"`
}

type block struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type usage struct {
	Input      int64 `json:"input"`
	Output     int64 `json:"output"`
	CacheRead  int64 `json:"cacheRead"`
	CacheWrite int64 `json:"cacheWrite"`
	Cost       struct {
		Total float64 `json:"total"`
	} `json:"cost"`
}

type disposition struct {
	Disposition string `json:"disposition"`
}

func (c *codec) Decode(line []byte, w *rpcdriver.Wire) ([]harness.Event, error) {
	var r record
	if err := json.Unmarshal(line, &r); err != nil {
		return nil, err
	}
	switch r.Type {
	case "response":
		return c.response(&r), nil
	case "agent_start":
		if c.inTurn {
			return nil, nil // a retry or a queued message within the same run
		}
		c.inTurn = true
		c.usage, c.failure = &harness.Usage{CostEstimated: true}, ""
		return []harness.Event{{Kind: harness.TurnStarted}}, nil
	case "agent_settled":
		return c.settle(&r), nil
	case "tool_execution_start":
		return []harness.Event{{Kind: harness.ToolStarted, Tool: r.ToolName}}, nil
	case "tool_execution_end":
		return []harness.Event{{Kind: harness.ToolEnded, Tool: r.ToolName}}, nil
	case "message_end":
		var m message
		if err := json.Unmarshal(r.Message, &m); err != nil {
			return nil, err
		}
		return c.messageEnd(&m), nil
	case "auto_retry_end":
		if r.FinalError != "" {
			c.failure = r.FinalError
		}
	case "compaction_end":
		if r.Result != nil {
			c.addUsage(r.Result.Usage)
		}
	case "extension_ui_request":
		// Nobody is at a terminal to answer a dialog; unanswered, it would
		// hold the run for ever. The rest (notify, status) need no answer.
		if slices.Contains(dialogs, r.Method) {
			if err := write(w, map[string]any{"type": "extension_ui_response", "id": r.ID, "cancelled": true}); err != nil {
				return nil, err
			}
		}
	case "extension_error":
		c.log.Warn("pi: extension error", "event", r.Event, "err", r.Error)
	}
	return nil, nil
}

// response handles a command's answer. Only a prompt's matters: a rejected
// or handled prompt starts no run, so no agent_settled will close the turn
// the message was meant to start.
func (c *codec) response(r *record) []harness.Event {
	c.mu.Lock()
	isPrompt := c.prompts[r.ID]
	delete(c.prompts, r.ID)
	c.mu.Unlock()
	if !isPrompt {
		if !r.Success {
			c.log.Warn("pi: command failed", "command", r.Command, "err", r.Error)
		}
		return nil
	}
	var d disposition
	_ = json.Unmarshal(r.Data, &d) // absent on failure
	switch {
	case !r.Success && !c.inTurn:
		return []harness.Event{{Kind: harness.TurnStarted}, {Kind: harness.TurnEnded, Outcome: harness.TurnFailed, Text: r.Error}}
	case !r.Success:
		c.log.Warn("pi: a mid-turn message was rejected", "err", r.Error)
	case d.Disposition == "handled" && !c.inTurn:
		// An extension command or input handler consumed it.
		return []harness.Event{{Kind: harness.TurnStarted}, {Kind: harness.TurnEnded, Outcome: harness.TurnCompleted}}
	}
	return nil
}

func (c *codec) messageEnd(m *message) []harness.Event {
	if m.Role != "assistant" {
		return nil
	}
	c.addUsage(m.Usage)
	switch m.StopReason {
	case "error":
		c.failure = m.ErrorMessage
	case "aborted":
	default:
		c.failure = ""
	}
	var blocks []block
	if err := json.Unmarshal(m.Content, &blocks); err != nil {
		return nil
	}
	var evs []harness.Event
	for _, b := range blocks {
		if b.Type == "text" && b.Text != "" {
			evs = append(evs, harness.Event{Kind: harness.Text, Text: b.Text})
		}
	}
	return evs
}

func (c *codec) addUsage(u *usage) {
	if u == nil || c.usage == nil {
		return
	}
	c.usage.InputTokens += u.Input
	c.usage.OutputTokens += u.Output
	c.usage.CacheReadTokens += u.CacheRead
	c.usage.CacheWriteTokens += u.CacheWrite
	c.usage.CostUSD += u.Cost.Total
}

// settle ends the turn. An abort wins over the error it causes: the aborted
// model request ends with stopReason "error", "This operation was aborted".
func (c *codec) settle(r *record) []harness.Event {
	if !c.inTurn {
		return nil
	}
	c.inTurn = false
	end := harness.Event{Kind: harness.TurnEnded, Outcome: harness.TurnCompleted}
	switch {
	case r.Aborted:
		end.Outcome = harness.TurnInterrupted
	case c.failure != "":
		end.Outcome, end.Text = harness.TurnFailed, c.failure
	}
	evs := []harness.Event{{Kind: harness.UsageReport, Usage: c.usage}, end}
	c.usage, c.failure = nil, ""
	return evs
}

func write(w *rpcdriver.Wire, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("pi: encoding: %w", err)
	}
	return w.WriteLine(b)
}
