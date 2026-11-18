// Package conductor is the server's half of Remote Control, the owner's own
// Claude Code session on their machine driven from the browser: the
// conversations and their transcripts, the work queue a remote control's host
// long-polls, and the events the host reports back. A remote control is a
// conductor agent in the API.
//
// A conductor is a host process on its owner's machine, not a harness
// session, and it attaches to the working copy as it is: the server never says
// what to check out. The server coordinates and the host computes, as with workers: the
// host connects outbound only, so nothing here dials it. Everything that
// crosses between the owner's browser and the host passes through Postgres, so
// any api replica can serve a host's poll, a stream or a message, and none
// holds what another needs.
//
// Who may do what: a person's requests (start, send, stream, stop) go through
// agent.ConductorService.AuthorizeOwner and arrive here as an
// *agent.OwnedConductor, so only the owner reaches a conversation. The host's
// requests arrive as a *Host, which proves the caller's own credential is an
// enabled conductor's; either way every query is bound to that one
// conductor, so a conversation of another conductor reads as absent.
package conductor

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
)

// State is where a conversation is in its life. Starting and Ready are live:
// a conductor runs one conversation's harness process at a time, so going live
// ends the live one it replaces.
type State string

const (
	// StateStarting: a start is queued or delivered and the host has not yet
	// said the process is up.
	StateStarting State = "starting"
	StateReady    State = "ready"
	// StateFailed: the host could not start the process. The next message tries
	// again.
	StateFailed State = "failed"
	// StateStopped is a session with no process: disconnected by its owner,
	// replaced by another session, or ended by a disable. The next message
	// reconnects it, resuming the same harness session.
	StateStopped State = "stopped"
)

// Live reports whether the conversation occupies its conductor.
func (s State) Live() bool { return s == StateStarting || s == StateReady }

// Conversation is one harness session between a conductor's owner and the
// model. Its ID is the harness's --session-id.
type Conversation struct {
	ID          uuid.UUID
	ConductorID uuid.UUID
	// OwnerUserID is nil once the owner's account is deleted
	// (OwnerUserDeleted); the conversation stays, ownerless.
	OwnerUserID      *uuid.UUID
	OwnerUserDeleted bool
	State            State
	Failure          *string
	// LastSeq is the newest transcript event, 0 before any.
	LastSeq int64
	// Truncated says assistant text past MaxTranscriptBytes (or events past
	// MaxEventsPerConversation) was dropped.
	Truncated bool
	// Repo is the newest repo state the host reported.
	Repo *RepoState
	// StartSeq is the queue number of the start the conversation is on.
	StartSeq int64
	// FirstLine is the default name, from the first message; Title is the
	// owner's own, which wins. Either is nil until set.
	FirstLine *string
	Title     *string
	// ContextTokens is the input side of the newest turn's usage as the host
	// reported it (cache read + cache write + input); 0 is unknown. CanCompact
	// is whether the host's harness can compact the session.
	ContextTokens int64
	CanCompact    bool
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// CapabilityInterrupt is what a host reports when it acts on interrupt work.
const CapabilityInterrupt = "interrupt"

// Limits on what a host says of itself.
const (
	MaxHostCapabilities    = 20
	MaxHostCapabilityBytes = 40
	MaxHostVersionBytes    = 64
)

// EventKind is one kind of transcript event.
type EventKind string

const (
	EventUser      EventKind = "user"
	EventAssistant EventKind = "assistant"
	EventTurnEnd   EventKind = "turn_end"
	EventReady     EventKind = "ready"
	EventFailed    EventKind = "failed"
	EventStopped   EventKind = "stopped"
	// EventUnsupported is the host saying it was sent a kind of work it does
	// not know (its code is older than the server's); Body is that kind.
	EventUnsupported EventKind = "unsupported"
	// EventInterruptRequested is recorded when the owner asks to stop a turn,
	// so every viewer shows it as stopping; EventInterrupted when the host
	// reports the turn ended because the owner stopped it. Both carry the turn.
	EventInterruptRequested EventKind = "interrupt_requested"
	EventInterrupted        EventKind = "interrupted"
	// EventRepoState is the working copy's state after a turn,
	// its Body the state as JSON (see RepoState).
	EventRepoState EventKind = "repo_state"
	// EventActivity is one tool call of a turn: its Body is an Activity as
	// JSON, a one-line description and never the call's input or output. A call
	// is reported when it starts and again when it ends, under the same call id.
	EventActivity EventKind = "activity"
	// EventTruncated marks where assistant text stopped being kept.
	EventTruncated EventKind = "truncated"
	// EventContextCleared and EventContextCompacted are dividers the host
	// reports once it has cleared or compacted the session's context.
	EventContextCleared   EventKind = "context_cleared"
	EventContextCompacted EventKind = "context_compacted"
	// EventContext is a host report, not a transcript event: the conversation's
	// context_tokens and can_compact.
	EventContext EventKind = "context"
)

// Event is one entry of a conversation's transcript. Seq is its place in it;
// a stream that has seen n has seen every event up to n.
type Event struct {
	Seq  int64
	Kind EventKind
	// Turn is the host's number for the turn, on assistant text and its end.
	Turn *int64
	// Body is the text of a user or assistant event, the reason of a failed or
	// stopped.
	Body string
	// Block names the model message an assistant event belongs to, where the
	// host knows it: events of one block are one paragraph, of different
	// blocks separate ones. Nil on events from before it was reported.
	Block     *string
	CreatedAt time.Time
}

// TranscriptPage is the newest whole turns of a transcript before a point.
type TranscriptPage struct {
	// Events are in seq order.
	Events []Event
	// HasMore says older events exist; NextBefore is the seq to ask with next.
	HasMore    bool
	NextBefore int64
	// Prompts are the owner's last distinct non-blank messages, newest last.
	// Set only on a page that starts from the newest event.
	Prompts []string
}

// WorkKind is a kind of work for the host.
type WorkKind string

const (
	WorkStart   WorkKind = "start"
	WorkMessage WorkKind = "message"
	WorkStop    WorkKind = "stop"
	// WorkInterrupt asks the host to end the named turn, keeping the process.
	WorkInterrupt WorkKind = "interrupt"
	// WorkClear and WorkCompact ask the host to clear the session's context
	// (keeping the conversation) or to compact it.
	WorkClear   WorkKind = "clear"
	WorkCompact WorkKind = "compact"
)

// WorkItem is one item of a conductor's queue. Seq orders them and is what
// the host acknowledges.
type WorkItem struct {
	Seq          int64
	Kind         WorkKind
	Conversation uuid.UUID
	// Resume is set on a start of a conversation whose session exists.
	Resume bool
	// MessageSeq and Text are set for a message: the transcript event it
	// delivers and the owner's words.
	MessageSeq *int64
	Text       string
	// Attachments are the staged files a message carries.
	Attachments []AttachmentRef
	// Turn is the turn to interrupt, set for an interrupt.
	Turn *int64
}

// Limits. Sized for a person typing and a model answering; anything past
// them is refused, or for the host's text dropped, rather than stored.
const (
	// MaxMessageBytes is one message from the owner.
	MaxMessageBytes = 32 * 1024
	// MaxDeltaBytes is one batch of assistant text from the host.
	MaxDeltaBytes = 64 * 1024
	// MaxTranscriptBytes caps the text one conversation keeps. Assistant text
	// past it is not stored; the owner's own messages are refused.
	MaxTranscriptBytes = 16 << 20
	// MaxEventsPerConversation caps the transcript's events, which the byte
	// cap does not: an event with no text costs a row all the same. Past it
	// assistant text and turn ends are dropped (marked once, still
	// acknowledged), the owner's messages are refused, and the events that
	// change state are kept for another EventReserve.
	MaxEventsPerConversation = 20000
	EventReserve             = 64
	// MaxActivityBytes is one activity event's JSON body.
	MaxActivityBytes = 1024
	// MaxBlockBytes is a block id.
	MaxBlockBytes = 128
	// ActivityEventsShare is the part of MaxEventsPerConversation past which
	// activity is dropped, so that the text and turn ends the transcript is for
	// keep the rest of the room.
	ActivityEventsShare = 0.75
	// MaxFailureBytes is a failure's or a stop's reason.
	MaxFailureBytes = 2048
	// MaxEventsPerBatch is how many events one host report may carry.
	MaxEventsPerBatch = 200
	// MaxQueuedWork is how many items may wait unacknowledged for one
	// conductor: its host being away must not let the queue grow without
	// limit. An owner sees ErrQueueFull and waits for the host.
	MaxQueuedWork = 1000
	// MaxWorkPerPoll is how many items one poll returns.
	MaxWorkPerPoll = 50
	// MaxEventsPerRead is how many transcript events one read returns.
	MaxEventsPerRead = 500
	// MaxTurnsPerPage is how many turns one backward read may ask for.
	MaxTurnsPerPage = 50
	// MaxPageBytes stops a backward read adding older turns once the page's
	// text passes it. A page always holds one whole turn, so a turn larger
	// than this is returned whole (bounded by MaxTranscriptBytes).
	MaxPageBytes = 256 << 10
	// MaxRecentPrompts is how many of the owner's distinct recent prompts the
	// first backward page carries, for the composer's history.
	MaxRecentPrompts = 10
	// recentPromptScan bounds the user events looked at to find them.
	recentPromptScan = 100
)

var (
	// ErrNotFound is a conversation that does not exist for this conductor --
	// another conductor's and another tenant's included.
	ErrNotFound = errors.New("conductor: conversation not found")
	// ErrNotConductor is a host request whose credential is not a conductor's.
	ErrNotConductor = errors.New("conductor: the caller is not a conductor")
	// ErrBusy is a start or reconnect that lost a race with another one for the
	// same conductor: each takes over from the live session, and one commits
	// first.
	ErrBusy = errors.New("conductor: another session was taken over at the same moment")
	// ErrQueueFull: too much is waiting for the host.
	ErrQueueFull = errors.New("conductor: too much work is waiting for the host")
	// ErrTranscriptFull: the conversation has reached MaxTranscriptBytes.
	ErrTranscriptFull = errors.New("conductor: the conversation has reached its size limit")
	// ErrHostBusy is a second host process polling a conductor whose first has
	// not yet been silent long enough to be given up on, or a report from a
	// process that is not the conductor's current host.
	ErrHostBusy = errors.New("conductor: another host is serving this conductor")
	// ErrGap is a host report that skips a number the server has not seen.
	// Report from Acked+1.
	ErrGap = errors.New("conductor: a report skips events the server has not received")
	// ErrWorkPending is a delete of a conversation whose host has work for it
	// it has not acknowledged: a stop above all, which the delete would erase.
	ErrWorkPending = errors.New("conductor: the host has not yet acknowledged the conversation's work")
	// ErrNotReplying is an interrupt of a turn that is not running: it has
	// ended, or the conversation has no process.
	ErrNotReplying = errors.New("conductor: that reply is not running")
	// ErrHostLacksInterrupt is an interrupt for a conductor whose host has not
	// said it can: queued, the work item would be ignored.
	ErrHostLacksInterrupt = errors.New("conductor: the host does not support interrupting a turn")
	// ErrTurnRunning is a clear or compact while a turn is running, without the
	// request saying to interrupt it.
	ErrTurnRunning = errors.New("conductor: a reply is running")
	// ErrNoProcess is a clear or compact of a conversation with no harness
	// process: the host has nothing to act on until a message reconnects it.
	ErrNoProcess = errors.New("conductor: the conversation has no process")
	// ErrCannotCompact is a compact of a conversation whose harness cannot.
	ErrCannotCompact = errors.New("conductor: the conversation cannot be compacted")
	// ErrInvalid is a request the server refuses on its content.
	ErrInvalid = errors.New("conductor: invalid request")
)

// GapError carries what the server has, for the host to resume from.
type GapError struct{ Acked int64 }

func (e *GapError) Error() string { return fmt.Sprintf("%v: the server has up to %d", ErrGap, e.Acked) }
func (e *GapError) Unwrap() error { return ErrGap }

// refusal is an error that carries what to tell the caller: the sentinel it
// matches with errors.Is, and a message written for them, so no wrapping
// context from further down can reach an answer.
type refusal struct {
	kind   error
	detail string
}

func (e *refusal) Error() string { return fmt.Sprintf("%v: %s", e.kind, e.detail) }
func (e *refusal) Unwrap() error { return e.kind }

func invalid(format string, args ...any) error {
	return &refusal{kind: ErrInvalid, detail: fmt.Sprintf(format, args...)}
}

const (
	// MaxFirstLineRunes caps the default name taken from a first message.
	MaxFirstLineRunes = 120
	// MaxTitleRunes caps a name the owner sets.
	MaxTitleRunes = 100
)

// firstLine is a message's first non-blank line with its whitespace collapsed,
// cut on a rune boundary; nil when there is none.
func firstLine(text string) *string {
	for _, line := range strings.Split(text, "\n") {
		words := strings.Fields(line)
		if len(words) == 0 {
			continue
		}
		out := strings.Join(words, " ")
		if r := []rune(out); len(r) > MaxFirstLineRunes {
			out = strings.TrimSpace(string(r[:MaxFirstLineRunes]))
		}
		return &out
	}
	return nil
}

// cleanTitle is the owner's name for a session as stored: trimmed, nil to
// clear it. A name that is not valid text, has control characters or is too
// long is refused rather than altered.
func cleanTitle(raw *string) (*string, error) {
	if raw == nil {
		return nil, nil
	}
	if !utf8.ValidString(*raw) {
		return nil, invalid("a name must be valid UTF-8")
	}
	title := strings.TrimSpace(*raw)
	if strings.ContainsFunc(title, unicode.IsControl) {
		return nil, invalid("a name must not contain control characters")
	}
	if utf8.RuneCountInString(title) > MaxTitleRunes {
		return nil, invalid("a name is at most %d characters", MaxTitleRunes)
	}
	if title == "" {
		return nil, nil
	}
	return &title, nil
}
