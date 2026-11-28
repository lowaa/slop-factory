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

// ErrEmptySession is a resume that found no conversation: pi made a new,
// empty session in its place.
var ErrEmptySession = errors.New("pi: the resumed session is empty")

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
	// while compacting), so a message Send accepted isn't silently dropped
	// while pi runs. They go again when the compaction ends, when the run
	// settles (keeping the turn open), or after an interrupt, ahead of the
	// next Send. state.db has marked them delivered, so a pi that exits
	// first leaves them Undelivered, for the core to queue again. Taking
	// them and writing them is one step under mu, so a Send and a resend
	// can't reorder them.
	held []*harness.Message

	// The rest is touched only by Decode, which rpcdriver calls from a single
	// goroutine.
	inTurn     bool
	compacting bool
	// heldTurn is a turn rpcdriver opened for an idle Send whose message pi
	// rejected while compacting outside a run: no run's settle will close
	// it, so the held messages' run, or the codec, has to.
	heldTurn bool
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
	msg *harness.Message
	// bridging marks a held message resent at a settle.
	bridging bool
	// opened marks a message sent while rpcdriver had no turn open, so its
	// Send opened one.
	opened bool
}

// bridge is a deferred turn end and the resent prompts it waits on.
type bridge struct {
	end         []harness.Event
	outstanding int
	taken       bool
}

// Undelivered implements rpcdriver.Holder: the held messages.
func (c *codec) Undelivered() []*harness.Message {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]*harness.Message(nil), c.held...)
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
		// resume that can't find its session (lost, or looked for in another
		// cwd's project) would carry on with neither history nor kick-off.
		return fmt.Errorf("%w: %s", ErrEmptySession, c.id)
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
func (c *codec) Send(_ context.Context, w *rpcdriver.Wire, msg *harness.Message, mid bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	// Messages held over an interrupt go first, in the order they were sent.
	if n, err := c.sendHeld(w, &pending{opened: !mid}); err != nil {
		c.log.Warn("pi: sending a held message", "err", err, "unsent", n)
		return err
	}
	return c.prompt(w, &pending{msg: msg, opened: !mid})
}

// prompt writes one message; mu is held.
func (c *codec) prompt(w *rpcdriver.Wire, p *pending) error {
	id := c.nextID()
	c.prompts[id] = p
	if err := writeCarrying(w, p.msg, command{ID: id, Type: "prompt", Message: p.msg.Text, StreamingBehavior: "steer"}); err != nil {
		delete(c.prompts, id)
		return err
	}
	return nil
}

func (c *codec) hold(msg *harness.Message) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.held = append(c.held, msg)
}

// sendHeld writes the held messages, each as like (bridging, opened). On a
// failed write the rest stay held, and n says how many. mu is held.
func (c *codec) sendHeld(w *rpcdriver.Wire, like *pending) (n int, err error) {
	held := c.held
	c.held = nil
	for i, msg := range held {
		p := *like
		p.msg = msg
		if err := c.prompt(w, &p); err != nil {
			c.held = append(held[i:], c.held...)
			return len(held) - i, err
		}
	}
	return 0, nil
}

// resendHeld sends the held messages again, returning how many it wrote. A
// write fails only when pi is gone, and the exit will be reported, so it is
// logged rather than costing the events decoded with it; what it didn't
// write stays held, Undelivered.
func (c *codec) resendHeld(w *rpcdriver.Wire, bridging bool) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	total := len(c.held)
	unsent, err := c.sendHeld(w, &pending{bridging: bridging})
	if err != nil {
		c.log.Warn("pi: resending a held message", "err", err, "unsent", unsent)
	}
	return total - unsent
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
		return c.agentStart(), nil
	case "agent_settled":
		return c.settle(&r, w), nil
	case "tool_execution_start":
		return []harness.Event{{Kind: harness.ToolStarted, Tool: r.ToolName}}, nil
	case "tool_execution_end":
		return []harness.Event{{Kind: harness.ToolEnded, Tool: r.ToolName}}, nil
	case "message_end":
		return c.decodeMessageEnd(&r)
	case "auto_retry_end":
		if r.FinalError != "" {
			c.failure = r.FinalError
		}
	case "compaction_start":
		c.compacting = true
	case "compaction_end":
		return c.compactionEnd(&r, w), nil
	case "extension_ui_request":
		return nil, c.cancelDialog(&r, w)
	case "extension_error":
		c.log.Warn("pi: extension error", "event", r.Event, "err", r.Error)
	}
	return nil, nil
}

func (c *codec) agentStart() []harness.Event {
	if c.inTurn {
		return nil // a retry or a queued message within the same run
	}
	c.inTurn, c.heldTurn = true, false
	c.usage, c.failure = &harness.Usage{CostEstimated: true}, ""
	return []harness.Event{{Kind: harness.TurnStarted}}
}

// noRun is a turn that starts and ends with no run: a prompt pi rejected or
// handled, or one held open by a rejected message that an interrupt ends.
func (c *codec) noRun(outcome harness.TurnOutcome, text string) []harness.Event {
	c.heldTurn = false
	return []harness.Event{{Kind: harness.TurnStarted}, {Kind: harness.TurnEnded, Outcome: outcome, Text: text}}
}

// Oversized implements rpcdriver.Oversizer: a record too long to read
// whole (agent_end repeats the run; a tool's end, its whole output) is
// decoded from its fields before the cut, so a tool's end still says
// which tool.
func (c *codec) Oversized(prefix []byte, w *rpcdriver.Wire) ([]harness.Event, error) {
	b := rpcdriver.PrefixObject(prefix)
	if b == nil {
		return nil, nil
	}
	return c.Decode(b, w)
}

func (c *codec) decodeMessageEnd(r *record) ([]harness.Event, error) {
	if len(r.Message) == 0 {
		return nil, nil // cut off (Oversized)
	}
	var m message
	if err := json.Unmarshal(r.Message, &m); err != nil {
		return nil, err
	}
	return c.messageEnd(&m), nil
}

func (c *codec) compactionEnd(r *record, w *rpcdriver.Wire) []harness.Event {
	c.compacting = false
	evs := c.compacted(r)
	switch {
	case !r.Aborted:
		c.resendHeld(w, false)
	case c.heldTurn && !c.inTurn:
		// An aborted one is part of an interrupt: the held messages wait
		// for the next Send rather than restart the agent, so nothing else
		// will end the turn their Send opened.
		evs = append(evs, c.noRun(harness.TurnInterrupted, "")...)
	}
	return evs
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
		return c.bridged(r, &d, p.msg)
	}
	switch {
	case !r.Success && (c.inTurn || c.compacting):
		c.log.Warn("pi: a message was rejected while pi was busy; holding it to send again", "err", r.Error)
		c.hold(p.msg)
		if p.opened && !c.inTurn {
			c.heldTurn = true
		}
	case !r.Success:
		return c.noRun(harness.TurnFailed, r.Error)
	case d.Disposition == "handled" && !c.inTurn:
		// An extension command or input handler consumed it.
		return c.noRun(harness.TurnCompleted, "")
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
func (c *codec) bridged(r *record, d *disposition, msg *harness.Message) []harness.Event {
	b := c.deferred
	if b == nil {
		return nil // its run has already settled the turn
	}
	b.outstanding--
	switch {
	case !r.Success:
		c.log.Warn("pi: a held message was rejected again; holding it for the next message", "err", r.Error)
		c.hold(msg)
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

func write(w *rpcdriver.Wire, v any) error { return writeCarrying(w, nil, v) }

// writeCarrying writes v, a line that carries msg (nil for none): until it
// is written, msg is Undelivered (rpcdriver.Wire.WriteMessage). A held
// message resent goes this way too, so one queued when pi exits is still
// accounted for.
func writeCarrying(w *rpcdriver.Wire, msg *harness.Message, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("pi: encoding: %w", err)
	}
	return w.WriteMessage(b, msg)
}
