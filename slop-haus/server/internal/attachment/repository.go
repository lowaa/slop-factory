package attachment

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
)

// Repository is the attachment tables. Every query runs in a transaction
// scoped to the context's tenant, so row-level security bounds it to that
// tenant whatever the SQL says.
type Repository struct {
	db *db.DB
}

func NewRepository(database *db.DB) *Repository { return &Repository{db: database} }

// The uploader's id is whichever of the two columns the kind names; it is nil
// for a person whose account was deleted.
const attachmentColumns = `tenant_id, id, ticket_id, conversation_id, uploader_kind,
	COALESCE(uploader_user_id, uploader_agent_id),
	filename, content_type, size_bytes, sha256, storage_key, created_at`

func scanAttachment(row pgx.Row) (Attachment, error) {
	var a Attachment
	var kind string
	err := row.Scan(&a.TenantID, &a.ID, &a.TicketID, &a.ConversationID, &kind, &a.UploaderID,
		&a.Filename, &a.ContentType, &a.SizeBytes, &a.SHA256, &a.StorageKey, &a.CreatedAt)
	a.UploaderKind = UploaderKind(kind)
	if errors.Is(err, pgx.ErrNoRows) {
		return Attachment{}, ErrNotFound
	}
	if err != nil {
		return Attachment{}, fmt.Errorf("attachment: read: %w", err)
	}
	return a, nil
}

// TicketExists says whether the context's tenant has the ticket.
func (r *Repository) TicketExists(ctx context.Context, q db.Querier, ticketID int64) (bool, error) {
	var ok bool
	err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM tickets WHERE id = $1)`, ticketID).Scan(&ok)
	return ok, err
}

// ConversationOwner is the person who owns a Remote Control conversation in
// the context's tenant, or ErrNotFound.
func (r *Repository) ConversationOwner(ctx context.Context, q db.Querier, conversationID uuid.UUID) (uuid.UUID, error) {
	var owner *uuid.UUID
	err := q.QueryRow(ctx, `SELECT owner_user_id FROM conductor_conversations WHERE id = $1`, conversationID).Scan(&owner)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, ErrNotFound
	}
	if err != nil {
		return uuid.Nil, err
	}
	// The owner's account was deleted: nobody owns the conversation now.
	if owner == nil {
		return uuid.Nil, ErrNotFound
	}
	return *owner, nil
}

// ConversationConductor is the conductor a Remote Control conversation runs
// on, in the context's tenant, or ErrNotFound.
func (r *Repository) ConversationConductor(ctx context.Context, q db.Querier, conversationID uuid.UUID) (uuid.UUID, error) {
	var conductor uuid.UUID
	err := q.QueryRow(ctx, `SELECT conductor_id FROM conductor_conversations WHERE id = $1`, conversationID).Scan(&conductor)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, ErrNotFound
	}
	return conductor, err
}

// Available is how many more bytes the tenant may store under quota: the
// quota less what its attachments and pending reservations hold.
func (r *Repository) Available(ctx context.Context, q db.Querier, quota int64) (int64, error) {
	var held int64
	err := q.QueryRow(ctx, `
		SELECT COALESCE((SELECT bytes FROM attachment_usage), 0)
		     + COALESCE((SELECT sum(size_bytes) FROM attachment_reservations), 0)`).Scan(&held)
	if err != nil {
		return 0, fmt.Errorf("attachment: read usage: %w", err)
	}
	return max(quota-held, 0), nil
}

// Reserve records key as about to be written, holding size bytes against the
// quota, or fails with ErrQuotaExceeded. The usage row is locked first, so two
// concurrent reservations cannot both fit into the last of the quota.
func (r *Repository) Reserve(ctx context.Context, q db.Querier, key string, size, quota int64) error {
	if _, err := q.Exec(ctx, `INSERT INTO attachment_usage DEFAULT VALUES ON CONFLICT DO NOTHING`); err != nil {
		return fmt.Errorf("attachment: reserve: %w", err)
	}
	var used, reserved int64
	if err := q.QueryRow(ctx, `SELECT bytes FROM attachment_usage FOR UPDATE`).Scan(&used); err != nil {
		return fmt.Errorf("attachment: reserve: %w", err)
	}
	if err := q.QueryRow(ctx, `SELECT COALESCE(sum(size_bytes), 0) FROM attachment_reservations`).Scan(&reserved); err != nil {
		return fmt.Errorf("attachment: reserve: %w", err)
	}
	if used+reserved+size > quota {
		return ErrQuotaExceeded
	}
	if _, err := q.Exec(ctx, `INSERT INTO attachment_reservations (storage_key, size_bytes) VALUES ($1, $2)`, key, size); err != nil {
		return fmt.Errorf("attachment: reserve: %w", err)
	}
	return nil
}

// Insert adds the row of an upload whose object is stored; the database
// refuses it unless the key is still reserved, and clears the reservation.
func (r *Repository) Insert(ctx context.Context, q db.Querier, a *Attachment) (Attachment, error) {
	row := q.QueryRow(ctx, `
		INSERT INTO attachments (id, ticket_id, conversation_id, uploader_kind, uploader_user_id, uploader_agent_id,
			filename, content_type, size_bytes, sha256, storage_key)
		VALUES ($1, $2, $3, $4, CASE WHEN $4 = 'user' THEN $5::uuid END, CASE WHEN $4 = 'agent' THEN $5::uuid END,
			$6, $7, $8, $9, $10)
		RETURNING `+attachmentColumns,
		a.ID, a.TicketID, a.ConversationID, string(a.UploaderKind), a.UploaderID,
		a.Filename, a.ContentType, a.SizeBytes, a.SHA256, a.StorageKey)
	return scanAttachment(row)
}

func (r *Repository) Get(ctx context.Context, q db.Querier, id uuid.UUID) (Attachment, error) {
	return scanAttachment(q.QueryRow(ctx, `SELECT `+attachmentColumns+` FROM attachments WHERE id = $1`, id))
}

func (r *Repository) ListForTicket(ctx context.Context, q db.Querier, ticketID int64) ([]Attachment, error) {
	rows, err := q.Query(ctx, `SELECT `+attachmentColumns+` FROM attachments WHERE ticket_id = $1 ORDER BY created_at, id`, ticketID)
	if err != nil {
		return nil, fmt.Errorf("attachment: list: %w", err)
	}
	defer rows.Close()
	var out []Attachment
	for rows.Next() {
		a, err := scanAttachment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// Delete removes the row, which queues its object (migration trigger).
func (r *Repository) Delete(ctx context.Context, q db.Querier, id uuid.UUID) error {
	tag, err := q.Exec(ctx, `DELETE FROM attachments WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("attachment: delete: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// UsedBytes is what the tenant's attachments hold.
func (r *Repository) UsedBytes(ctx context.Context, q db.Querier) (int64, error) {
	var n int64
	err := q.QueryRow(ctx, `SELECT COALESCE((SELECT bytes FROM attachment_usage), 0)`).Scan(&n)
	return n, err
}

// -- Sweeper --

// QueuedDeletion is an object waiting to be deleted from the store.
type QueuedDeletion struct {
	StorageKey string
	Attempts   int
}

// ExpireReservations queues the objects of reservations older than cutoff and
// drops the reservations, in one statement. A key a live row has is only
// unreserved.
func (r *Repository) ExpireReservations(ctx context.Context, q db.Querier, cutoff time.Time, limit int) (abandoned int, claimed int, err error) {
	err = q.QueryRow(ctx, `
		WITH expired AS (
			DELETE FROM attachment_reservations
			 WHERE storage_key IN (SELECT storage_key FROM attachment_reservations
			                        WHERE reserved_at < $1 ORDER BY reserved_at LIMIT $2 FOR UPDATE SKIP LOCKED)
			RETURNING tenant_id, storage_key
		), queued AS (
			INSERT INTO attachment_delete_queue (tenant_id, storage_key, reason)
			SELECT e.tenant_id, e.storage_key, 'upload_abandoned' FROM expired e
			 WHERE NOT EXISTS (SELECT 1 FROM attachments a WHERE a.tenant_id = e.tenant_id AND a.storage_key = e.storage_key)
			ON CONFLICT (tenant_id, storage_key) DO UPDATE
				SET reason = 'upload_abandoned', queued_at = now(), attempts = 0, last_error = NULL, swept_at = NULL
			RETURNING 1
		)
		SELECT (SELECT count(*) FROM queued), (SELECT count(*) FROM expired)`, cutoff, limit).Scan(&abandoned, &claimed)
	if err != nil {
		return 0, 0, fmt.Errorf("attachment: expire reservations: %w", err)
	}
	return abandoned, claimed, nil
}

// ClaimDeletions locks up to limit queued deletions that are not swept.
func (r *Repository) ClaimDeletions(ctx context.Context, q db.Querier, limit int) ([]QueuedDeletion, error) {
	rows, err := q.Query(ctx, `
		SELECT d.storage_key, d.attempts FROM attachment_delete_queue d
		 WHERE d.swept_at IS NULL ORDER BY d.queued_at, d.storage_key LIMIT $1 FOR UPDATE SKIP LOCKED`, limit)
	if err != nil {
		return nil, fmt.Errorf("attachment: claim deletions: %w", err)
	}
	defer rows.Close()
	var out []QueuedDeletion
	for rows.Next() {
		var d QueuedDeletion
		if err := rows.Scan(&d.StorageKey, &d.Attempts); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// KeyIsLive says whether a row still points at key.
func (r *Repository) KeyIsLive(ctx context.Context, q db.Querier, key string) (bool, error) {
	var live bool
	err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM attachments WHERE storage_key = $1)`, key).Scan(&live)
	return live, err
}

// ClearDeletion drops a queued deletion without touching the store.
func (r *Repository) ClearDeletion(ctx context.Context, q db.Querier, key string) error {
	_, err := q.Exec(ctx, `DELETE FROM attachment_delete_queue WHERE storage_key = $1`, key)
	return err
}

func (r *Repository) MarkSwept(ctx context.Context, q db.Querier, key string, at time.Time) error {
	_, err := q.Exec(ctx, `UPDATE attachment_delete_queue SET swept_at = $2, last_error = NULL WHERE storage_key = $1`, key, at)
	return err
}

func (r *Repository) RecordFailure(ctx context.Context, q db.Querier, key, msg string) error {
	_, err := q.Exec(ctx, `UPDATE attachment_delete_queue SET attempts = attempts + 1, last_error = left($2, 500) WHERE storage_key = $1`, key, msg)
	return err
}

// PruneSwept forgets deletions swept before cutoff.
func (r *Repository) PruneSwept(ctx context.Context, q db.Querier, cutoff time.Time, limit int) (int, error) {
	tag, err := q.Exec(ctx, `
		DELETE FROM attachment_delete_queue WHERE storage_key IN (
			SELECT storage_key FROM attachment_delete_queue WHERE swept_at < $1 LIMIT $2)`, cutoff, limit)
	return int(tag.RowsAffected()), err
}

// DeleteStagedOfMissingConversations removes staged rows whose conversation
// was pruned, which queues their objects.
func (r *Repository) DeleteStagedOfMissingConversations(ctx context.Context, q db.Querier, limit int) (int, error) {
	tag, err := q.Exec(ctx, `
		DELETE FROM attachments WHERE id IN (
			SELECT a.id FROM attachments a
			 WHERE a.ticket_id IS NULL
			   AND NOT EXISTS (SELECT 1 FROM conductor_conversations c WHERE c.tenant_id = a.tenant_id AND c.id = a.conversation_id)
			 LIMIT $1)`, limit)
	return int(tag.RowsAffected()), err
}

// DeleteAll removes up to limit of the tenant's attachments, for a tenant that
// was archived.
func (r *Repository) DeleteAll(ctx context.Context, q db.Querier, limit int) (int, error) {
	tag, err := q.Exec(ctx, `DELETE FROM attachments WHERE id IN (SELECT id FROM attachments LIMIT $1)`, limit)
	return int(tag.RowsAffected()), err
}
