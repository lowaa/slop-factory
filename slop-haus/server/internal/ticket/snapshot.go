package ticket

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

// SnapshotSource is how a snapshot came to be.
type SnapshotSource string

const (
	// SnapshotFirstClaim was taken when the ticket's first attempt started.
	SnapshotFirstClaim SnapshotSource = "first_claim"
	// SnapshotRebuilt was worked out afterwards for a ticket from before
	// snapshots, from its revisions and its comments' timestamps.
	SnapshotRebuilt SnapshotSource = "rebuilt"
	// SnapshotRefrozen was re-taken from the ticket as it is now, by a person.
	SnapshotRefrozen SnapshotSource = "refrozen"
)

var (
	// ErrNoSnapshot is a read of a ticket that has none: it is not replayable.
	ErrNoSnapshot = errors.New("ticket: the ticket has no snapshot")
	// ErrSnapshotExists is a rebuild of a ticket that already has one. A
	// snapshot is replaced only by freezing the ticket now.
	ErrSnapshotExists = errors.New("ticket: the ticket already has a snapshot")
	// ErrNotParked is a snapshot taken by hand of a ticket that is not parked.
	ErrNotParked = errors.New("ticket: only a parked ticket is snapshotted by hand")
)

// Snapshot is the ticket as an attempt first saw it.
type Snapshot struct {
	ID       int64
	TicketID int64
	Revision int
	Title    string
	Body     string
	Paths    []model.Path
	// CommentsThroughID is the newest ticket-level comment in the spec
	// conversation; 0 for none.
	CommentsThroughID int64
	// AsOf is the moment the snapshot shows the ticket at; TakenAt is when it
	// was written.
	AsOf, TakenAt time.Time
	Source        SnapshotSource
	// TakenBy is the person who froze the ticket, for SnapshotRefrozen.
	TakenBy        *uuid.UUID
	TakenByDeleted bool
}

const snapshotColumns = `id, ticket_id, revision, title, body, paths, comments_through_id, as_of, taken_at, source,
	taken_by_user_id, taken_by_user_id_deleted`

// snapshotAsOf is the INSERT ... SELECT that writes a snapshot of ticket $1
// as it stood at $2. Titles and bodies are walked back through ticket_revisions
// rather than read from the ticket: with no edit after $2 that is the live
// text, and with one it is the text the first later edit replaced. Paths are
// not recorded with a history, so the ones the ticket has now that existed by
// then stand in; a path since removed is lost.
//
// $3 is the source and $4 the person, if any.
const snapshotAsOf = `
	INSERT INTO ticket_snapshots (ticket_id, revision, title, body, paths, comments_through_id, as_of, source, taken_by_user_id)
	SELECT t.id,
	       COALESCE((SELECT max(r.revision) FROM ticket_revisions r WHERE r.ticket_id = t.id AND r.created_at <= $2), 0),
	       COALESCE((SELECT r.new_title FROM ticket_revisions r
	                  WHERE r.ticket_id = t.id AND r.created_at <= $2 AND r.new_title IS NOT NULL
	                  ORDER BY r.revision DESC LIMIT 1),
	                (SELECT r.old_title FROM ticket_revisions r
	                  WHERE r.ticket_id = t.id AND r.new_title IS NOT NULL
	                  ORDER BY r.revision LIMIT 1), t.title),
	       COALESCE((SELECT r.new_body FROM ticket_revisions r
	                  WHERE r.ticket_id = t.id AND r.created_at <= $2 AND r.new_body IS NOT NULL
	                  ORDER BY r.revision DESC LIMIT 1),
	                (SELECT r.old_body FROM ticket_revisions r
	                  WHERE r.ticket_id = t.id AND r.new_body IS NOT NULL
	                  ORDER BY r.revision LIMIT 1), t.body),
	       COALESCE((SELECT jsonb_agg(jsonb_build_object('path', p.path, 'source', p.source) ORDER BY p.path)
	                   FROM ticket_paths p WHERE p.ticket_id = t.id AND p.created_at <= $2), '[]'),
	       COALESCE((SELECT max(c.id) FROM ticket_comments c
	                  WHERE c.ticket_id = t.id AND c.attempt_number IS NULL AND c.created_at <= $2), 0),
	       $2, $3, $4
	  FROM tickets t WHERE t.id = $1
	RETURNING ` + snapshotColumns

func scanSnapshot(row pgx.Row) (*Snapshot, error) {
	var s Snapshot
	var paths []byte
	var source string
	if err := row.Scan(&s.ID, &s.TicketID, &s.Revision, &s.Title, &s.Body, &paths, &s.CommentsThroughID,
		&s.AsOf, &s.TakenAt, &source, &s.TakenBy, &s.TakenByDeleted); err != nil {
		return nil, err
	}
	var stored []struct {
		Path   string           `json:"path"`
		Source model.PathSource `json:"source"`
	}
	if err := json.Unmarshal(paths, &stored); err != nil {
		return nil, fmt.Errorf("decode snapshot paths: %w", err)
	}
	s.Paths = make([]model.Path, 0, len(stored))
	for _, p := range stored {
		s.Paths = append(s.Paths, model.Path{Path: p.Path, Source: p.Source})
	}
	s.Source = SnapshotSource(source)
	return &s, nil
}

// takeClaimSnapshot snapshots the ticket as an attempt starts, unless it has
// one already or any attempt has started: the original attempt's first claim
// wins, so a sibling or a later claim never takes a snapshot that already
// shows what was learnt since. A replay never takes one, as it is shown
// the ticket's. The caller holds the ticket's row.
func takeClaimSnapshot(ctx context.Context, q db.Querier, attempt *AttemptRef) error {
	var replay, skip bool
	if err := q.QueryRow(ctx, `
		SELECT (SELECT replay FROM attempts WHERE ticket_id = $1 AND number = $2),
		       EXISTS (SELECT 1 FROM ticket_snapshots WHERE ticket_id = $1)
		    OR EXISTS (SELECT 1 FROM attempts WHERE ticket_id = $1 AND started_revision IS NOT NULL)`,
		attempt.TicketID, attempt.Number).Scan(&replay, &skip); err != nil {
		return fmt.Errorf("check for a snapshot: %w", err)
	}
	if replay || skip {
		return nil
	}
	if _, err := scanSnapshot(q.QueryRow(ctx, snapshotAsOf, attempt.TicketID, time.Now(), SnapshotFirstClaim, nil)); err != nil {
		return fmt.Errorf("take snapshot: %w", err)
	}
	return nil
}

// Snapshot is the ticket's current snapshot, or ErrNoSnapshot.
func (s *PostgresStore) Snapshot(ctx context.Context, ticketID int64) (*Snapshot, error) {
	return latestSnapshot(ctx, s.db, ticketID)
}

func latestSnapshot(ctx context.Context, q db.Querier, ticketID int64) (*Snapshot, error) {
	snap, err := scanSnapshot(q.QueryRow(ctx,
		`SELECT `+snapshotColumns+` FROM ticket_snapshots WHERE ticket_id = $1 ORDER BY id DESC LIMIT 1`, ticketID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("%w: #%d", ErrNoSnapshot, ticketID)
	}
	return snap, err
}

// lockParked locks the ticket and checks it is parked, returning when it
// last became so.
func lockParked(ctx context.Context, q db.Querier, ticketID int64) (time.Time, error) {
	var status string
	err := q.QueryRow(ctx, `SELECT status FROM tickets WHERE id = $1 FOR UPDATE`, ticketID).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, fmt.Errorf("%w: #%d", ErrNotFound, ticketID)
	}
	if err != nil {
		return time.Time{}, fmt.Errorf("lock ticket: %w", err)
	}
	if Status(status) != StatusParked {
		return time.Time{}, fmt.Errorf("%w: #%d is %s", ErrNotParked, ticketID, status)
	}
	var parkedAt time.Time
	if err := q.QueryRow(ctx, `
		SELECT changed_at FROM ticket_status_changes
		 WHERE ticket_id = $1 AND to_status = 'parked' ORDER BY id DESC LIMIT 1`, ticketID).Scan(&parkedAt); err != nil {
		return time.Time{}, fmt.Errorf("find when the ticket was parked: %w", err)
	}
	return parkedAt, nil
}

// SnapshotParked rebuilds the snapshot of a parked ticket that has none, as
// the ticket stood when it was parked. The time its base commit was set is
// not recorded, so a base set after parking is not reflected: use FreezeNow
// when the ticket has been changed since.
func (s *PostgresStore) SnapshotParked(ctx context.Context, ticketID int64) (*Snapshot, error) {
	var snap *Snapshot
	err := s.db.InTx(ctx, func(q db.Querier) error {
		parkedAt, err := lockParked(ctx, q, ticketID)
		if err != nil {
			return err
		}
		if _, err := latestSnapshot(ctx, q, ticketID); err == nil {
			return fmt.Errorf("%w: #%d", ErrSnapshotExists, ticketID)
		} else if !errors.Is(err, ErrNoSnapshot) {
			return err
		}
		snap, err = scanSnapshot(q.QueryRow(ctx, snapshotAsOf, ticketID, parkedAt, SnapshotRebuilt, nil))
		return err
	})
	if err != nil {
		return nil, err
	}
	return snap, nil
}

// FreezeNow re-takes the snapshot of a parked ticket from the ticket as it is
// now. The earlier snapshots stay, so the history shows who re-froze it and
// when.
func (s *PostgresStore) FreezeNow(ctx context.Context, ticketID int64, by uuid.UUID) (*Snapshot, error) {
	var snap *Snapshot
	err := s.db.InTx(ctx, func(q db.Querier) error {
		if _, err := lockParked(ctx, q, ticketID); err != nil {
			return err
		}
		var err error
		snap, err = scanSnapshot(q.QueryRow(ctx, snapshotAsOf, ticketID, time.Now(), SnapshotRefrozen, by))
		return err
	})
	if err != nil {
		return nil, err
	}
	return snap, nil
}

// attemptConversation is the conversation an attempt is shown: its own, with
// the ticket's as the snapshot froze it when the attempt is a replay.
func attemptConversation(ctx context.Context, q db.Querier, ref *AttemptRef) (Conversation, error) {
	conv := Conversation{Attempt: &ref.Number}
	if err := q.QueryRow(ctx, `SELECT replay FROM attempts WHERE ticket_id = $1 AND number = $2`,
		ref.TicketID, ref.Number).Scan(&conv.Replay); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return Conversation{}, fmt.Errorf("read the attempt: %w", err)
	}
	return conv, nil
}

// frozen shows t as its snapshot did.
func (snap *Snapshot) frozen(t *Ticket) {
	t.Title, t.Body, t.Paths, t.Revision = snap.Title, snap.Body, snap.Paths, snap.Revision
}
