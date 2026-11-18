package ticket

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
)

func (s *PostgresStore) EditText(ctx context.Context, id int64, editor Author, params *EditParams) (*EditResult, error) {
	if !editor.valid() {
		return nil, ErrInvalidAuthor
	}
	result := &EditResult{}
	err := s.db.InTx(ctx, func(q db.Querier) error {
		var err error
		if result.Changed, err = editText(ctx, q, id, editor, params); err != nil {
			return err
		}
		result.Ticket, err = loadTicket(ctx, q, id)
		return err
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// EditHeld resolves the agent's ticket from its lease inside the edit's own
// transaction, and under the claim lock like every other held-ticket
// operation: a human moving the ticket away revokes the lease under that
// lock, so an edit cannot land on a ticket the agent has just lost.
func (s *PostgresStore) EditHeld(ctx context.Context, agentID uuid.UUID, params *EditParams) (*EditResult, error) {
	result := &EditResult{}
	err := s.withHeldLease(ctx, agentID, func(q db.Querier, _, ticketID int64) error {
		var err error
		if result.Changed, err = editText(ctx, q, ticketID, AgentAuthor(agentID), params); err != nil {
			return err
		}
		result.Ticket, err = loadTicket(ctx, q, ticketID)
		return err
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// editBarrier is a test-only seam, like depsBarrier. Production leaves it
// nil. It sits between reading the current text and writing the new, the
// window in which a second edit must not be able to read the same "old".
var editBarrier func()

// editText is the one place a title or body is written after creation, and
// it always writes the revision with it.
//
// The row lock is what makes the revision's old text true: without it, two
// concurrent edits would each record the same "old" value, and the history
// would show one of them changing text it never saw. A field whose new
// value equals the current one is not a change, and an edit with no change
// records nothing.
func editText(ctx context.Context, q db.Querier, id int64, editor Author, params *EditParams) (bool, error) {
	var title, body string
	var revision int
	err := q.QueryRow(ctx,
		`SELECT title, body, revision FROM tickets WHERE id = $1 FOR UPDATE`, id).Scan(&title, &body, &revision)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, fmt.Errorf("%w: #%d", ErrNotFound, id)
	}
	if err != nil {
		return false, fmt.Errorf("read ticket text: %w", err)
	}
	if editBarrier != nil {
		editBarrier()
	}
	if params.BaseRevision != nil && *params.BaseRevision != revision {
		return false, fmt.Errorf("%w: #%d is at revision %d, the edit was made against %d",
			ErrStaleRevision, id, revision, *params.BaseRevision)
	}

	var oldTitle, newTitle, oldBody, newBody *string
	if params.Title != nil && *params.Title != title {
		oldTitle, newTitle = &title, params.Title
	}
	if params.Body != nil && *params.Body != body {
		oldBody, newBody = &body, params.Body
	}
	if newTitle == nil && newBody == nil {
		return false, nil
	}

	revision++
	if _, err := q.Exec(ctx, `
		UPDATE tickets
		   SET title = COALESCE($2, title), body = COALESCE($3, body),
		       revision = $4, updated_at = now()
		 WHERE id = $1`, id, newTitle, newBody, revision); err != nil {
		return false, fmt.Errorf("edit ticket: %w", err)
	}
	if _, err := q.Exec(ctx, `
		INSERT INTO ticket_revisions
		       (ticket_id, revision, editor_agent_id, editor_user_id, old_title, new_title, old_body, new_body)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		id, revision, editor.AgentID, editor.UserID, oldTitle, newTitle, oldBody, newBody); err != nil {
		return false, fmt.Errorf("record revision: %w", err)
	}
	return true, nil
}

// ListRevisions returns a ticket's revisions, oldest first. A ticket that
// does not exist is ErrNotFound rather than an empty history, so the API
// answers 404 the way it does for the ticket itself.
func (s *PostgresStore) ListRevisions(ctx context.Context, ticketID int64) ([]Revision, error) {
	var revisions []Revision
	err := s.db.InReadTx(ctx, func(q db.Querier) error {
		var exists bool
		if err := q.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM tickets WHERE id = $1)`, ticketID).Scan(&exists); err != nil {
			return fmt.Errorf("find ticket: %w", err)
		}
		if !exists {
			return fmt.Errorf("%w: #%d", ErrNotFound, ticketID)
		}
		rows, err := q.Query(ctx, `
			SELECT id, ticket_id, revision, editor_agent_id, editor_user_id, editor_user_id_deleted,
			       old_title, new_title, old_body, new_body, created_at
			  FROM ticket_revisions WHERE ticket_id = $1 ORDER BY revision`, ticketID)
		if err != nil {
			return fmt.Errorf("list revisions: %w", err)
		}
		revisions, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (Revision, error) {
			var r Revision
			err := row.Scan(&r.ID, &r.TicketID, &r.Number, &r.Editor.AgentID, &r.Editor.UserID, &r.EditorDeleted,
				&r.OldTitle, &r.NewTitle, &r.OldBody, &r.NewBody, &r.CreatedAt)
			return r, err
		})
		if err != nil {
			return fmt.Errorf("scan revision: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return revisions, nil
}
