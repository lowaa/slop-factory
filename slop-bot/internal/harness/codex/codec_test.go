package codex

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-bot/internal/harness"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/codex/protocol"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/rpcdriver"
)

// thread is the scrubbed thread id of every fixture.
const thread = "00000000-0000-7000-8000-000000000001"

// peer stands in for app-server's stdin: it records what the codec writes.
type peer struct {
	t    *testing.T
	sent []message
	// ids are the id of the last request of each method.
	ids map[string]json.RawMessage
	// broken fails every write, as a pipe to an exited process does.
	broken bool
}

func (p *peer) Write(b []byte) (int, error) {
	if p.broken {
		return 0, errors.New("broken pipe")
	}
	for _, line := range bytes.Split(bytes.TrimRight(b, "\n"), []byte("\n")) {
		var m message
		require.NoError(p.t, json.Unmarshal(line, &m))
		p.sent = append(p.sent, m)
		if m.Method != "" && m.ID != nil {
			p.ids[m.Method] = m.ID
		}
	}
	return len(b), nil
}

// last is the last message written with method, decoded into params.
func (p *peer) last(method string, params any) {
	p.t.Helper()
	for i := len(p.sent) - 1; i >= 0; i-- {
		if p.sent[i].Method == method {
			if params != nil {
				require.NoError(p.t, json.Unmarshal(p.sent[i].Params, params))
			}
			return
		}
	}
	require.FailNow(p.t, "the codec never sent "+method)
}

func (p *peer) methods() []string {
	var out []string
	for _, m := range p.sent {
		out = append(out, m.Method)
	}
	return out
}

// player replays recorded app-server output (testdata, see its README).
// A response's id is recorded as "<method>"; the player swaps in the id of
// the codec's own last request of that method, so the codec's numbering is
// its own business.
type player struct {
	t     *testing.T
	p     *peer
	lines [][]byte
}

func newPlayer(t *testing.T, p *peer, files ...string) *player {
	t.Helper()
	pl := &player{t: t, p: p}
	for _, f := range files {
		pl.lines = append(pl.lines, readLines(t, "testdata/"+f+".jsonl")...)
	}
	return pl
}

func readLines(t *testing.T, path string) [][]byte {
	t.Helper()
	f, err := os.Open(path) // #nosec G304 -- the package's own fixtures
	require.NoError(t, err)
	defer func() { _ = f.Close() }()
	var out [][]byte
	sc := bufio.NewScanner(f)
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		out = append(out, append([]byte(nil), sc.Bytes()...))
	}
	require.NoError(t, sc.Err())
	return out
}

func (pl *player) take() []byte {
	pl.t.Helper()
	require.NotEmpty(pl.t, pl.lines, "the recording ran out")
	line := pl.lines[0]
	pl.lines = pl.lines[1:]
	var m struct {
		ID json.RawMessage `json:"id"`
	}
	require.NoError(pl.t, json.Unmarshal(line, &m))
	var marker string
	if json.Unmarshal(m.ID, &marker) == nil && strings.HasPrefix(marker, "<") {
		method := strings.Trim(marker, "<>")
		id, ok := pl.p.ids[method]
		require.True(pl.t, ok, "the recording answers %s, which the codec never sent", method)
		line = bytes.Replace(line, []byte(`"id":"`+marker+`"`), []byte(`"id":`+string(id)), 1)
	}
	return line
}

// next serves Open's handshake.
func (pl *player) next(context.Context) ([]byte, error) { return pl.take(), nil }

// decode feeds lines to the codec up to and including the first that
// matches until (all of them, with nil), and returns the events.
func (pl *player) decode(c *codec, w *rpcdriver.Wire, until func(line []byte) bool) []harness.Event {
	pl.t.Helper()
	var evs []harness.Event
	for len(pl.lines) > 0 {
		line := pl.take()
		got, err := c.Decode(line, w)
		require.NoError(pl.t, err)
		evs = append(evs, got...)
		if until != nil && until(line) {
			return evs
		}
	}
	require.Nil(pl.t, until, "the recording ran out before the line waited for")
	return evs
}

func itemStarted(itemType string) func([]byte) bool {
	return func(line []byte) bool {
		var m struct {
			Method string                           `json:"method"`
			Params protocol.ItemStartedNotification `json:"params"`
		}
		return json.Unmarshal(line, &m) == nil && m.Method == protocol.MethodItemStarted && m.Params.Item.Type == itemType
	}
}

func method(name string) func([]byte) bool {
	return func(line []byte) bool {
		var m struct {
			Method string `json:"method"`
		}
		return json.Unmarshal(line, &m) == nil && m.Method == name
	}
}

func kinds(evs []harness.Event) []harness.EventKind {
	out := make([]harness.EventKind, len(evs))
	for i, e := range evs {
		out[i] = e.Kind
	}
	return out
}

func of(evs []harness.Event, kind harness.EventKind) []harness.Event {
	var out []harness.Event
	for _, e := range evs {
		if e.Kind == kind {
			out = append(out, e)
		}
	}
	return out
}

type fixture struct {
	t *testing.T
	c *codec
	p *peer
	w *rpcdriver.Wire
}

func newFixture(t *testing.T, opts *codecOptions) *fixture {
	t.Helper()
	if opts == nil {
		ws := &harness.Workspace{Dir: "/ws", Permissive: true, Briefing: "brief", Effort: "low"}
		opts = &codecOptions{effort: ws.Effort}
		opts.start, opts.resumeParams = threadParams(ws)
	}
	opts.logger = slog.New(slog.DiscardHandler)
	p := &peer{t: t, ids: map[string]json.RawMessage{}}
	return &fixture{t: t, c: newCodec(opts), p: p, w: rpcdriver.NewWire(p)}
}

// open runs the handshake against the recorded start.
func (f *fixture) open() {
	f.t.Helper()
	id, err := f.c.Open(context.Background(), f.w, newPlayer(f.t, f.p, "start").next)
	require.NoError(f.t, err)
	require.Equal(f.t, thread, id)
}

func (f *fixture) send(text string) {
	f.t.Helper()
	require.NoError(f.t, f.c.Send(context.Background(), f.w, &harness.Message{Text: text}, false))
}

func TestOpenStartsAPermissiveThreadWithTheBriefing(t *testing.T) {
	t.Parallel()
	f := newFixture(t, nil)
	f.open()
	assert.Equal(t, []string{protocol.MethodInitialize, protocol.MethodInitialized, protocol.MethodThreadStart}, f.p.methods())

	var init protocol.InitializeParams
	f.p.last(protocol.MethodInitialize, &init)
	assert.Equal(t, "slop-bot", init.ClientInfo.Name)
	require.NotNil(t, init.Capabilities)
	assert.True(t, init.Capabilities.ExperimentalAPI, "backgroundTerminals/clean is experimental")

	var start protocol.ThreadStartParams
	f.p.last(protocol.MethodThreadStart, &start)
	assert.Equal(t, "/ws", start.Cwd)
	assert.Equal(t, protocol.AskForApprovalNever, start.ApprovalPolicy)
	assert.Equal(t, protocol.SandboxModeDangerFullAccess, start.Sandbox)
	assert.Equal(t, "brief", start.DeveloperInstructions)
}

func TestARecordedTurn(t *testing.T) {
	t.Parallel()
	f := newFixture(t, nil)
	f.open()
	f.send("Reply PONG")

	var turn protocol.TurnStartParams
	f.p.last(protocol.MethodTurnStart, &turn)
	assert.Equal(t, thread, turn.ThreadID)
	assert.Equal(t, "Reply PONG", turn.Input[0].Text)
	assert.Equal(t, protocol.ReasoningEffort("low"), turn.Effort)

	evs := newPlayer(t, f.p, "turn_completed").decode(f.c, f.w, nil)
	assert.Equal(t, []harness.EventKind{
		harness.TurnStarted, harness.Text, harness.LimitReport, harness.UsageReport, harness.TurnEnded,
	}, kinds(evs))
	assert.Equal(t, "PONG, ZEBRA-42", evs[1].Text)
	assert.InDelta(t, 4.0, evs[2].Limit.UsedPercent, 0.001, "the 5-hour window, the tighter of the two")
	assert.Equal(t, time.Unix(1795844239, 0).UTC(), evs[2].Limit.ResetsAt)
	assert.Equal(t, &harness.Usage{InputTokens: 16110 - 13952, CacheReadTokens: 13952, OutputTokens: 12}, evs[3].Usage)
	assert.Equal(t, harness.TurnCompleted, evs[4].Outcome)
}

func TestAMidTurnMessageSteersTheRunningTurn(t *testing.T) {
	t.Parallel()
	f := newFixture(t, nil)
	f.open()
	f.send("Run the command")
	pl := newPlayer(t, f.p, "turn_steered")
	evs := pl.decode(f.c, f.w, itemStarted(protocol.ThreadItemTypeCommandExecution))
	assert.Equal(t, harness.ToolStarted, evs[len(evs)-1].Kind)
	assert.Equal(t, "commandExecution", evs[len(evs)-1].Tool)

	f.send("Also say STEERED")
	var turn protocol.TurnStartResponse
	require.NoError(t, json.Unmarshal(readLines(t, "testdata/turn_steered.jsonl")[0], &struct {
		Result *protocol.TurnStartResponse `json:"result"`
	}{&turn}))
	var steer protocol.TurnSteerParams
	f.p.last(protocol.MethodTurnSteer, &steer)
	assert.Equal(t, turn.Turn.ID, steer.ExpectedTurnID, "steered into the turn turn/start named")
	assert.Equal(t, "Also say STEERED", steer.Input[0].Text)

	evs = append(evs, pl.decode(f.c, f.w, nil)...)
	assert.Len(t, of(evs, harness.TurnStarted), 1, "the steer lands in the same turn")
	assert.Len(t, of(evs, harness.ToolEnded), 1)
	texts := of(evs, harness.Text)
	assert.Equal(t, "DONE STEERED", texts[len(texts)-1].Text)
	usage := of(evs, harness.UsageReport)
	require.Len(t, usage, 1)
	// The sum of the turn's four model calls.
	assert.Equal(t, &harness.Usage{
		InputTokens:     (17572 - 16000) + (17672 - 17536) + (17747 - 17536) + (17838 - 17536),
		CacheReadTokens: 16000 + 3*17536,
		OutputTokens:    54 + 18 + 33 + 8,
	}, usage[0].Usage)
	assert.Equal(t, harness.TurnCompleted, evs[len(evs)-1].Outcome)
}

func TestARefusedSteerBecomesATurn(t *testing.T) {
	t.Parallel()
	// Codex says "no active turn to steer" when the turn ended between the
	// codec's check and Codex's. Its turn/completed may arrive before the
	// refusal or after it; either way the message starts the next turn,
	// once.
	for name, completedFirst := range map[string]bool{"completed first": true, "refused first": false} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t, nil)
			f.open()
			f.send("first")
			pl := newPlayer(t, f.p, "turn_completed")
			pl.decode(f.c, f.w, method(protocol.MethodTurnStarted))
			f.send("second")
			f.p.last(protocol.MethodTurnSteer, nil)
			refusal := newPlayer(t, f.p, "steer_refused")
			if completedFirst {
				pl.decode(f.c, f.w, nil)
				refusal.decode(f.c, f.w, nil)
			} else {
				refusal.decode(f.c, f.w, nil)
				assert.Equal(t, protocol.MethodTurnSteer, f.p.methods()[len(f.p.sent)-1], "nothing more until the turn ends")
				pl.decode(f.c, f.w, nil)
			}
			starts := 0
			for _, m := range f.p.methods() {
				if m == protocol.MethodTurnStart {
					starts++
				}
			}
			assert.Equal(t, 2, starts)
			var turn protocol.TurnStartParams
			f.p.last(protocol.MethodTurnStart, &turn)
			assert.Equal(t, "second", turn.Input[0].Text)
		})
	}
}

func TestMessagesWhileATurnStartsAreSteeredIntoIt(t *testing.T) {
	t.Parallel()
	f := newFixture(t, nil)
	f.open()
	f.send("first")
	f.send("second")
	assert.Equal(t, protocol.MethodTurnStart, f.p.methods()[len(f.p.sent)-1], "no turn id to steer by yet")
	newPlayer(t, f.p, "turn_completed").decode(f.c, f.w, func([]byte) bool { return true })
	var steer protocol.TurnSteerParams
	f.p.last(protocol.MethodTurnSteer, &steer)
	assert.Equal(t, "second", steer.Input[0].Text)
	assert.NotEmpty(t, steer.ExpectedTurnID)
}

func TestInterruptEndsTheTurnAndItsCommands(t *testing.T) {
	t.Parallel()
	f := newFixture(t, nil)
	f.open()
	f.send("sleep")
	pl := newPlayer(t, f.p, "turn_interrupted")
	evs := pl.decode(f.c, f.w, itemStarted(protocol.ThreadItemTypeCommandExecution))
	assert.Equal(t, harness.ToolStarted, evs[len(evs)-1].Kind)

	require.NoError(t, f.c.Interrupt(context.Background(), f.w))
	var in protocol.TurnInterruptParams
	f.p.last(protocol.MethodTurnInterrupt, &in)
	assert.Equal(t, thread, in.ThreadID)
	assert.NotEmpty(t, in.TurnID)

	evs = pl.decode(f.c, f.w, nil)
	// The command never completes inside the turn, so the turn's end closes
	// it; its late item/completed adds nothing.
	assert.Equal(t, []harness.EventKind{
		harness.LimitReport, harness.ToolEnded, harness.UsageReport, harness.TurnEnded,
	}, kinds(evs))
	assert.Equal(t, harness.TurnInterrupted, evs[3].Outcome)
	var clean protocol.ThreadBackgroundTerminalsCleanParams
	f.p.last(protocol.MethodThreadBackgroundTerminalsClean, &clean)
	assert.Equal(t, thread, clean.ThreadID)
}

func TestAFailedWriteAtATurnsEndKeepsItsEvents(t *testing.T) {
	t.Parallel()
	f := newFixture(t, nil)
	f.open()
	f.send("sleep")
	pl := newPlayer(t, f.p, "turn_interrupted")
	pl.decode(f.c, f.w, itemStarted(protocol.ThreadItemTypeCommandExecution))
	require.NoError(t, f.c.Interrupt(context.Background(), f.w))
	for !method(protocol.MethodTurnCompleted)(pl.lines[0]) {
		_, err := f.c.Decode(pl.take(), f.w)
		require.NoError(t, err)
	}
	// The clean request turn/completed sends fails; the turn still ends.
	f.p.broken = true
	got, err := f.c.Decode(pl.take(), f.w)
	require.NoError(t, err)
	assert.Equal(t, harness.TurnEnded, got[len(got)-1].Kind)
	assert.Equal(t, harness.TurnInterrupted, got[len(got)-1].Outcome)
}

func TestInterruptBeforeTheTurnIsNamedWaitsForIt(t *testing.T) {
	t.Parallel()
	f := newFixture(t, nil)
	f.open()
	f.send("sleep")
	require.NoError(t, f.c.Interrupt(context.Background(), f.w))
	assert.NotContains(t, f.p.methods(), protocol.MethodTurnInterrupt)
	newPlayer(t, f.p, "turn_interrupted").decode(f.c, f.w, func([]byte) bool { return true })
	f.p.last(protocol.MethodTurnInterrupt, nil)
}

func TestInterruptWhenIdleIsANoOp(t *testing.T) {
	t.Parallel()
	f := newFixture(t, nil)
	f.open()
	before := len(f.p.sent)
	require.NoError(t, f.c.Interrupt(context.Background(), f.w))
	assert.Len(t, f.p.sent, before)
}

func TestALostInterruptRaceIsHarmless(t *testing.T) {
	t.Parallel()
	f := newFixture(t, nil)
	f.open()
	f.send("first")
	pl := newPlayer(t, f.p, "turn_completed")
	pl.decode(f.c, f.w, method(protocol.MethodTurnStarted))
	require.NoError(t, f.c.Interrupt(context.Background(), f.w))
	evs := pl.decode(f.c, f.w, nil)
	evs = append(evs, newPlayer(t, f.p, "interrupt_refused").decode(f.c, f.w, nil)...)
	assert.Equal(t, harness.TurnCompleted, evs[len(evs)-1].Outcome)
}

func resumeOptions() *codecOptions {
	ws := &harness.Workspace{Dir: "/ws", Permissive: true}
	opts := &codecOptions{resume: thread}
	opts.start, opts.resumeParams = threadParams(ws)
	opts.resumeParams.ThreadID = thread
	return opts
}

func TestResumeKeepsTheThread(t *testing.T) {
	t.Parallel()
	f := newFixture(t, resumeOptions())
	pl := newPlayer(t, f.p, "resume")
	id, err := f.c.Open(context.Background(), f.w, pl.next)
	require.NoError(t, err)
	assert.Equal(t, thread, id)
	var resume protocol.ThreadResumeParams
	f.p.last(protocol.MethodThreadResume, &resume)
	assert.Equal(t, thread, resume.ThreadID)
	assert.Equal(t, protocol.SandboxModeDangerFullAccess, resume.Sandbox)

	// Codex replays the previous turn's usage after a resume; it must not
	// be counted into the next turn's.
	assert.Empty(t, pl.decode(f.c, f.w, nil))
	f.send("again")
	evs := newPlayer(t, f.p, "turn_completed").decode(f.c, f.w, nil)
	assert.Equal(t, int64(12), of(evs, harness.UsageReport)[0].Usage.OutputTokens)
}

func TestResumeOfAMissingThread(t *testing.T) {
	t.Parallel()
	f := newFixture(t, resumeOptions())
	pl := newPlayer(t, f.p, "start")
	pl.lines = append(pl.lines[:1], readLines(t, "testdata/resume_missing.jsonl")...)
	_, err := f.c.Open(context.Background(), f.w, pl.next)
	assert.ErrorIs(t, err, ErrNoRollout)
}

// The cases below aren't recorded: permissive Codex sends no approvals, and
// a failed turn needs a failing model. They are written from the schema.

func TestAFailedTurnSaysWhy(t *testing.T) {
	t.Parallel()
	f := newFixture(t, nil)
	f.open()
	f.send("first")
	pl := newPlayer(t, f.p, "turn_completed")
	pl.decode(f.c, f.w, method(protocol.MethodTurnStarted))
	turnID := f.c.turnID
	decode := func(line string) []harness.Event {
		evs, err := f.c.Decode([]byte(line), f.w)
		require.NoError(t, err)
		return evs
	}
	assert.Empty(t, decode(`{"method":"error","params":{"threadId":"`+thread+`","turnId":"`+turnID+`","willRetry":false,"error":{"message":"usage limit reached"}}}`))
	evs := decode(`{"method":"turn/completed","params":{"threadId":"` + thread + `","turn":{"id":"` + turnID + `","items":[],"status":"failed","error":null}}}`)
	assert.Equal(t, harness.TurnFailed, evs[len(evs)-1].Outcome)
	assert.Equal(t, "usage limit reached", evs[len(evs)-1].Text, "the error notification, when the turn names none")
}

func TestARefusedTurnStartIsAFailedTurn(t *testing.T) {
	t.Parallel()
	f := newFixture(t, nil)
	f.open()
	f.send("first")
	id := string(f.p.ids[protocol.MethodTurnStart])
	evs, err := f.c.Decode([]byte(`{"id":`+id+`,"error":{"code":-32600,"message":"bad input"}}`), f.w)
	require.NoError(t, err)
	assert.Equal(t, []harness.EventKind{harness.TurnStarted, harness.TurnEnded}, kinds(evs))
	assert.Equal(t, "bad input", evs[1].Text)
	f.send("second")
	assert.Equal(t, protocol.MethodTurnStart, f.p.methods()[len(f.p.sent)-1], "idle again")
}

func TestOtherThreadsAreIgnored(t *testing.T) {
	t.Parallel()
	f := newFixture(t, nil)
	f.open()
	evs, err := f.c.Decode([]byte(`{"method":"turn/started","params":{"threadId":"00000000-0000-7000-8000-0000000000ff","turn":{"id":"t","items":[],"status":"inProgress"}}}`), f.w)
	require.NoError(t, err)
	assert.Empty(t, evs, "Codex's own threads (titles, sub-agents) aren't this conversation")
}

func TestServerRequestsAreApproved(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		params, result string
	}{
		protocol.MethodItemCommandExecutionRequestApproval: {`{}`, `{"decision":"accept"}`},
		protocol.MethodItemFileChangeRequestApproval:       {`{}`, `{"decision":"accept"}`},
		protocol.MethodApplyPatchApproval:                  {`{}`, `{"decision":"approved"}`},
		protocol.MethodExecCommandApproval:                 {`{}`, `{"decision":"approved"}`},
		protocol.MethodItemPermissionsRequestApproval: {
			`{"cwd":"/ws","itemId":"i","threadId":"t","turnId":"u","startedAtMs":1,"permissions":{"network":{"enabled":true}}}`,
			`{"permissions":{"network":{"enabled":true}},"scope":"turn"}`,
		},
		protocol.MethodMcpServerElicitationRequest: {`{}`, `{"action":"accept","content":{}}`},
		protocol.MethodItemToolRequestUserInput:    {`{}`, `{"answers":{}}`},
	}
	for m, tc := range cases {
		t.Run(m, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t, nil)
			evs, err := f.c.Decode([]byte(`{"id":"req-7","method":"`+m+`","params":`+tc.params+`}`), f.w)
			require.NoError(t, err)
			assert.Empty(t, evs)
			got := f.p.sent[len(f.p.sent)-1]
			assert.JSONEq(t, `"req-7"`, string(got.ID), "answered by the request's own id")
			assert.Nil(t, got.Error)
			assert.JSONEq(t, tc.result, string(got.Result))
		})
	}
}

func TestOtherServerRequestsAreRefusedNotLeftWaiting(t *testing.T) {
	t.Parallel()
	f := newFixture(t, nil)
	_, err := f.c.Decode([]byte(`{"id":3,"method":"item/tool/call","params":{}}`), f.w)
	require.NoError(t, err)
	got := f.p.sent[len(f.p.sent)-1]
	assert.JSONEq(t, `3`, string(got.ID))
	require.NotNil(t, got.Error)
	assert.Equal(t, methodNotFound, got.Error.Code)
}

func TestAThreadWithoutAUsableIDFailsTheHandshake(t *testing.T) {
	t.Parallel()
	f := newFixture(t, nil)
	pl := newPlayer(t, f.p, "start")
	pl.lines = append(pl.lines[:1], []byte(`{"id":"<thread/start>","result":{"thread":{"id":"not-a-uuid"}}}`))
	_, err := f.c.Open(context.Background(), f.w, pl.next)
	assert.ErrorContains(t, err, "usable thread id")
}
