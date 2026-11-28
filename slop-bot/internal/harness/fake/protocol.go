// Package fake is a scriptable stand-in harness for slop-bot's own tests.
// It speaks a minimal programmatic protocol over stdin and stdout (fake/rpc),
// or runs as a TUI in a terminal and reports through hooks (fake/tui), and a
// test drives what it does through a Controller. It decides nothing: it
// does what the test says and reports what it received, so a test can
// assert on slop-bot's behaviour, not a model's.
package fake

// Wire messages from slop-bot to the fake, on its stdin.
type inMsg struct {
	Op string `json:"op"` // hello | user | interrupt
	// Text of a user message.
	Text string `json:"text,omitempty"`
	// From is who sent a user message.
	From string `json:"from,omitempty"`
}

// Wire messages from the fake to slop-bot, on its stdout.
type outMsg struct {
	Ev      string `json:"ev"` // hello | turn_start | turn_end | tool_start | tool_end | text | usage | limit
	ID      string `json:"id,omitempty"`
	Outcome string `json:"outcome,omitempty"`
	Tool    string `json:"tool,omitempty"`
	Text    string `json:"text,omitempty"`
	Input   int64  `json:"input,omitempty"`
	Output  int64  `json:"output,omitempty"`
	// Used and ResetsAt (unix seconds) of a limit.
	Used     float64 `json:"used,omitempty"`
	ResetsAt int64   `json:"resets_at,omitempty"`
	Window   string  `json:"window,omitempty"`
}

// Command is what a test tells the fake to do.
type Command struct {
	Cmd string `json:"cmd"`
	// Outcome for turn_end: completed | interrupted | failed.
	Outcome string `json:"outcome,omitempty"`
	// Tool for tool_start and tool_end.
	Tool string `json:"tool,omitempty"`
	// Text of text; and for tool_start and tool_end, the input and output
	// the transcript records.
	Text string `json:"text,omitempty"`
	// Used is the percentage of the plan used, and ResetsAt (unix seconds)
	// when its window resets, for limit; Window names the window.
	Used     float64 `json:"used,omitempty"`
	ResetsAt int64   `json:"resets_at,omitempty"`
	Window   string  `json:"window,omitempty"`
	// Argv for run: a command the fake runs, as an agent's tool call would
	// (e.g. `slop-bot ask ...`), with the fake's own environment.
	Argv []string `json:"argv,omitempty"`
	// Code for exit.
	Code int `json:"code,omitempty"`
	// On for ignore_interrupts, ignore_eof and quiet_cancel.
	On bool `json:"on,omitempty"`
	// Count for drop_submits.
	Count int `json:"count,omitempty"`
}

// Commands.
const (
	CmdTurnStart        = "turn_start"
	CmdTurnEnd          = "turn_end"
	CmdToolStart        = "tool_start"
	CmdToolEnd          = "tool_end"
	CmdText             = "text"
	CmdUsage            = "usage"
	CmdLimit            = "limit"
	CmdRun              = "run"
	CmdExit             = "exit"
	CmdIgnoreInterrupts = "ignore_interrupts"
	CmdIgnoreEOF        = "ignore_eof"
	// CmdStep (TUI mode) runs the step hook, before a model call: where a
	// TUI harness takes injected messages mid-turn (agy's PreInvocation).
	CmdStep = "step"
	// CmdDropSubmits (TUI mode) ignores the next Count submit keys, as a
	// harness still handling a paste swallows an Enter.
	CmdDropSubmits = "drop_submits"
	// CmdQuietCancel (TUI mode) makes a cancel run no hook, as agy's
	// doesn't: only the screen shows it.
	CmdQuietCancel = "quiet_cancel"
	// CmdIgnoreHangup (TUI mode) makes the fake survive SIGHUP, as a TUI
	// that misses its terminal closing would.
	CmdIgnoreHangup = "ignore_hangup"
)

// Report is what the fake tells the test.
type Report struct {
	Rep string `json:"rep"`
	// ID is the conversation id, on started.
	ID string `json:"id,omitempty"`
	// Resumed is set on started when the fake was asked to resume ID.
	Resumed bool `json:"resumed,omitempty"`
	// TUI is set on started when the fake runs in TUI mode.
	TUI bool `json:"tui,omitempty"`
	// Text and From of a received user message, or a paste's text; MidTurn
	// says it came while a turn ran.
	Text    string `json:"text,omitempty"`
	From    string `json:"from,omitempty"`
	MidTurn bool   `json:"mid_turn,omitempty"`
	// Via is how a user message arrived in TUI mode: ViaKeys or ViaHook.
	Via string `json:"via,omitempty"`
	// Key is a key report's key; Dropped says the fake ignored it.
	Key     string `json:"key,omitempty"`
	Dropped bool   `json:"dropped,omitempty"`
	// Event and Reply are a hook's event and the reply it read back.
	Event string `json:"event,omitempty"`
	Reply string `json:"reply,omitempty"`
	// Code, Stdout and Stderr of a run.
	Code   int    `json:"code,omitempty"`
	Stdout string `json:"stdout,omitempty"`
	Stderr string `json:"stderr,omitempty"`
	// Ack echoes the command a report acknowledges.
	Ack string `json:"ack,omitempty"`
	// Err is set when a command or hook failed.
	Err string `json:"err,omitempty"`
}

// Reports.
const (
	RepStarted     = "started"
	RepUser        = "user"
	RepInterrupt   = "interrupt"
	RepStdinClosed = "stdin_closed"
	RepRan         = "ran"
	RepAck         = "ack"
	// TUI mode: what the terminal delivered, each hook's reply, and a quit.
	RepPaste = "paste"
	RepKey   = "key"
	RepHook  = "hook"
	RepQuit  = "quit"
)

// TUIQuit is the line that quits the TUI mode, as /exit quits Claude Code's:
// how a person hands an attached agent back.
const TUIQuit = "/quit"

// How a user message arrived, in TUI mode.
const (
	ViaKeys = "keys"
	ViaHook = "hook"
)

// Hook events the TUI mode runs, through EnvHookCmd.
const (
	HookSessionStart = "session_start"
	HookPromptSubmit = "prompt_submit"
	HookTurnStart    = "turn_start"
	HookTurnEnd      = "turn_end"
	HookToolStart    = "tool_start"
	HookToolEnd      = "tool_end"
	HookText         = "text"
	HookUsage        = "usage"
	HookStep         = "step"
)

// HookPayload is what the TUI mode passes a hook on stdin.
type HookPayload struct {
	ConversationID string `json:"conversation_id"`
	Resumed        bool   `json:"resumed,omitempty"`
	Prompt         string `json:"prompt,omitempty"`
	Tool           string `json:"tool,omitempty"`
	Text           string `json:"text,omitempty"`
	Outcome        string `json:"outcome,omitempty"`
	Input          int64  `json:"input,omitempty"`
	Output         int64  `json:"output,omitempty"`
}

// HookReply is what a hook answers on stdout. Inject puts messages in front
// of the model (on step and turn_end); on turn_end, Continue keeps the turn
// going.
type HookReply struct {
	Inject   []InjectedMessage `json:"inject,omitempty"`
	Continue bool              `json:"continue,omitempty"`
}

// InjectedMessage is one message a hook reply injects.
type InjectedMessage struct {
	From string `json:"from,omitempty"`
	Text string `json:"text"`
}

// Environment variables the fake reads.
const (
	// EnvControl is the controller's socket. Without it the fake runs on its
	// own: every message gets one completed turn echoing it.
	EnvControl = "SLOP_BOT_FAKE_CONTROL"
	// EnvID fixes the id of a new conversation.
	EnvID = "SLOP_BOT_FAKE_ID"
	// EnvHookCmd is the TUI mode's hook command, a JSON argv. The fake
	// appends the event name, passes a HookPayload on stdin and reads a
	// HookReply from stdout. It stands in for `slop-bot hook <event>`.
	EnvHookCmd = "SLOP_BOT_FAKE_HOOK_CMD"
	// EnvTranscripts is a directory the fake keeps its transcript in, as
	// <id>.jsonl in the normalised format; unset, it keeps none.
	EnvTranscripts = "SLOP_BOT_FAKE_TRANSCRIPTS"
)
