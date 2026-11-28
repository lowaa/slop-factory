// Package harness is slop-bot's seam onto the coding-agent harnesses
// (Claude Code, Codex, pi, agy). An implementation is one harness driven one
// way, a driver: claude-code/stream-json, agy/tui and so on. The core
// (scheduler, lifecycle, tell, attach, CLI) sees only AIHarness and
// Conversation, and never knows whether a conversation runs over a protocol
// or a TUI.
package harness

import (
	"context"
	"time"

	"github.com/lowaa/slop-factory/slop-bot/internal/ident"
)

// AIHarness is one harness under one driver.
type AIHarness interface {
	// Name is the harness, e.g. "claude-code".
	Name() string
	// Driver is how it is driven, e.g. "stream-json" or "tui".
	Driver() string
	Capabilities() Capabilities
	// Start launches a session in ws, resuming resume's conversation when it
	// is non-nil.
	Start(ctx context.Context, ws *Workspace, resume *ConversationRef) (Conversation, error)
}

// Conversation is one live session, driven whichever way.
type Conversation interface {
	// Ref is the harness's own id for the conversation, for resume.
	Ref() ConversationRef
	// Send delivers a user message: steered into the running turn where the
	// driver can, otherwise at the next turn boundary. A message sent while
	// idle starts a turn.
	Send(ctx context.Context, msg *Message) error
	// Interrupt stops the running turn; the session stays usable.
	Interrupt(ctx context.Context) error
	// Events is the structured stream: turns, tools, text, usage, exit.
	// It closes after the session has exited.
	Events() <-chan Event
	// Stop ends the session. Without force it waits for the running turn to
	// settle first. It returns once the session is gone and resumable.
	Stop(ctx context.Context, force bool) error
	// AttachPlan says how a person takes this conversation over.
	AttachPlan(ctx context.Context) (*AttachPlan, error)
}

// Capabilities are what a harness's driver can do, so the core picks the best
// path per harness instead of special-casing names.
type Capabilities struct {
	// MidTurnSteer means Send during a turn reaches the model in that turn.
	MidTurnSteer bool
	// ChosenIDs means the conversation id is chosen by slop-bot up front.
	ChosenIDs bool
}

// ConversationRef is enough to resume a conversation later.
type ConversationRef struct {
	Harness string `json:"harness"`
	Driver  string `json:"driver"`
	ID      string `json:"id"`
}

// Message is what an agent receives. From is shown to the agent so it can
// weigh a message by who sent it.
type Message struct {
	From ident.Principal
	Text string
}

// Workspace is everything a harness needs to run one agent.
type Workspace struct {
	Agent  ident.AgentID
	Ticket ident.TicketID
	// Dir is the primary worktree: the harness's cwd.
	Dir string
	// ExtraDirs are the ticket's other worktrees.
	ExtraDirs []string
	// Briefing is the rendered briefing, injected through the harness's
	// system-prompt channel; BriefingFile holds the same text on disk.
	Briefing     string
	BriefingFile string
	// Model and Effort are blank for the harness's own default.
	Model  string
	Effort string
	// Permissive runs without permission prompts.
	Permissive bool
	// Env is added to the process environment (the agent token, ...).
	Env map[string]string
	// APIKeyOptIn lets the harness's API key variables through.
	APIKeyOptIn bool
}

// AttachKind is how attach takes a conversation over.
type AttachKind int

const (
	// HandOver stops the programmatic session and runs the harness's TUI in
	// the person's terminal, resuming the same conversation.
	HandOver AttachKind = iota + 1
	// Join attaches the person to the session's existing TUI (tmux).
	Join
)

// AttachPlan is how `slop-bot attach` takes this conversation over.
type AttachPlan struct {
	Kind    AttachKind
	Command *LaunchSpec
}

// LaunchSpec is a process to run.
type LaunchSpec struct {
	Path string
	Args []string
	// Env is the complete environment, as KEY=VALUE.
	Env []string
	Dir string
}

// EventKind classifies an Event.
type EventKind string

// Event kinds.
const (
	// Ready means the session is up and can take a message.
	Ready EventKind = "ready"
	// TurnStarted and TurnEnded bracket a turn.
	TurnStarted EventKind = "turn_started"
	TurnEnded   EventKind = "turn_ended"
	// ToolStarted and ToolEnded bracket a tool call.
	ToolStarted EventKind = "tool_started"
	ToolEnded   EventKind = "tool_ended"
	// Text is the agent's own output.
	Text EventKind = "text"
	// UsageReport carries token usage and, where the harness says, cost.
	UsageReport EventKind = "usage"
	// LimitReport carries how much of the plan's allowance is left.
	LimitReport EventKind = "limit"
	// Exited is always the last event: the process is gone.
	Exited EventKind = "exited"
)

// TurnOutcome is how a turn ended.
type TurnOutcome string

// Turn outcomes.
const (
	TurnCompleted   TurnOutcome = "completed"
	TurnInterrupted TurnOutcome = "interrupted"
	TurnFailed      TurnOutcome = "failed"
)

// Event is one thing that happened in a conversation.
type Event struct {
	Kind EventKind
	At   time.Time
	// Outcome is set on TurnEnded.
	Outcome TurnOutcome
	// Tool is set on ToolStarted and ToolEnded.
	Tool string
	// Text is set on Text, and on TurnEnded when the harness reports an error.
	Text  string
	Usage *Usage
	Limit *Limit
	// ExitCode and Err are set on Exited.
	ExitCode int
	Err      error
}

// Usage is token usage for a turn.
type Usage struct {
	InputTokens      int64
	OutputTokens     int64
	CacheReadTokens  int64
	CacheWriteTokens int64
	// CostUSD is the harness's own figure, where it gives one.
	CostUSD float64
	// CostEstimated means CostUSD is the harness's estimate from list
	// prices, not what was billed: on a subscription login nothing is
	// charged per token, so it must not be summed as spend.
	CostEstimated bool
}

// Limit is the plan allowance a harness reports.
type Limit struct {
	// UsedPercent of the current window.
	UsedPercent float64
	ResetsAt    time.Time
}
