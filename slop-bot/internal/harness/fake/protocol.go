// Package fake is a scriptable stand-in harness for slop-bot's own tests.
// It speaks a minimal programmatic protocol over stdin and stdout, and a
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
	Ev      string `json:"ev"` // hello | turn_start | turn_end | tool_start | tool_end | text | usage
	ID      string `json:"id,omitempty"`
	Outcome string `json:"outcome,omitempty"`
	Tool    string `json:"tool,omitempty"`
	Text    string `json:"text,omitempty"`
	Input   int64  `json:"input,omitempty"`
	Output  int64  `json:"output,omitempty"`
}

// Command is what a test tells the fake to do.
type Command struct {
	Cmd string `json:"cmd"`
	// Outcome for turn_end: completed | interrupted | failed.
	Outcome string `json:"outcome,omitempty"`
	// Tool for tool_start and tool_end.
	Tool string `json:"tool,omitempty"`
	Text string `json:"text,omitempty"`
	// Argv for run: a command the fake runs, as an agent's tool call would
	// (e.g. `slop-bot ask ...`), with the fake's own environment.
	Argv []string `json:"argv,omitempty"`
	// Code for exit.
	Code int `json:"code,omitempty"`
	// On for ignore_interrupts and ignore_eof.
	On bool `json:"on,omitempty"`
}

// Commands.
const (
	CmdTurnStart        = "turn_start"
	CmdTurnEnd          = "turn_end"
	CmdToolStart        = "tool_start"
	CmdToolEnd          = "tool_end"
	CmdText             = "text"
	CmdUsage            = "usage"
	CmdRun              = "run"
	CmdExit             = "exit"
	CmdIgnoreInterrupts = "ignore_interrupts"
	CmdIgnoreEOF        = "ignore_eof"
)

// Report is what the fake tells the test.
type Report struct {
	Rep string `json:"rep"`
	// ID is the conversation id, on started.
	ID string `json:"id,omitempty"`
	// Resumed is set on started when the fake was asked to resume ID.
	Resumed bool `json:"resumed,omitempty"`
	// Text and From of a received user message; MidTurn says it came while
	// a turn ran.
	Text    string `json:"text,omitempty"`
	From    string `json:"from,omitempty"`
	MidTurn bool   `json:"mid_turn,omitempty"`
	// Code, Stdout and Stderr of a run.
	Code   int    `json:"code,omitempty"`
	Stdout string `json:"stdout,omitempty"`
	Stderr string `json:"stderr,omitempty"`
	// Ack echoes the command a report acknowledges.
	Ack string `json:"ack,omitempty"`
	// Err is set when a command failed.
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
)

// Environment variables the fake reads.
const (
	// EnvControl is the controller's socket. Without it the fake runs on its
	// own: every message gets one completed turn echoing it.
	EnvControl = "SLOP_BOT_FAKE_CONTROL"
	// EnvID fixes the id of a new conversation.
	EnvID = "SLOP_BOT_FAKE_ID"
)
