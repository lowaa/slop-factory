package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-bot/internal/harness"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/codex/protocol"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/rpcdriver"
	"github.com/lowaa/slop-factory/slop-bot/internal/ident"
)

// ErrNoRollout is a resume of a thread Codex has no rollout file for: the
// conversation is gone, and resuming it can't work.
var ErrNoRollout = errors.New("codex has no rollout for the thread")

// noRollout is thread/resume's error for a thread with no rollout file
// (recorded: testdata/resume_missing.jsonl). Any other error is a failure,
// never "missing", so a changed message fails loudly.
const noRollout = "no rollout found for thread id"

// codecOptions are what one conversation's codec sends at the handshake.
type codecOptions struct {
	logger *slog.Logger
	// version is the client version slop-bot reports in initialize.
	version string
	start   *protocol.ThreadStartParams
	// resume is the thread to resume; then resumeParams are sent instead of
	// start.
	resume       string
	resumeParams *protocol.ThreadResumeParams
	// effort is the reasoning effort each turn/start asks for.
	effort string
	// permissive approves the server requests Codex sends (every command,
	// when guarded; any that slip through, when not); without it they are
	// declined (approvals.go).
	permissive bool
	// guard, when set, is asked about each command Codex asks to run, as
	// by, the agent (approvals.go). Only a permissive agent has one.
	guard Guard
	by    ident.Principal
}

// codec speaks Codex's app-server JSON-RPC: requests and their responses,
// notifications for everything that happens in a turn, and server requests
// (approvals) that must be answered or the turn waits for ever.
//
// Send and Interrupt run on the caller's goroutine and Decode on
// rpcdriver's, so the protocol state is behind mu. Writes happen under it
// too, so a request's id and the state that expects its response stay in
// step.
type codec struct {
	opts *codecOptions

	mu       sync.Mutex
	nextID   int64
	pending  map[int64]*request
	threadID string
	// turnID is the running turn, from turn/start's response or
	// turn/started; "" when idle.
	turnID string
	// starting is a turn/start in flight: its turn id isn't known yet, so a
	// message can't be steered and an interrupt can't name it.
	starting bool
	// queued are messages sent while starting, steered once the turn id is
	// known.
	queued []string
	// interruptAsked is an interrupt that waits for the turn id.
	interruptAsked bool
	// interrupted is a turn slop-bot interrupted, whose commands are
	// cleaned up when it ends.
	interrupted bool
	// tools are the tool items started and not yet completed, by item id.
	tools map[string]string
	usage *harness.Usage
	// lastError is the last unretried error notification, for a turn that
	// fails without saying why.
	lastError string
}

type requestKind int

const (
	reqInitialize requestKind = iota + 1
	reqThread
	reqTurn
	reqSteer
	reqInterrupt
	reqClean
)

type request struct {
	kind requestKind
	// text and turn are a steer's message and the turn it named, for when
	// the steer is refused.
	text, turn string
}

func newCodec(opts *codecOptions) *codec {
	return &codec{opts: opts, pending: map[int64]*request{}, tools: map[string]string{}}
}

// message is any JSON-RPC message: a request or notification has Method, a
// response ID with Result or Error.
type message struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string { return fmt.Sprintf("%s (%d)", e.Message, e.Code) }

// Open runs initialize, initialized, then thread/start or thread/resume,
// and returns the thread id Codex chose (or kept, on resume).
func (c *codec) Open(ctx context.Context, w *rpcdriver.Wire, next func(context.Context) ([]byte, error)) (string, error) {
	_, err := c.call(ctx, w, next, reqInitialize, protocol.MethodInitialize, &protocol.InitializeParams{
		ClientInfo: protocol.ClientInfo{Name: "slop-bot", Title: "slop-bot", Version: c.opts.version},
		// thread/backgroundTerminals/clean, which ends the commands an
		// interrupt leaves running, is experimental.
		Capabilities: &protocol.InitializeCapabilities{ExperimentalAPI: true},
	})
	if err != nil {
		return "", fmt.Errorf("initialize: %w", err)
	}
	if err := write(w, &message{Method: protocol.MethodInitialized}); err != nil {
		return "", err
	}
	method, params := protocol.MethodThreadStart, any(c.opts.start)
	if c.opts.resume != "" {
		method, params = protocol.MethodThreadResume, c.opts.resumeParams
	}
	result, err := c.call(ctx, w, next, reqThread, method, params)
	var rerr *rpcError
	if errors.As(err, &rerr) && c.opts.resume != "" && strings.Contains(rerr.Message, noRollout) {
		return "", fmt.Errorf("%w %s", ErrNoRollout, c.opts.resume)
	}
	if err != nil {
		return "", fmt.Errorf("%s: %w", method, err)
	}
	var thread struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if err := json.Unmarshal(result, &thread); err != nil {
		return "", fmt.Errorf("%s: %w", method, err)
	}
	if _, err := uuid.Parse(thread.Thread.ID); err != nil {
		return "", fmt.Errorf("%s answered without a usable thread id %q", method, thread.Thread.ID)
	}
	c.mu.Lock()
	c.threadID = thread.Thread.ID
	c.mu.Unlock()
	return thread.Thread.ID, nil
}

// call sends a request during the handshake and reads lines until its
// response. Anything else arriving meanwhile is handled as it would be
// later; the events it makes are dropped, as there is no turn yet.
func (c *codec) call(ctx context.Context, w *rpcdriver.Wire, next func(context.Context) ([]byte, error), kind requestKind, method string, params any) (json.RawMessage, error) {
	c.mu.Lock()
	id, err := c.request(w, &request{kind: kind}, method, params)
	c.mu.Unlock()
	if err != nil {
		return nil, err
	}
	for {
		line, err := next(ctx)
		if err != nil {
			return nil, err
		}
		var m message
		if err := json.Unmarshal(line, &m); err != nil {
			return nil, fmt.Errorf("undecodable line: %w", err)
		}
		if m.Method == "" && responseID(m.ID) == id {
			c.mu.Lock()
			delete(c.pending, id)
			c.mu.Unlock()
			if m.Error != nil {
				return nil, m.Error
			}
			return m.Result, nil
		}
		if _, err := c.Decode(line, w); err != nil {
			return nil, err
		}
	}
}

// request writes a request and records what its response is for. mu is
// held.
func (c *codec) request(w *rpcdriver.Wire, r *request, method string, params any) (int64, error) {
	c.nextID++
	id := c.nextID
	b, err := json.Marshal(params)
	if err != nil {
		return 0, fmt.Errorf("codex: encoding %s: %w", method, err)
	}
	c.pending[id] = r
	if err := write(w, &message{ID: json.RawMessage(fmt.Sprint(id)), Method: method, Params: b}); err != nil {
		delete(c.pending, id)
		return 0, err
	}
	return id, nil
}

func responseID(raw json.RawMessage) int64 {
	var id int64
	if json.Unmarshal(raw, &id) != nil {
		return 0
	}
	return id
}

func textInput(text string) []protocol.UserInput {
	return []protocol.UserInput{{Type: protocol.UserInputTypeText, Text: text}}
}

// Send starts a turn when idle and steers the running one otherwise
// (turn/steer, which lands in the same turn: protocol spike). rpcdriver's
// midTurn is not used: only the codec knows the turn's id, and whether a
// turn/start is still in flight.
func (c *codec) Send(_ context.Context, w *rpcdriver.Wire, msg *harness.Message, _ bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.deliver(w, msg.Text)
}

// deliver routes one message. mu is held.
func (c *codec) deliver(w *rpcdriver.Wire, text string) error {
	switch {
	case c.starting, c.interrupted && c.turnID != "":
		// No turn to steer into yet, or one that is being interrupted: a
		// steer there would be lost with it. turnCompleted starts the next
		// turn with what waits.
		c.queued = append(c.queued, text)
		return nil
	case c.turnID != "":
		_, err := c.request(w, &request{kind: reqSteer, text: text, turn: c.turnID}, protocol.MethodTurnSteer, &protocol.TurnSteerParams{
			ThreadID: c.threadID, ExpectedTurnID: c.turnID, Input: textInput(text),
		})
		return err
	}
	return c.startTurn(w, text)
}

func (c *codec) startTurn(w *rpcdriver.Wire, text string) error {
	c.starting = true
	_, err := c.request(w, &request{kind: reqTurn}, protocol.MethodTurnStart, &protocol.TurnStartParams{
		ThreadID: c.threadID, Input: textInput(text), Effort: protocol.ReasoningEffort(c.opts.effort),
	})
	if err != nil {
		c.starting = false
	}
	return err
}

// Interrupt asks Codex to end the running turn. With a turn/start in flight
// it waits for the turn's id; idle, there is nothing to interrupt.
func (c *codec) Interrupt(_ context.Context, w *rpcdriver.Wire) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch {
	case c.turnID != "":
		return c.interrupt(w)
	case c.starting:
		c.interruptAsked = true
	}
	return nil
}

func (c *codec) interrupt(w *rpcdriver.Wire) error {
	c.interruptAsked = false
	c.interrupted = true
	_, err := c.request(w, &request{kind: reqInterrupt}, protocol.MethodTurnInterrupt, &protocol.TurnInterruptParams{
		ThreadID: c.threadID, TurnID: c.turnID,
	})
	return err
}

// Decode handles one line from Codex.
func (c *codec) Decode(line []byte, w *rpcdriver.Wire) ([]harness.Event, error) {
	var m message
	if err := json.Unmarshal(line, &m); err != nil {
		return nil, err
	}
	// Before mu: the guard may run git and read state.db, and a Send
	// needn't wait for that.
	screened := c.screen(&m)
	c.mu.Lock()
	defer c.mu.Unlock()
	evs, err := c.handle(w, &m, screened)
	if err != nil && len(evs) > 0 {
		// rpcdriver drops the events of a line that errs, and a lost
		// TurnEnded would leave the conversation in a turn for ever. The
		// error is a failed write (the request a turn's end sends), which
		// the process's exit will report.
		c.opts.logger.Warn("codex: a follow-up request failed", "method", m.Method, "err", err)
		err = nil
	}
	return evs, err
}

func (c *codec) handle(w *rpcdriver.Wire, m *message, s *screened) ([]harness.Event, error) {
	switch {
	case m.Method != "" && m.ID != nil:
		return nil, c.answer(w, m, s)
	case m.Method != "":
		return c.notification(w, m)
	case m.ID != nil:
		return c.response(w, m)
	}
	return nil, nil
}

func (c *codec) response(w *rpcdriver.Wire, m *message) ([]harness.Event, error) {
	id := responseID(m.ID)
	r, ok := c.pending[id]
	if !ok {
		return nil, nil
	}
	delete(c.pending, id)
	switch r.kind {
	case reqTurn:
		return c.turnStarted(w, m)
	case reqSteer:
		if m.Error != nil {
			return nil, c.steerRefused(w, r, m.Error)
		}
	case reqInterrupt:
		if m.Error != nil {
			// "no active turn to interrupt": the turn ended first.
			c.opts.logger.Info("codex refused an interrupt", "err", m.Error.Message)
		}
	case reqClean:
		if m.Error != nil {
			c.opts.logger.Warn("codex could not end an interrupted turn's commands", "err", m.Error.Message)
		}
	}
	return nil, nil
}

// steerRefused redelivers a steer whose turn ended between our check and
// Codex's ("no active turn to steer"). If that turn's turn/completed hasn't
// arrived yet, the message waits for it and then starts a turn; steering it
// again would only be refused again.
func (c *codec) steerRefused(w *rpcdriver.Wire, r *request, e *rpcError) error {
	c.opts.logger.Info("codex refused a steer; delivering it as a turn", "err", e.Message)
	if c.turnID == r.turn {
		c.queued = append(c.queued, r.text)
		return nil
	}
	return c.deliver(w, r.text)
}

// turnStarted handles turn/start's response: the turn's id, which the
// messages and interrupt that waited for it can now use.
func (c *codec) turnStarted(w *rpcdriver.Wire, m *message) ([]harness.Event, error) {
	c.starting = false
	if m.Error != nil {
		// No turn, so nothing to interrupt: the interrupt mustn't carry
		// over to the next turn.
		c.interruptAsked = false
		// No turn ran, so the message that asked for it is reported as a
		// failed turn rather than lost.
		evs := []harness.Event{
			{Kind: harness.TurnStarted},
			{Kind: harness.TurnEnded, Outcome: harness.TurnFailed, Text: m.Error.Message},
		}
		return evs, c.flush(w)
	}
	var res protocol.TurnStartResponse
	if err := json.Unmarshal(m.Result, &res); err != nil {
		return nil, err
	}
	c.turnID = res.Turn.ID
	return nil, c.flush(w)
}

// flush delivers what waited for a turn/start to answer.
func (c *codec) flush(w *rpcdriver.Wire) error {
	if c.interruptAsked && c.turnID != "" {
		if err := c.interrupt(w); err != nil {
			return err
		}
	}
	queued := c.queued
	c.queued = nil
	for i, text := range queued {
		if err := c.deliver(w, text); err != nil {
			return err
		}
		if c.starting {
			// The first became a turn/start; the rest wait for it.
			c.queued = append(c.queued, queued[i+1:]...)
			return nil
		}
	}
	return nil
}

func (c *codec) notification(w *rpcdriver.Wire, m *message) ([]harness.Event, error) {
	var scope struct {
		ThreadID string `json:"threadId"`
	}
	_ = json.Unmarshal(m.Params, &scope)
	// A thread of Codex's own (a title, a sub-agent) is not this
	// conversation.
	if scope.ThreadID != "" && c.threadID != "" && scope.ThreadID != c.threadID {
		return nil, nil
	}
	switch m.Method {
	case protocol.MethodTurnStarted:
		var n protocol.TurnStartedNotification
		if err := json.Unmarshal(m.Params, &n); err != nil {
			return nil, err
		}
		c.turnID = n.Turn.ID
		c.lastError = ""
		return []harness.Event{{Kind: harness.TurnStarted}}, nil
	case protocol.MethodTurnCompleted:
		return c.turnCompleted(w, m.Params)
	case protocol.MethodItemStarted:
		return c.itemStarted(m.Params)
	case protocol.MethodItemCompleted:
		return c.itemCompleted(m.Params)
	case protocol.MethodThreadTokenUsageUpdated:
		return nil, c.tokenUsage(m.Params)
	case protocol.MethodAccountRateLimitsUpdated:
		return c.rateLimits(m.Params)
	case protocol.MethodError:
		var n protocol.ErrorNotification
		if err := json.Unmarshal(m.Params, &n); err != nil {
			return nil, err
		}
		if !n.WillRetry {
			c.lastError = n.Error.Message
		}
	}
	return nil, nil
}

func (c *codec) turnCompleted(w *rpcdriver.Wire, params json.RawMessage) ([]harness.Event, error) {
	var n protocol.TurnCompletedNotification
	if err := json.Unmarshal(params, &n); err != nil {
		return nil, err
	}
	if n.Turn.ID == c.turnID {
		c.turnID = ""
	}
	var evs []harness.Event
	// An interrupted command never completes inside the turn: close what is
	// open so every ToolStarted has its ToolEnded.
	for id, tool := range c.tools {
		evs = append(evs, harness.Event{Kind: harness.ToolEnded, Tool: tool})
		delete(c.tools, id)
	}
	if c.usage != nil {
		evs = append(evs, harness.Event{Kind: harness.UsageReport, Usage: c.usage})
		c.usage = nil
	}
	next := len(c.queued) > 0 && !c.starting
	// A queued message starts the next turn at once. Ending this one first
	// would tell the driver the session settled, and a graceful Stop would
	// close stdin on a message Send already accepted; the next turn's end
	// is the one reported.
	if !next {
		evs = append(evs, c.turnEnded(&n.Turn))
	}
	if c.interrupted {
		// An interrupted command keeps running until app-server exits
		// (protocol spike); this ends it. command/exec/terminate doesn't:
		// it only reaches commands a client started with command/exec.
		c.interrupted = false
		if _, err := c.request(w, &request{kind: reqClean}, protocol.MethodThreadBackgroundTerminalsClean,
			&protocol.ThreadBackgroundTerminalsCleanParams{ThreadID: c.threadID}); err != nil {
			return evs, err
		}
	}
	if next {
		text := c.queued[0]
		c.queued = c.queued[1:]
		return evs, c.startTurn(w, text)
	}
	return evs, nil
}

func (c *codec) turnEnded(t *protocol.Turn) harness.Event {
	switch t.Status {
	case protocol.TurnStatusCompleted:
		return harness.Event{Kind: harness.TurnEnded, Outcome: harness.TurnCompleted}
	case protocol.TurnStatusInterrupted:
		return harness.Event{Kind: harness.TurnEnded, Outcome: harness.TurnInterrupted}
	}
	text := c.lastError
	if t.Error != nil && t.Error.Message != "" {
		text = t.Error.Message
	}
	if text == "" {
		text = "the turn ended " + string(t.Status)
	}
	return harness.Event{Kind: harness.TurnEnded, Outcome: harness.TurnFailed, Text: text}
}

type itemNotification struct {
	Item protocol.ThreadItem `json:"item"`
}

func (c *codec) itemStarted(params json.RawMessage) ([]harness.Event, error) {
	var n itemNotification
	if err := json.Unmarshal(params, &n); err != nil {
		return nil, err
	}
	tool := toolName(&n.Item)
	if tool == "" {
		return nil, nil
	}
	c.tools[n.Item.ID] = tool
	return []harness.Event{{Kind: harness.ToolStarted, Tool: tool}}, nil
}

func (c *codec) itemCompleted(params json.RawMessage) ([]harness.Event, error) {
	var n itemNotification
	if err := json.Unmarshal(params, &n); err != nil {
		return nil, err
	}
	if n.Item.Type == protocol.ThreadItemTypeAgentMessage {
		return []harness.Event{{Kind: harness.Text, Text: n.Item.Text}}, nil
	}
	// A command that outlived its interrupted turn completes after it; its
	// ToolEnded was sent at the turn's end.
	tool, open := c.tools[n.Item.ID]
	if !open {
		return nil, nil
	}
	delete(c.tools, n.Item.ID)
	return []harness.Event{{Kind: harness.ToolEnded, Tool: tool}}, nil
}

// toolName is the tool an item is, or "" for an item that isn't a tool call
// (messages, reasoning, plans).
func toolName(it *protocol.ThreadItem) string {
	switch it.Type {
	case protocol.ThreadItemTypeMcpToolCall:
		return it.Server + "/" + it.Tool
	case protocol.ThreadItemTypeDynamicToolCall, protocol.ThreadItemTypeCollabAgentToolCall:
		return it.Tool
	case protocol.ThreadItemTypeCommandExecution, protocol.ThreadItemTypeFileChange,
		protocol.ThreadItemTypeWebSearch, protocol.ThreadItemTypeImageView,
		protocol.ThreadItemTypeImageGeneration:
		return it.Type
	}
	return ""
}

// tokenUsage adds one model call's usage to the running turn's. Each
// update's `last` is one call, and their sum over a turn is the turn's
// (recorded: turn_steered.jsonl). A resumed thread replays the previous
// turn's update first; its turn id isn't the running one, so it is not
// counted twice.
func (c *codec) tokenUsage(params json.RawMessage) error {
	var n protocol.ThreadTokenUsageUpdatedNotification
	if err := json.Unmarshal(params, &n); err != nil {
		return err
	}
	if c.turnID == "" || n.TurnID != c.turnID {
		return nil
	}
	if c.usage == nil {
		c.usage = &harness.Usage{}
	}
	last := &n.TokenUsage.Last
	// OpenAI's input count includes the cached and cache-written parts;
	// harness.Usage, like Claude's, counts them apart (as the supervisor's
	// usage/codex.py does).
	c.usage.InputTokens += max(last.InputTokens-last.CachedInputTokens-last.CacheWriteInputTokens, 0)
	c.usage.CacheReadTokens += last.CachedInputTokens
	c.usage.CacheWriteTokens += last.CacheWriteInputTokens
	c.usage.OutputTokens += last.OutputTokens
	return nil
}

// rateLimits reports each window an update carries, named by its limit id
// and slot (the 5-hour primary, the weekly secondary). Updates are sparse:
// a window left out is unchanged, and the Manager keeps the last report of
// each.
func (c *codec) rateLimits(params json.RawMessage) ([]harness.Event, error) {
	var n protocol.AccountRateLimitsUpdatedNotification
	if err := json.Unmarshal(params, &n); err != nil {
		return nil, err
	}
	var evs []harness.Event
	for _, w := range []struct {
		slot string
		win  *protocol.RateLimitWindow
	}{{"primary", n.RateLimits.Primary}, {"secondary", n.RateLimits.Secondary}} {
		if l := limitOf(n.RateLimits.LimitID+"/"+w.slot, w.win); l != nil {
			evs = append(evs, harness.Event{Kind: harness.LimitReport, Limit: l})
		}
	}
	return evs, nil
}

func limitOf(window string, win *protocol.RateLimitWindow) *harness.Limit {
	if win == nil {
		return nil
	}
	l := &harness.Limit{Window: window, UsedPercent: float64(win.UsedPercent)}
	if win.ResetsAt != 0 {
		l.ResetsAt = time.Unix(win.ResetsAt, 0).UTC()
	}
	return l
}

func write(w *rpcdriver.Wire, m *message) error {
	b, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("codex: encoding: %w", err)
	}
	return w.WriteLine(b)
}
