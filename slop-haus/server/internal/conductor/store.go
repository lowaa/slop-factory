package conductor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/lowaa/slop-factory/go-db/pgerr"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/agent"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
)

// StartParams is a new conversation. The ID is the one the host will pass the
// harness as its session id, chosen by the server so no one else can name it.
type StartParams struct {
	ID          uuid.UUID
	ConductorID uuid.UUID
	OwnerUserID uuid.UUID
	// FirstMessage, when set, is the owner's opening words, queued behind the
	// start.
	FirstMessage *string
	// ClientID is the browser's id for the start. A start repeated with the
	// same one is answered with the conversation it made.
	ClientID *uuid.UUID
}

// HostEvent is one thing the host reports about a conversation. HostSeq is
// the host's own number for it, per conversation, from 1.
type HostEvent struct {
	HostSeq int64
	// Kind is EventAssistant, EventTurnEnd, EventReady, EventFailed or
	// EventRepoState.
	Kind EventKind
	Turn *int64
	// Interrupted is set on a turn_end that ended because the owner stopped
	// the turn, however the host did it.
	Interrupted bool
	// Body is the text or the reason.
	Body string
	// Block is the model message an assistant text belongs to, if the host says.
	Block string
	// WorkSeq is the queue number of the start the host is answering, for ready
	// and failed, and the one the session is on, for repo_state. One that answers a start before the conversation's
	// current one is a stopped session's, arriving late, and is not recorded.
	WorkSeq int64
	// Repo is the state, for EventRepoState.
	Repo *RepoState
	// Activity is the tool call, for EventActivity.
	Activity *Activity
	// ContextTokens and CanCompact are what an EventContext reports; each is
	// left alone when nil.
	ContextTokens *int64
	CanCompact    *bool
}

// Store is the persistence the Service needs. Every call runs in the
// context's tenant, and every one that takes a conductor id is bound to it:
// a conversation of another conductor is ErrNotFound. What the rules are
// (who may call, what a conversation may do) is the Service's; what a
// conversation's state allows, and the order of the queue and the transcript,
// are the database's, so they hold however many replicas run.
type Store interface {
	// StartConversation makes the conversation and queues its start. A conductor
	// that is disabled is agent.ErrIsDisabled: the owner's proof was checked
	// earlier, and this is checked again under a lock a disable must wait for.
	StartConversation(ctx context.Context, p *StartParams) (*Conversation, error)
	// StartedWith is the conversation a start with this client id made, or nil.
	StartedWith(ctx context.Context, conductorID, clientID uuid.UUID) (*Conversation, error)
	// AppendUserMessage stores the owner's message and queues it for the host.
	// A conversation with no process is reconnected first: its start is queued
	// to resume the session, ahead of the message. A message with a ClientID
	// already stored is not stored again: the original event is returned.
	// attachmentIDs are staged attachments of this conversation, uploaded by its
	// owner; any other is ErrInvalid.
	AppendUserMessage(ctx context.Context, conductorID, id uuid.UUID, text string, clientID *uuid.UUID, attachmentIDs []uuid.UUID) (*Event, error)
	// DisconnectConversation ends a live conversation's process and queues a
	// stop. One with no process is returned as it is.
	DisconnectConversation(ctx context.Context, conductorID, id uuid.UUID, reason string) (*Conversation, error)
	// QueueContextAction queues a clear or compact (kind) for the host. A turn
	// running is ErrTurnRunning unless interrupt, which then interrupts it first
	// in the same transaction; a compact of a conversation that cannot is
	// ErrCannotCompact.
	QueueContextAction(ctx context.Context, conductorID, id uuid.UUID, kind WorkKind, interrupt bool) (*Conversation, error)
	// InterruptTurn records that the owner asked to stop a turn and queues the
	// interrupt for the host. A turn already asked about is not queued again;
	// one that has ended, or a conversation with no process, is ErrNotReplying.
	InterruptTurn(ctx context.Context, conductorID, id uuid.UUID, turn int64) (*Conversation, error)
	// DeleteConversation ends a live conversation (queuing its stop), then
	// deletes it and, by cascade, its transcript and work, unless the host has
	// work for it it has not acknowledged: that is ErrWorkPending, and the
	// ending stays. Usage rows keep their figures and lose the reference.
	DeleteConversation(ctx context.Context, conductorID, id uuid.UUID, reason string) error
	// ApplyHostEvents stores what the host reported, in order and once: events
	// numbered at or below the conversation's host_acked are skipped, and the
	// first new one must be the next number (else *GapError). hostID must be
	// the conductor's current host.
	ApplyHostEvents(ctx context.Context, conductorID, hostID, id uuid.UUID, events []HostEvent) (int64, error)

	// SetTitle sets the owner's name for a conversation of any state, nil to
	// clear it. It is not activity: updated_at stays.
	SetTitle(ctx context.Context, conductorID, id uuid.UUID, title *string) (*Conversation, error)

	GetConversation(ctx context.Context, conductorID, id uuid.UUID) (*Conversation, error)
	ListConversations(ctx context.Context, conductorID uuid.UUID, limit int) ([]Conversation, error)
	Events(ctx context.Context, conductorID, id uuid.UUID, after int64, limit int) ([]Event, error)
	// EventsByTurns reads the newest whole turns before a seq (0 for the end),
	// stopping early once the text passes MaxPageBytes but never short of one
	// turn.
	EventsByTurns(ctx context.Context, conductorID, id uuid.UUID, before int64, turns int) (*TranscriptPage, error)

	// ClaimHost makes hostID the conductor's host -- refused with ErrHostBusy
	// while another has polled within ttl -- and acknowledges
	// every item up to ack, deleting them. It returns the highest number
	// acknowledged so far.
	ClaimHost(ctx context.Context, conductorID, hostID uuid.UUID, ack int64, ttl time.Duration, report HostReport) (acked int64, err error)
	// ReleaseHost clears the conductor's host if it is still hostID, so another
	// process may take over at once instead of after the ttl. Any other
	// hostID, and a conductor with no host, change nothing.
	ReleaseHost(ctx context.Context, conductorID, hostID uuid.UUID) error
	// HostStatus says whether a host has polled the conductor within ttl.
	HostStatus(ctx context.Context, conductorID uuid.UUID, ttl time.Duration) (HostStatus, error)
	// ReadWork is the unacknowledged items, in order -- to hostID only while it
	// is still the conductor's host (else ErrHostBusy).
	ReadWork(ctx context.Context, conductorID, hostID uuid.UUID, limit int) ([]WorkItem, error)
}

type PostgresStore struct {
	db *db.DB
}

func NewPostgresStore(database *db.DB) *PostgresStore {
	return &PostgresStore{db: database}
}

var _ Store = (*PostgresStore)(nil)

const conversationColumns = `id, conductor_id, owner_user_id, owner_user_id_deleted, state, failure, next_seq, truncated, start_seq,
	created_at, updated_at, repo_state, first_line, title, context_tokens, can_compact`

func scanConversation(row pgx.Row) (*Conversation, error) {
	var (
		c    Conversation
		repo []byte
	)
	if err := row.Scan(&c.ID, &c.ConductorID, &c.OwnerUserID, &c.OwnerUserDeleted, &c.State, &c.Failure, &c.LastSeq, &c.Truncated, &c.StartSeq,
		&c.CreatedAt, &c.UpdatedAt, &repo, &c.FirstLine, &c.Title, &c.ContextTokens, &c.CanCompact); err != nil {
		return nil, err
	}
	if repo != nil {
		c.Repo = new(RepoState)
		if err := json.Unmarshal(repo, c.Repo); err != nil {
			return nil, fmt.Errorf("decode repo state: %w", err)
		}
	}
	return &c, nil
}

// lockConversation reads and locks a conductor's conversation. Every write
// to a conversation, its transcript and its place in the queue starts here:
// one lock order (conversation, then the conductor's queue) means two
// requests for one conductor cannot deadlock, and the lock is what numbers
// transcript events in the order they commit.
func lockConversation(ctx context.Context, q db.Querier, conductorID, id uuid.UUID) (*Conversation, error) {
	c, err := scanConversation(q.QueryRow(ctx, `
		SELECT `+conversationColumns+` FROM conductor_conversations
		 WHERE id = $1 AND conductor_id = $2 FOR UPDATE`, id, conductorID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("conductor: read conversation: %w", err)
	}
	return c, nil
}

func (s *PostgresStore) GetConversation(ctx context.Context, conductorID, id uuid.UUID) (*Conversation, error) {
	c, err := scanConversation(s.db.QueryRow(ctx, `
		SELECT `+conversationColumns+` FROM conductor_conversations
		 WHERE id = $1 AND conductor_id = $2`, id, conductorID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("conductor: read conversation: %w", err)
	}
	return c, nil
}

func (s *PostgresStore) SetTitle(ctx context.Context, conductorID, id uuid.UUID, title *string) (*Conversation, error) {
	c, err := scanConversation(s.db.QueryRow(ctx, `
		UPDATE conductor_conversations SET title = $3
		 WHERE id = $1 AND conductor_id = $2
		RETURNING `+conversationColumns, id, conductorID, title))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("conductor: set title: %w", err)
	}
	return c, nil
}

func (s *PostgresStore) ListConversations(ctx context.Context, conductorID uuid.UUID, limit int) ([]Conversation, error) {
	rows, err := s.db.Query(ctx, `
		SELECT `+conversationColumns+` FROM conductor_conversations
		 WHERE conductor_id = $1 ORDER BY updated_at DESC, id LIMIT $2`, conductorID, limit)
	if err != nil {
		return nil, fmt.Errorf("conductor: list conversations: %w", err)
	}
	defer rows.Close()
	out := []Conversation{}
	for rows.Next() {
		c, err := scanConversation(rows)
		if err != nil {
			return nil, fmt.Errorf("conductor: scan conversation: %w", err)
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

// appendEvent adds the next event to a locked conversation and returns it.
// The caller holds the conversation's lock and writes the new next_seq back
// with touch.
func appendEvent(ctx context.Context, q db.Querier, c *Conversation, kind EventKind, turn *int64, body string, clientID *uuid.UUID) (*Event, error) {
	return appendBlockEvent(ctx, q, c, kind, turn, body, nil, clientID)
}

func appendBlockEvent(ctx context.Context, q db.Querier, c *Conversation, kind EventKind, turn *int64, body string, block *string, clientID *uuid.UUID) (*Event, error) {
	c.LastSeq++
	if kind == EventUser && c.FirstLine == nil {
		c.FirstLine = firstLine(body)
	}
	e := &Event{Seq: c.LastSeq, Kind: kind, Turn: turn, Body: body, Block: block}
	if err := q.QueryRow(ctx, `
		INSERT INTO conductor_conversation_events (conversation_id, seq, kind, turn, body, block, client_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING created_at`,
		c.ID, e.Seq, string(kind), turn, body, block, clientID).Scan(&e.CreatedAt); err != nil {
		return nil, fmt.Errorf("conductor: append event: %w", err)
	}
	return e, nil
}

// saveConversation writes back what the locked conversation's changes left
// in it.
func saveConversation(ctx context.Context, q db.Querier, c *Conversation, hostAcked, bytes int64) error {
	_, err := q.Exec(ctx, `
		UPDATE conductor_conversations
		   SET state = $2, failure = $3, next_seq = $4, truncated = $5, host_acked = $6, transcript_bytes = $7,
		       start_seq = $8, repo_state = $9, first_line = COALESCE(first_line, $10), updated_at = now(),
		       context_tokens = $11, can_compact = $12
		 WHERE id = $1`,
		c.ID, string(c.State), c.Failure, c.LastSeq, c.Truncated, hostAcked, bytes, c.StartSeq, c.Repo, c.FirstLine,
		c.ContextTokens, c.CanCompact)
	if err != nil {
		return fmt.Errorf("conductor: save conversation: %w", err)
	}
	return nil
}

type counters struct {
	hostAcked, bytes int64
	// queueNext is the newest number the conductor's queue has issued.
	queueNext int64
}

func lockedCounters(ctx context.Context, q db.Querier, id uuid.UUID) (counters, error) {
	var c counters
	if err := q.QueryRow(ctx, `SELECT host_acked, transcript_bytes FROM conductor_conversations WHERE id = $1`, id).
		Scan(&c.hostAcked, &c.bytes); err != nil {
		return c, fmt.Errorf("conductor: read counters: %w", err)
	}
	return c, nil
}

// enqueue adds an item to the conductor's queue. The number comes from the
// queue's row, which this takes the lock of until commit: see the table's
// comment for why it is not a sequence.
func enqueue(ctx context.Context, q db.Querier, conductorID uuid.UUID, w *WorkItem) (int64, error) {
	var seq, acked int64
	err := q.QueryRow(ctx, `
		INSERT INTO conductor_queues (conductor_id, next_seq) VALUES ($1, 1)
		ON CONFLICT (tenant_id, conductor_id) DO UPDATE SET next_seq = conductor_queues.next_seq + 1
		RETURNING next_seq, acked_seq`, conductorID).Scan(&seq, &acked)
	if err != nil {
		return 0, fmt.Errorf("conductor: number work: %w", err)
	}
	// A stop is exempt: the owner must be able to end a conversation whatever
	// the host's backlog, and a conversation's process is stopped at most once
	// per start, so the stops owed are bounded by the starts the cap allows.
	if seq-acked > MaxQueuedWork && w.Kind != WorkStop {
		return 0, ErrQueueFull
	}
	if _, err := q.Exec(ctx, `
		INSERT INTO conductor_work (conductor_id, seq, kind, conversation_id, message_seq, resume, turn)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		conductorID, seq, string(w.Kind), w.Conversation, w.MessageSeq, w.Resume, w.Turn); err != nil {
		return 0, fmt.Errorf("conductor: queue work: %w", err)
	}
	return seq, nil
}

// requireEnabled takes a share lock on the conductor's row and refuses a
// disabled one. A person's proof of ownership was checked before the
// transaction began, so a disable that commits in between would otherwise let
// a conversation start on a conductor that was just switched off (or whose
// owner just left: removing a member disables their conductors). The lock makes
// the disable wait for this transaction, and then end the conversation it made.
func requireEnabled(ctx context.Context, q db.Querier, conductorID uuid.UUID) error {
	var enabled bool
	err := q.QueryRow(ctx, `SELECT disabled_at IS NULL FROM agents WHERE id = $1 FOR SHARE`, conductorID).Scan(&enabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("read conductor: %w", err)
	}
	if !enabled {
		return agent.ErrIsDisabled
	}
	return nil
}

// lockSessions locks the conductor's live conversations together with id's, in
// id order, and returns the live ones other than id. The host runs one process
// at a time, so the owner opening or reconnecting a session replaces the live
// one rather than being refused: the caller ends what this returns (endAll)
// once it has read, under the lock, whether its own session needs bringing
// back.
//
// Taking them together in id order means two replacements that cross (A
// replaces B while B replaces A) wait for each other instead of deadlocking.
// id may be the zero uuid for a conversation that does not exist yet. A session
// that goes live after this ran is not seen, and the one-live-conversation
// index refuses whichever of the two commits second.
func lockSessions(ctx context.Context, q db.Querier, conductorID, id uuid.UUID) ([]uuid.UUID, error) {
	rows, err := q.Query(ctx, `
		SELECT id FROM conductor_conversations
		 WHERE conductor_id = $1 AND (id = $2 OR state IN ('starting', 'ready'))
		 ORDER BY id FOR UPDATE`, conductorID, id)
	if err != nil {
		return nil, fmt.Errorf("conductor: lock live conversations: %w", err)
	}
	defer rows.Close()
	var others []uuid.UUID
	for rows.Next() {
		var other uuid.UUID
		if err := rows.Scan(&other); err != nil {
			return nil, fmt.Errorf("conductor: scan live conversation: %w", err)
		}
		if other != id {
			others = append(others, other)
		}
	}
	return others, rows.Err()
}

// endAll ends locked conversations that have a process, with the stop queued
// ahead of whatever the caller queues next.
func endAll(ctx context.Context, q db.Querier, conductorID uuid.UUID, ids []uuid.UUID, reason string) error {
	for _, id := range ids {
		if _, err := endConversation(ctx, q, conductorID, id, reason); err != nil {
			return err
		}
	}
	return nil
}

// endConversation stops a locked conversation if it is live: the stop event for
// the transcript and the stop for the host. One that has no process is left as
// it is.
func endConversation(ctx context.Context, q db.Querier, conductorID, id uuid.UUID, reason string) (*Conversation, error) {
	c, err := lockConversation(ctx, q, conductorID, id)
	if err != nil {
		return nil, err
	}
	if !c.State.Live() {
		return c, nil
	}
	counts, err := lockedCounters(ctx, q, id)
	if err != nil {
		return nil, err
	}
	c.State = StateStopped
	if _, err := appendEvent(ctx, q, c, EventStopped, nil, reason, nil); err != nil {
		return nil, err
	}
	if _, err := enqueue(ctx, q, conductorID, &WorkItem{Kind: WorkStop, Conversation: id}); err != nil {
		return nil, err
	}
	if err := saveConversation(ctx, q, c, counts.hostAcked, counts.bytes); err != nil {
		return nil, err
	}
	return c, nil
}

const replacedReason = "replaced by another session"

func (s *PostgresStore) StartConversation(ctx context.Context, p *StartParams) (*Conversation, error) {
	var started *Conversation
	err := s.db.InTx(ctx, func(q db.Querier) error {
		if err := requireEnabled(ctx, q, p.ConductorID); err != nil {
			return err
		}
		live, err := lockSessions(ctx, q, p.ConductorID, uuid.Nil)
		if err != nil {
			return err
		}
		if err := endAll(ctx, q, p.ConductorID, live, replacedReason); err != nil {
			return err
		}
		c, err := scanConversation(q.QueryRow(ctx, `
			INSERT INTO conductor_conversations (id, conductor_id, owner_user_id, state, start_client_id)
			VALUES ($1, $2, $3, 'starting', $4)
			RETURNING `+conversationColumns,
			p.ID, p.ConductorID, p.OwnerUserID, p.ClientID))
		if err != nil {
			return err
		}
		if c.StartSeq, err = enqueue(ctx, q, p.ConductorID, &WorkItem{Kind: WorkStart, Conversation: c.ID}); err != nil {
			return err
		}
		var bytes int64
		if p.FirstMessage != nil {
			e, err := appendEvent(ctx, q, c, EventUser, nil, *p.FirstMessage, p.ClientID)
			if err != nil {
				return err
			}
			if _, err := enqueue(ctx, q, p.ConductorID, &WorkItem{Kind: WorkMessage, Conversation: c.ID, MessageSeq: &e.Seq}); err != nil {
				return err
			}
			bytes = int64(len(*p.FirstMessage))
		}
		if err := saveConversation(ctx, q, c, 0, bytes); err != nil {
			return err
		}
		started = c
		return nil
	})
	switch {
	case pgerr.IsUniqueViolation(err):
		// Either another start replaced the live conversation at the same moment,
		// or a twin of this very start won the race. Which index objected first is
		// the database's order, so look: a twin's conversation is the answer.
		if p.ClientID != nil {
			if c, lookupErr := s.StartedWith(ctx, p.ConductorID, *p.ClientID); lookupErr != nil || c != nil {
				return c, lookupErr
			}
		}
		return nil, ErrBusy
	case err != nil:
		return nil, wrap(err, "start conversation")
	}
	return started, nil
}

// StartedWith is the conversation a start with this client id made, or nil.
func (s *PostgresStore) StartedWith(ctx context.Context, conductorID, clientID uuid.UUID) (*Conversation, error) {
	c, err := scanConversation(s.db.QueryRow(ctx, `
		SELECT `+conversationColumns+` FROM conductor_conversations
		 WHERE conductor_id = $1 AND start_client_id = $2`, conductorID, clientID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("conductor: look up start: %w", err)
	}
	return c, nil
}

// wrap adds what failed to an error. The sentinel errors survive it:
// callers match with errors.Is.
func wrap(err error, what string) error {
	return fmt.Errorf("conductor: %s: %w", what, err)
}

func (s *PostgresStore) AppendUserMessage(ctx context.Context, conductorID, id uuid.UUID, text string, clientID *uuid.UUID, attachmentIDs []uuid.UUID) (*Event, error) {
	var event *Event
	err := s.db.InTx(ctx, func(q db.Querier) error {
		if err := requireEnabled(ctx, q, conductorID); err != nil {
			return err
		}
		others, err := lockSessions(ctx, q, conductorID, id)
		if err != nil {
			return err
		}
		c, err := lockConversation(ctx, q, conductorID, id)
		if err != nil {
			return err
		}
		if clientID != nil {
			// A send repeated after a lost reply replaces nothing, whatever has
			// happened to the conversation since.
			e, err := findByClientID(ctx, q, id, *clientID)
			if err != nil {
				return err
			}
			if e != nil {
				event = e
				return nil
			}
		}
		if err := requireStaged(ctx, q, c, attachmentIDs); err != nil {
			return err
		}
		counts, err := lockedCounters(ctx, q, id)
		if err != nil {
			return err
		}
		if counts.bytes+int64(len(text)) > MaxTranscriptBytes || c.LastSeq >= MaxEventsPerConversation {
			return ErrTranscriptFull
		}
		if !c.State.Live() {
			// The process is gone: bring it back, resuming the same session, and
			// replace the live one.
			if err := endAll(ctx, q, conductorID, others, replacedReason); err != nil {
				return err
			}
			c.State, c.Failure = StateStarting, nil
			if c.StartSeq, err = enqueue(ctx, q, conductorID, &WorkItem{Kind: WorkStart, Conversation: id, Resume: true}); err != nil {
				return err
			}
		}
		e, err := appendEvent(ctx, q, c, EventUser, nil, text, clientID)
		if err != nil {
			return err
		}
		if err := saveAttachmentIDs(ctx, q, c.ID, e.Seq, attachmentIDs); err != nil {
			return err
		}
		if _, err := enqueue(ctx, q, conductorID, &WorkItem{Kind: WorkMessage, Conversation: id, MessageSeq: &e.Seq}); err != nil {
			return err
		}
		if err := saveConversation(ctx, q, c, counts.hostAcked, counts.bytes+int64(len(text))); err != nil {
			return err
		}
		event = e
		return nil
	})
	switch {
	case pgerr.IsUniqueViolation(err):
		return nil, ErrBusy
	case err != nil:
		return nil, wrap(err, "append message")
	}
	return event, nil
}

func findByClientID(ctx context.Context, q db.Querier, id, clientID uuid.UUID) (*Event, error) {
	var e Event
	err := q.QueryRow(ctx, `
		SELECT seq, kind, turn, body, block, created_at FROM conductor_conversation_events
		 WHERE conversation_id = $1 AND client_id = $2`, id, clientID).
		Scan(&e.Seq, &e.Kind, &e.Turn, &e.Body, &e.Block, &e.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("conductor: look up message: %w", err)
	}
	return &e, nil
}

func (s *PostgresStore) DisconnectConversation(ctx context.Context, conductorID, id uuid.UUID, reason string) (*Conversation, error) {
	var ended *Conversation
	err := s.db.InTx(ctx, func(q db.Querier) error {
		c, err := endConversation(ctx, q, conductorID, id, reason)
		ended = c
		return err
	})
	if err != nil {
		return nil, wrap(err, "disconnect conversation")
	}
	return ended, nil
}

func (s *PostgresStore) InterruptTurn(ctx context.Context, conductorID, id uuid.UUID, turn int64) (*Conversation, error) {
	var c *Conversation
	err := s.db.InTx(ctx, func(q db.Querier) error {
		var err error
		if c, err = lockConversation(ctx, q, conductorID, id); err != nil {
			return err
		}
		if !c.State.Live() {
			return ErrNotReplying
		}
		var ended, asked bool
		if err := q.QueryRow(ctx, `
			SELECT coalesce(bool_or(kind = 'turn_end'), false), coalesce(bool_or(kind = 'interrupt_requested'), false)
			  FROM conductor_conversation_events
			 WHERE conversation_id = $1 AND turn = $2 AND kind IN ('turn_end', 'interrupt_requested')`, id, turn).
			Scan(&ended, &asked); err != nil {
			return fmt.Errorf("conductor: read turn: %w", err)
		}
		if ended {
			return ErrNotReplying
		}
		if asked {
			return nil // the host already has it
		}
		if err := requestInterrupt(ctx, q, c, turn); err != nil {
			return err
		}
		return saveLocked(ctx, q, c)
	})
	if err != nil {
		return nil, wrap(err, "interrupt turn")
	}
	return c, nil
}

// requestInterrupt records that the owner asked to stop the turn and queues the
// interrupt, for a locked conversation; the caller saves it.
func requestInterrupt(ctx context.Context, q db.Querier, c *Conversation, turn int64) error {
	// Shared lock, as ApplyHostEvents: a host taking the queue over waits
	// for this to commit, so the work is not queued for a host other than
	// the one whose capabilities were checked.
	var caps []string
	if err := q.QueryRow(ctx, `SELECT host_capabilities FROM conductor_queues WHERE conductor_id = $1 FOR SHARE`, c.ConductorID).
		Scan(&caps); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("conductor: read host capabilities: %w", err)
	}
	if !slices.Contains(caps, CapabilityInterrupt) {
		return ErrHostLacksInterrupt
	}
	if c.LastSeq >= MaxEventsPerConversation+EventReserve {
		return ErrTranscriptFull
	}
	if _, err := appendEvent(ctx, q, c, EventInterruptRequested, &turn, "", nil); err != nil {
		return err
	}
	_, err := enqueue(ctx, q, c.ConductorID, &WorkItem{Kind: WorkInterrupt, Conversation: c.ID, Turn: &turn})
	return err
}

// saveLocked saves a locked conversation whose host_acked and byte count the
// caller did not change.
func saveLocked(ctx context.Context, q db.Querier, c *Conversation) error {
	counts, err := lockedCounters(ctx, q, c.ID)
	if err != nil {
		return err
	}
	return saveConversation(ctx, q, c, counts.hostAcked, counts.bytes)
}

// runningTurn says whether a reply is in flight on a locked conversation, and
// its turn when the host has numbered it (nil while only the owner's message
// has been seen). A conversation with no process has none running.
func runningTurn(ctx context.Context, q db.Querier, c *Conversation) (running bool, turn *int64, err error) {
	if !c.State.Live() {
		return false, nil, nil
	}
	var kind string
	err = q.QueryRow(ctx, `
		SELECT kind, turn FROM conductor_conversation_events
		 WHERE conversation_id = $1 AND kind IN ('user', 'assistant', 'activity', 'turn_end')
		 ORDER BY seq DESC LIMIT 1`, c.ID).Scan(&kind, &turn)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil, nil
	}
	if err != nil {
		return false, nil, fmt.Errorf("conductor: read newest turn: %w", err)
	}
	return kind != string(EventTurnEnd), turn, nil
}

func (s *PostgresStore) QueueContextAction(ctx context.Context, conductorID, id uuid.UUID, kind WorkKind, interrupt bool) (*Conversation, error) {
	var c *Conversation
	err := s.db.InTx(ctx, func(q db.Querier) error {
		var err error
		if c, err = lockConversation(ctx, q, conductorID, id); err != nil {
			return err
		}
		if !c.State.Live() {
			return ErrNoProcess
		}
		if kind == WorkCompact && !c.CanCompact {
			return ErrCannotCompact
		}
		running, turn, err := runningTurn(ctx, q, c)
		if err != nil {
			return err
		}
		if running {
			if !interrupt {
				return ErrTurnRunning
			}
			if turn == nil {
				return &refusal{kind: ErrTurnRunning, detail: "the reply has not started: try again in a moment"}
			}
			var asked bool
			if err := q.QueryRow(ctx, `
				SELECT EXISTS (SELECT 1 FROM conductor_conversation_events
				                WHERE conversation_id = $1 AND turn = $2 AND kind = 'interrupt_requested')`, id, *turn).
				Scan(&asked); err != nil {
				return fmt.Errorf("conductor: read interrupt request: %w", err)
			}
			if !asked {
				if err := requestInterrupt(ctx, q, c, *turn); err != nil {
					return err
				}
			}
		}
		if _, err := enqueue(ctx, q, conductorID, &WorkItem{Kind: kind, Conversation: id}); err != nil {
			return err
		}
		return saveLocked(ctx, q, c)
	})
	if err != nil {
		return nil, wrap(err, "queue "+string(kind))
	}
	return c, nil
}

func (s *PostgresStore) DeleteConversation(ctx context.Context, conductorID, id uuid.UUID, reason string) error {
	var pending bool
	err := s.db.InTx(ctx, func(q db.Querier) error {
		// Ended first, so the host is told to stop the process; the stop is then
		// itself unacknowledged work, and the delete waits for the host's ack
		// rather than erase it (as PruneEndedBatch does).
		if _, err := endConversation(ctx, q, conductorID, id, reason); err != nil {
			return err
		}
		if err := q.QueryRow(ctx, `
			SELECT EXISTS (SELECT 1 FROM conductor_work WHERE conductor_id = $1 AND conversation_id = $2)`,
			conductorID, id).Scan(&pending); err != nil {
			return fmt.Errorf("conductor: read pending work: %w", err)
		}
		if pending {
			return nil // commit the ending
		}
		if _, err := q.Exec(ctx, `DELETE FROM conductor_conversations WHERE id = $1 AND conductor_id = $2`, id, conductorID); err != nil {
			return fmt.Errorf("conductor: delete conversation: %w", err)
		}
		return nil
	})
	if err != nil {
		return wrap(err, "delete conversation")
	}
	if pending {
		return ErrWorkPending
	}
	return nil
}

func (s *PostgresStore) ApplyHostEvents(ctx context.Context, conductorID, hostID, id uuid.UUID, events []HostEvent) (int64, error) {
	var result int64
	err := s.db.InTx(ctx, func(q db.Querier) error {
		c, err := lockConversation(ctx, q, conductorID, id)
		if err != nil {
			return err
		}
		// Shared lock: a host taking the conductor over (ClaimHost's UPDATE) waits
		// for this report to commit, or is seen by it, so a host that was
		// superseded cannot commit events after the new one has taken over.
		var (
			current   *uuid.UUID
			queueNext int64
		)
		if err := q.QueryRow(ctx, `SELECT host_id, next_seq FROM conductor_queues WHERE conductor_id = $1 FOR SHARE`, conductorID).
			Scan(&current, &queueNext); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("conductor: read host: %w", err)
		}
		if current == nil || *current != hostID {
			return ErrHostBusy
		}
		counts, err := lockedCounters(ctx, q, id)
		if err != nil {
			return err
		}
		counts.queueNext = queueNext
		stored := counts.hostAcked
		acked := stored
		for i := range events {
			e := &events[i]
			if e.HostSeq <= acked {
				continue
			}
			if e.HostSeq != acked+1 {
				// What is stored, not what this batch got through: it is rolled back.
				return &GapError{Acked: stored}
			}
			if err := applyHostEvent(ctx, q, c, e, &counts); err != nil {
				return err
			}
			acked = e.HostSeq
		}
		result = acked
		if acked == stored {
			return nil
		}
		return saveConversation(ctx, q, c, acked, counts.bytes)
	})
	if err != nil {
		return 0, wrap(err, "apply host events")
	}
	return result, nil
}

// applyHostEvent applies one reported event to the locked conversation.
//
// A ready or failed that arrives for a conversation that has already ended
// (the owner stopped it while the host was working), or that answers a start
// the conversation has since moved past (the owner stopped and resumed it),
// is consumed and not recorded: the conversation does not come back to life,
// or take a stopped session's word for the new one's state, because the host
// answered late. Assistant text and the end of a turn are kept in any state,
// so the transcript holds what the model said -- up to the byte and event
// caps, past which they are dropped and the loss marked once.
func applyHostEvent(ctx context.Context, q db.Querier, c *Conversation, e *HostEvent, counts *counters) error {
	switch e.Kind {
	case EventAssistant:
		if counts.bytes+int64(len(e.Body)) > MaxTranscriptBytes || c.LastSeq >= MaxEventsPerConversation {
			return markTruncated(ctx, q, c)
		}
		counts.bytes += int64(len(e.Body))
		var block *string
		if e.Block != "" {
			block = &e.Block
		}
		_, err := appendBlockEvent(ctx, q, c, EventAssistant, e.Turn, e.Body, block, nil)
		return err
	case EventActivity:
		return applyActivity(ctx, q, c, e, counts)
	case EventTurnEnd:
		if c.LastSeq >= MaxEventsPerConversation {
			return markTruncated(ctx, q, c)
		}
		if _, err := appendEvent(ctx, q, c, EventTurnEnd, e.Turn, "", nil); err != nil {
			return err
		}
		if !e.Interrupted || c.LastSeq >= MaxEventsPerConversation+EventReserve {
			return nil
		}
		// Kept within the reserve like the state events: the transcript says
		// the owner stopped this turn even when it is otherwise full.
		_, err := appendEvent(ctx, q, c, EventInterrupted, e.Turn, "", nil)
		return err
	case EventUnsupported:
		return recordStateEvent(ctx, q, c, EventUnsupported, e.Body)
	case EventContext:
		if e.ContextTokens != nil {
			c.ContextTokens = *e.ContextTokens
		}
		if e.CanCompact != nil {
			c.CanCompact = *e.CanCompact
		}
		return nil
	case EventContextCleared:
		c.ContextTokens = 0
		return recordStateEvent(ctx, q, c, EventContextCleared, "")
	case EventContextCompacted:
		return recordStateEvent(ctx, q, c, EventContextCompacted, "")
	case EventRepoState:
		return applyRepoState(ctx, q, c, e, counts)
	case EventReady:
		if e.WorkSeq > counts.queueNext {
			return futureWork(e)
		}
		if !c.State.Live() || e.WorkSeq < c.StartSeq {
			return nil
		}
		c.State = StateReady
		return recordStateEvent(ctx, q, c, EventReady, "")
	case EventFailed:
		if e.WorkSeq > counts.queueNext {
			return futureWork(e)
		}
		if !c.State.Live() || e.WorkSeq < c.StartSeq {
			return nil
		}
		c.State, c.Failure = StateFailed, &e.Body
		return recordStateEvent(ctx, q, c, EventFailed, e.Body)
	}
	return invalid("event kind %q", e.Kind)
}

// applyActivity adds a tool call to the transcript. It is the first thing
// dropped when the transcript fills (and is not marked as a loss): it is
// progress, not what the model said.
func applyActivity(ctx context.Context, q db.Querier, c *Conversation, e *HostEvent, counts *counters) error {
	body, err := e.Activity.encode()
	if err != nil {
		return err
	}
	if c.LastSeq >= int64(MaxEventsPerConversation*ActivityEventsShare) || counts.bytes+int64(len(body)) > MaxTranscriptBytes {
		return nil
	}
	counts.bytes += int64(len(body))
	_, err = appendEvent(ctx, q, c, EventActivity, e.Turn, body, nil)
	return err
}

// applyRepoState keeps the newest repo state of the current session on the
// conversation, and adds it
// to the transcript within the same caps as the text and turn ends, past which
// the conversation still shows where the repo is but not how it got there.
func applyRepoState(ctx context.Context, q db.Querier, c *Conversation, e *HostEvent, counts *counters) error {
	if e.WorkSeq > counts.queueNext {
		return futureWork(e)
	}
	body, err := e.Repo.encode()
	if err != nil {
		return err
	}
	// A session that was stopped and resumed is a new one, and its predecessor's
	// last report arriving late says nothing of where the repo is now: it is in
	// the transcript, as the model's text is, and is not the conversation's state.
	if e.WorkSeq >= c.StartSeq {
		c.Repo = e.Repo
	}
	if counts.bytes+int64(len(body)) > MaxTranscriptBytes || c.LastSeq >= MaxEventsPerConversation {
		return nil
	}
	counts.bytes += int64(len(body))
	_, err = appendEvent(ctx, q, c, EventRepoState, e.Turn, body, nil)
	return err
}

// futureWork refuses a report that answers a queue number the server has not
// issued: a host bug, refused whole so that it is seen rather than taken for
// an answer to work that has not yet been done.
func futureWork(e *HostEvent) error {
	return invalid("work_seq %d is not a queue number the server has issued", e.WorkSeq)
}

// markTruncated notes, once, that the transcript stopped keeping what the
// model said.
func markTruncated(ctx context.Context, q db.Querier, c *Conversation) error {
	if c.Truncated {
		return nil
	}
	c.Truncated = true
	_, err := appendEvent(ctx, q, c, EventTruncated, nil, "", nil)
	return err
}

// recordStateEvent adds an event that changes the conversation's state to the
// transcript, within the reserve past the event cap: the state itself is
// applied either way, but a host cannot grow the transcript by repeating it.
func recordStateEvent(ctx context.Context, q db.Querier, c *Conversation, kind EventKind, body string) error {
	if c.LastSeq >= MaxEventsPerConversation+EventReserve {
		return nil
	}
	_, err := appendEvent(ctx, q, c, kind, nil, body, nil)
	return err
}
