// Package attemptverdict keeps a person's verdict on each finished attempt:
// would they merge it. It sits beside the attempt's signals, never in them.
package attemptverdict

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// Verdict values, as stored.
const (
	WouldMerge            = "would_merge"
	WouldMergeWithChanges = "would_merge_with_changes"
	WouldNotMerge         = "would_not_merge"
)

var (
	ErrInvalidVerdict = errors.New("attemptverdict: verdict must be would_merge, would_merge_with_changes or would_not_merge")
	ErrNoteTooLong    = errors.New("attemptverdict: the note is too long")
	ErrNotFinished    = errors.New("attemptverdict: the attempt has not finished")
	ErrNoVerdict      = errors.New("attemptverdict: the attempt has no verdict")
)

// MaxNoteRunes bounds a note; the table's check bounds it in characters too.
const MaxNoteRunes = 1000

func validVerdict(v string) bool {
	return v == WouldMerge || v == WouldMergeWithChanges || v == WouldNotMerge
}

// Store keeps verdicts append-only: the newest row of an attempt is its
// current verdict. Row-level security scopes every read and write to the
// caller's tenant.
type Store struct {
	db *db.DB
}

func NewStore(database *db.DB) *Store { return &Store{db: database} }

var _ ticket.AttemptVerdicts = (*Store)(nil)

// Set appends the person's verdict on a finished attempt and returns the
// verdict it replaced ("" for none).
func (s *Store) Set(ctx context.Context, ref ticket.AttemptRef, verdict, note string, by uuid.UUID) (previous string, err error) {
	err = s.db.InTx(ctx, func(q db.Querier) error {
		previous = ""
		subject := fmt.Sprintf("%d/%d", ref.TicketID, ref.Number)
		if err := db.TakeSubjectAdvisoryLock(ctx, q, db.AttemptVerdictLockKey, subject); err != nil {
			return err
		}
		var finished bool
		err := q.QueryRow(ctx, `
			SELECT status IN `+ticket.AttemptFinishedStatusesSQL+` FROM attempts WHERE ticket_id = $1 AND number = $2`,
			ref.TicketID, ref.Number).Scan(&finished)
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: #%d attempt %d", ticket.ErrNoSuchAttempt, ref.TicketID, ref.Number)
		}
		if err != nil {
			return fmt.Errorf("attemptverdict: attempt: %w", err)
		}
		if !finished {
			return ErrNotFinished
		}
		err = q.QueryRow(ctx, `
			SELECT verdict FROM attempt_verdicts WHERE ticket_id = $1 AND attempt_number = $2
			 ORDER BY id DESC LIMIT 1`, ref.TicketID, ref.Number).Scan(&previous)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("attemptverdict: previous: %w", err)
		}
		if _, err := q.Exec(ctx, `
			INSERT INTO attempt_verdicts (ticket_id, attempt_number, verdict, note, set_by_user_id)
			VALUES ($1, $2, $3, $4, $5)`, ref.TicketID, ref.Number, verdict, note, by); err != nil {
			return fmt.Errorf("attemptverdict: write: %w", err)
		}
		return nil
	})
	return previous, err
}

// AttemptVerdicts reads the current verdict and the history of each attempt;
// attempts nobody judged are absent. It is also what the cross-ticket
// comparison reads for a set of (ticket, attempt) pairs.
func (s *Store) AttemptVerdicts(ctx context.Context, refs []ticket.AttemptRef) (map[ticket.AttemptRef]*ticket.AttemptVerdictBody, error) {
	out := map[ticket.AttemptRef]*ticket.AttemptVerdictBody{}
	if len(refs) == 0 {
		return out, nil
	}
	tickets := make([]int64, len(refs))
	numbers := make([]int32, len(refs))
	for i, r := range refs {
		tickets[i], numbers[i] = r.TicketID, int32(r.Number)
	}
	err := s.db.InReadTx(ctx, func(q db.Querier) error {
		rows, err := q.Query(ctx, `
			SELECT ticket_id, attempt_number, verdict, note, set_by_user_id, set_by_user_id_deleted, set_at
			  FROM attempt_verdicts
			 WHERE (ticket_id, attempt_number) IN (SELECT * FROM unnest($1::bigint[], $2::int[]))
			 ORDER BY id DESC`, tickets, numbers)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var (
				ref     ticket.AttemptRef
				number  int32
				entry   ticket.AttemptVerdictEntry
				by      *uuid.UUID
				setAt   time.Time
				deleted bool
			)
			if err := rows.Scan(&ref.TicketID, &number, &entry.Verdict, &entry.Note, &by, &deleted, &setAt); err != nil {
				return err
			}
			ref.Number = int(number)
			entry.SetAt, entry.SetByUserDeleted = setAt, deleted
			if by != nil {
				id := by.String()
				entry.SetByUserID = &id
			}
			// Newest first: the first row of an attempt is its current verdict.
			if cur := out[ref]; cur != nil {
				cur.History = append(cur.History, entry)
			} else {
				out[ref] = &ticket.AttemptVerdictBody{AttemptVerdictEntry: entry, History: []ticket.AttemptVerdictEntry{}}
			}
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("attemptverdict: read: %w", err)
	}
	return out, nil
}
