package pi

import (
	"cmp"
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

	mu sync.Mutex
	// prompts are the prompt commands awaiting their response, by request
	// id, so a rejected or handled one can be told apart from other
	// commands' and a rejected one can be sent again.
	prompts map[string]*pending
	// held are messages pi rejected while busy (it refuses even a steer
	// while compacting), so a message Send accepted is never silently
	// dropped. They go again when the compaction ends, when the run settles
	// (keeping the turn open), or after an interrupt, ahead of the next Send.
	held []string

	// The rest is touched only by Decode, which rpcdriver calls from a single
	// goroutine.
	inTurn     bool
	compacting bool
	// deferred is a settled run's turn end, held back while held messages
	// are resent at the settle: they start the next run within the same
	// turn, so Stop keeps waiting for it. It is emitted if none was taken.
	deferred *bridge
	usage    *harness.Usage
	// failure is the run's last model error, cleared when a later request
	// succeeds (a retry), and reported if the run ends on it.
	failure string
}

func newCodec(id string, resumed bool, log *slog.Logger) *codec {
	return &codec{id: id, resumed: resumed, log: log, prompts: map[string]*pending{}}
}

// pending is a prompt awaiting pi's response.
type pending struct {
	text string
	// bridging marks a held message resent at a settle.
	bridging bool
}

// bridge is a deferred turn end and the resent prompts it waits on.
type bridge struct {
	end         []harness.Event
	outstanding int
	taken       bool
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
		if err := json.Unmarshal(line, &r); err != nil {
			continue
		}
		if r.Type == "extension_ui_request" {
			// One of the user's extensions may open a dialog at session start,
			// before pi answers; unanswered, it would block.
			if err := c.cancelDialog(&r, w); err != nil {
				return "", err
			}
			continue
		}
		if r.Type != "response" || r.ID != handshakeID {
			continue // no run has started, so no other record matters yet
		}
		if err := c.checkState(&r); err != nil {
			return "", err
		}
		return c.id, nil
	}
}

// checkState checks get_state's answer: the session slop-bot chose, with a
// model selected.
func (c *codec) checkState(r *record) error {
	if !r.Success {
		return fmt.Errorf("get_state failed: %s", r.Error)
	}
	var st stateData
	if err := json.Unmarshal(r.Data, &st); err != nil {
		return fmt.Errorf("decoding get_state: %w", err)
	}
	if st.SessionID != c.id {
		return fmt.Errorf("pi opened session %q, not %q", st.SessionID, c.id)
	}
	if st.Model == nil {
		return errors.New("pi has no model selected")
	}
	if c.resumed && st.MessageCount == 0 {
		// --session-id creates a missing session rather than failing, so a
		// resume that lost its session file starts afresh without context.
		c.log.Warn("pi: resumed session is empty", "session", c.id)
	}
	c.log.Info("pi: session open", "session", c.id, "model", st.Model.Provider+"/"+st.Model.ID,
		"thinking", st.ThinkingLevel, "messages", st.MessageCount)
	return nil
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
	// Messages held over an interrupt go first, in the order they were sent.
	for _, text := range c.takeHeld() {
		if err := c.prompt(w, text, false); err != nil {
			return err
		}
	}
	return c.prompt(w, msg.Text, false)
}

func (c *codec) prompt(w *rpcdriver.Wire, text string, bridging bool) error {
	id := c.nextID()
	c.mu.Lock()
	c.prompts[id] = &pending{text: text, bridging: bridging}
	c.mu.Unlock()
	return write(w, command{ID: id, Type: "prompt", Message: text, StreamingBehavior: "steer"})
}

func (c *codec) hold(text string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.held = append(c.held, text)
}

func (c *codec) takeHeld() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	held := c.held
	c.held = nil
	return held
}

// resendHeld sends the held messages again, returning how many it wrote. A
// write fails only when pi is gone, and the exit will be reported, so it is
// logged rather than costing the events decoded with it.
func (c *codec) resendHeld(w *rpcdriver.Wire, bridging bool) int {
	held := c.takeHeld()
	for i, text := range held {
		if err := c.prompt(w, text, bridging); err != nil {
			c.log.Warn("pi: resending a held message", "err", err, "lost", len(held)-i)
			return i
		}
	}
	return len(held)
}

// Interrupt sends abort: pi kills the running tool, ends the run with
// agent_settled {aborted:true}, and keeps the session usable. A steer still
// queued in pi is delivered into the transcript, not lost, though no run
// follows it; one slop-bot is holding stays held until the next Send, so an
// interrupt never restarts the agent.
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
		return c.settle(&r, w), nil
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
	case "compaction_start":
		c.compacting = true
	case "compaction_end":
		c.compacting = false
		evs := c.compacted(&r)
		if !r.Aborted {
			// An aborted one is part of an interrupt: the held messages wait
			// for the next Send rather than restart the agent.
			c.resendHeld(w, false)
		}
		return evs, nil
	case "extension_ui_request":
		return nil, c.cancelDialog(&r, w)
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
	p, isPrompt := c.prompts[r.ID]
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
	if p.bridging {
		return c.bridged(r, &d, p.text)
	}
	switch {
	case !r.Success && (c.inTurn || c.compacting):
		c.log.Warn("pi: a message was rejected while pi was busy; holding it to send again", "err", r.Error)
		c.hold(p.text)
	case !r.Success:
		return []harness.Event{{Kind: harness.TurnStarted}, {Kind: harness.TurnEnded, Outcome: harness.TurnFailed, Text: r.Error}}
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
		c.failure = cmp.Or(m.ErrorMessage, "the model request failed")
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

// compacted counts a compaction's model usage: into the run's, or, for a
// compaction outside a run, as a report of its own.
func (c *codec) compacted(r *record) []harness.Event {
	if r.Result == nil || r.Result.Usage == nil {
		return nil
	}
	if c.inTurn {
		c.addUsage(r.Result.Usage)
		return nil
	}
	c.usage = &harness.Usage{CostEstimated: true}
	c.addUsage(r.Result.Usage)
	u := c.usage
	c.usage = nil
	return []harness.Event{{Kind: harness.UsageReport, Usage: u}}
}

// cancelDialog answers an extension's dialog as dismissed: nobody is at a
// terminal to answer it, and unanswered it would block for ever. The other
// methods (notify, status) need no answer.
func (c *codec) cancelDialog(r *record, w *rpcdriver.Wire) error {
	if !slices.Contains(dialogs, r.Method) {
		return nil
	}
	return write(w, map[string]any{"type": "extension_ui_response", "id": r.ID, "cancelled": true})
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
// With messages held, and no abort, it resends them and defers the end: the
// run they start continues the same turn, and its settle ends it.
func (c *codec) settle(r *record, w *rpcdriver.Wire) []harness.Event {
	if !c.inTurn {
		return nil
	}
	end := harness.Event{Kind: harness.TurnEnded, Outcome: harness.TurnCompleted}
	switch {
	case r.Aborted:
		end.Outcome = harness.TurnInterrupted
	case c.failure != "":
		end.Outcome, end.Text = harness.TurnFailed, c.failure
	}
	evs := []harness.Event{{Kind: harness.UsageReport, Usage: c.usage}, end}
	c.deferred = nil
	if !r.Aborted {
		if n := c.resendHeld(w, true); n > 0 {
			c.deferred = &bridge{end: evs, outstanding: n}
			c.failure = "" // the next run's outcome is its own; its usage adds to this
			return nil
		}
	}
	c.inTurn = false
	c.usage, c.failure = nil, ""
	return evs
}

// bridged handles the response to a message resent at a settle. Once every
// one has answered and none started a run, the deferred turn end goes out.
// One pi rejects again stays held, for the next Send.
func (c *codec) bridged(r *record, d *disposition, text string) []harness.Event {
	b := c.deferred
	if b == nil {
		return nil // its run has already settled the turn
	}
	b.outstanding--
	switch {
	case !r.Success:
		c.log.Warn("pi: a held message was rejected again; holding it for the next message", "err", r.Error)
		c.hold(text)
	case d.Disposition != "handled":
		b.taken = true
	}
	if b.outstanding > 0 || b.taken {
		return nil
	}
	c.deferred = nil
	c.inTurn = false
	c.usage, c.failure = nil, ""
	return b.end
}

func write(w *rpcdriver.Wire, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("pi: encoding: %w", err)
	}
	return w.WriteLine(b)
}
