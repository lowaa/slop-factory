package state

import (
	"context"
	"time"

	"github.com/lowaa/slop-factory/slop-bot/internal/ident"
)

// The interfaces are cut by what a consumer does, so the scheduler, the
// inbox, socket auth and so on each depend on just the methods they use,
// and a test above this package can fake only those. A consumer that needs
// another combination embeds the pieces in an interface of its own.

// AgentReader reads agent records.
type AgentReader interface {
	Get(ctx context.Context, id ident.AgentID) (*Agent, error)
	LiveForTicket(ctx context.Context, ticket ident.TicketID) (*Agent, error)
	ReleasedForTicket(ctx context.Context, ticket ident.TicketID) (bool, error)
	ListLive(ctx context.Context) ([]*Agent, error)
	History(ctx context.Context, id ident.AgentID) ([]StateChange, error)
}

// TokenLookup resolves an agent token, by its control.HashToken, to its live
// agent, for socket auth: it is the shape of control.TokenResolver, so the
// daemon hands the store to control as is.
type TokenLookup interface {
	AgentForTokenHash(ctx context.Context, hash string) (ident.Principal, error)
}

// AgentWriter records an agent's life: the scheduler and the state
// tracker.
type AgentWriter interface {
	Create(ctx context.Context, n *NewAgent) (*Agent, error)
	SetState(ctx context.Context, t *Transition) (bool, error)
	SetConversationID(ctx context.Context, id ident.AgentID, conversation string) error
	SetProgressNote(ctx context.Context, id ident.AgentID, note string) error
	SetDeclared(ctx context.Context, id ident.AgentID, declared, summary string) error
	AddNudge(ctx context.Context, id ident.AgentID) (int, error)
	ResetNudges(ctx context.Context, id ident.AgentID) error
}

// WorktreeRecorder records the worktrees an agent makes and cleans up.
type WorktreeRecorder interface {
	AddWorktree(ctx context.Context, id ident.AgentID, w *Worktree) error
	RemoveWorktree(ctx context.Context, id ident.AgentID, repo string) error
}

// AttachLeases is the attach hand-over's lease.
type AttachLeases interface {
	AcquireLease(ctx context.Context, id ident.AgentID, l *Lease) error
	RenewLease(ctx context.Context, id ident.AgentID, l *Lease) error
	ReleaseLease(ctx context.Context, id ident.AgentID, holder string) error
	ReleaseExpiredLease(ctx context.Context, id ident.AgentID, holder string, at time.Time) error
	ExpiredLeases(ctx context.Context, at time.Time) ([]*Agent, error)
}

// AgentStore is all of the agents table.
type AgentStore interface {
	AgentReader
	TokenLookup
	AgentWriter
	WorktreeRecorder
	AttachLeases
}

// Outbox queues messages: tell, nudges and system notices.
type Outbox interface {
	Enqueue(ctx context.Context, n *NewMessage) (*Message, error)
}

// Inbox is a recipient's side of delivery: the delivery policy and
// `slop-bot inbox`.
type Inbox interface {
	Pending(ctx context.Context, to Party) ([]Message, error)
	Unread(ctx context.Context, to Party) ([]Message, error)
	MarkDelivered(ctx context.Context, id int64, path DeliveryPath) error
	MarkRead(ctx context.Context, to Party, ids []int64) (int64, error)
}

// AskStore is `slop-bot ask`, `asks`, and the user's answers.
type AskStore interface {
	Ask(ctx context.Context, n *NewAsk) (*Ask, error)
	Answer(ctx context.Context, n *NewMessage) (*Message, int64, error)
	OpenAsks(ctx context.Context) ([]*Ask, error)
	OpenAsksFrom(ctx context.Context, agent ident.AgentID) ([]*Ask, error)
}

// MessageHistory is `inbox --log` and the loop guard's counts.
type MessageHistory interface {
	Log(ctx context.Context, q *LogQuery) ([]Message, error)
	CountBetween(ctx context.Context, a, b Party, since time.Time) (int64, error)
	CountSentBy(ctx context.Context, from Party, since time.Time) (int64, error)
}

// MessageStore is all of the inbox.
type MessageStore interface {
	Outbox
	Inbox
	AskStore
	MessageHistory
}

// EventAppender is anything that emits events.
type EventAppender interface {
	Append(ctx context.Context, n *NewEvent) (int64, error)
}

// EventFollower reads the log from a cursor, waiting for more with Follow.
type EventFollower interface {
	ReadAfter(ctx context.Context, after int64, limit int) ([]Event, error)
	Follow(ctx context.Context, after int64, limit int) ([]Event, error)
}

// EventLog is both ends of the event log.
type EventLog interface {
	EventAppender
	EventFollower
}

// QueueStore is the scheduler's waiting tickets.
type QueueStore interface {
	PushBack(ctx context.Context, pool Pool, ticket ident.TicketID) error
	PushFront(ctx context.Context, pool Pool, ticket ident.TicketID) error
	Pop(ctx context.Context, pool Pool) (ident.TicketID, bool, error)
	List(ctx context.Context, pool Pool) ([]QueuedTicket, error)
	Remove(ctx context.Context, ticket ident.TicketID) (bool, error)
}

// CursorStore is the TicketingSystem watchers' upstream positions.
type CursorStore interface {
	Get(ctx context.Context, system string) (string, bool, error)
	Set(ctx context.Context, system, position string) error
}

// TranscriptIndex is the retained transcripts, for `transcript` and the
// retention sweep.
type TranscriptIndex interface {
	Add(ctx context.Context, n *NewTranscript) error
	ForAgent(ctx context.Context, agent ident.AgentID) ([]Transcript, error)
	Expired(ctx context.Context, at time.Time) ([]Transcript, error)
	Remove(ctx context.Context, path string) error
}

// AutonomySetting is the persisted `slop-bot autonomy` toggle.
type AutonomySetting interface {
	Autonomy(ctx context.Context) (enabled, ok bool, err error)
	SetAutonomy(ctx context.Context, enabled bool) error
}

// RowPruner is the retention sweep.
type RowPruner interface {
	Prune(ctx context.Context, c *PruneCutoffs) (*PruneResult, error)
}

var (
	_ AgentStore      = (*Agents)(nil)
	_ MessageStore    = (*Messages)(nil)
	_ EventLog        = (*Events)(nil)
	_ QueueStore      = (*Queue)(nil)
	_ CursorStore     = (*Cursors)(nil)
	_ TranscriptIndex = (*Transcripts)(nil)
	_ AutonomySetting = (*Settings)(nil)
	_ RowPruner       = (*Pruner)(nil)
)
