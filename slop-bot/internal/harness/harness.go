// Package harness is slop-bot's seam onto the coding-agent harnesses
// (Claude Code, Codex, pi, agy). An implementation is one harness driven one
// way, a driver: claude-code/stream-json, agy/tui and so on. The core
// (scheduler, lifecycle, tell, attach, CLI) sees only AIHarness and
// Conversation, and never knows whether a conversation runs over a protocol
// or a TUI.
package harness

import (
	"context"
	"errors"
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
	// idle starts a turn. A TUI driver may return ErrAttached (nothing was
	// sent: send it later) or ErrUnsubmitted (part of it was typed: don't
	// send it again).
	Send(ctx context.Context, msg *Message) error
	// Interrupt stops the running turn; the session stays usable.
	Interrupt(ctx context.Context) error
	// Events is the structured stream: turns, tools, text, usage, exit.
	// It closes after the session has exited, with Exited last; or, with no
	// Exited, when its owner lets go of a session that lives on (a TUI
	// driver's Close, at daemon shutdown, for a later Adopt).
	Events() <-chan Event
	// Stop ends the session. Without force it waits for the running turn to
	// settle first. It returns once the session is gone and resumable, or,
	// without force, ErrAttached from a TUI driver that won't type its quit
	// into a session a person is attached to: the session runs on, and the
	// caller forces or waits.
	Stop(ctx context.Context, force bool) error
	// AttachPlan says how a person takes this conversation over.
	AttachPlan(ctx context.Context) (*AttachPlan, error)
}

// Adopter is a harness whose sessions outlive the daemon (a TUI driver's,
// in slop-bot's tmux server). Its conversations are Lasting: at shutdown the
// daemon lets go of them instead of stopping them, and after a restart
// takes each back with AdoptAgent instead of resuming it, so a turn in
// flight carries on rather than being cut short.
type Adopter interface {
	// AdoptAgent takes over agent's session, which outlived the daemon that
	// started it: ErrNoSession if it didn't.
	AdoptAgent(ctx context.Context, agent ident.AgentID) (Conversation, error)
}

// Lasting is a conversation whose session can outlive it (see Adopter).
type Lasting interface {
	// Close lets go of the session without ending it: Events closes with no
	// Exited, and the session runs on for a later AdoptAgent.
	Close() error
}

var (
	// ErrNoSession means there is no session to adopt: it ended with the
	// daemon, or never started.
	ErrNoSession = errors.New("harness: no session to adopt")
	// ErrAttached means a person is attached to a TUI session, so the driver
	// won't type into it. Nothing was typed.
	ErrAttached = errors.New("harness: a person is attached to the session")
	// ErrUnsubmitted means typing began, then stopped short of submitting
	// (a person attached, or the harness never confirmed): the text may sit
	// in the prompt, so sending again would type it twice.
	ErrUnsubmitted = errors.New("harness: typed into the prompt but not submitted")
)

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
	// IDs are the inbox messages Text renders. A driver that can't put Text
	// in front of the model (a TUI, idle) points the agent at its inbox
	// instead, and says so through the daemon's pointer, which keeps these
	// in the agent's inbox until it reads them.
	IDs []int64
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
	// Model and Effort are blank for the implementation's default, which may
	// be one slop-bot chooses rather than the harness's own (pi always
	// passes a model).
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
	// Exited is the last event when the process is gone (see Events).
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

// Usage is token usage for a turn, or for work a harness does between turns,
// such as a compaction.
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

// Limit is one of the plan's windows, as a harness reports it. A plan has
// several (a 5-hour and a weekly one, say), each reported as its own Limit:
// every one at its limit holds the harness, so none may stand in for
// another.
type Limit struct {
	// Window names the window, unique within the harness, e.g. "five_hour"
	// or "codex/primary"; "" for a harness that reports just one.
	Window string
	// UsedPercent of the window.
	UsedPercent float64
	// ResetsAt is zero when the harness didn't say.
	ResetsAt time.Time
}
