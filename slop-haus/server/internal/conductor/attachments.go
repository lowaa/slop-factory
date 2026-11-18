package conductor

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
)

// MaxMessageAttachments is how many staged files one message may carry.
const MaxMessageAttachments = 8

// AttachmentRef is a staged file a message carries, as the host needs it to
// fetch the content and name it for the session.
type AttachmentRef struct {
	ID          uuid.UUID
	Filename    string
	ContentType string
	SizeBytes   int64
}

func validateAttachmentIDs(ids []uuid.UUID) error {
	if len(ids) > MaxMessageAttachments {
		return invalid("a message carries at most %d attachments", MaxMessageAttachments)
	}
	seen := make(map[uuid.UUID]bool, len(ids))
	for _, id := range ids {
		if seen[id] {
			return invalid("an attachment is listed twice")
		}
		seen[id] = true
	}
	return nil
}

// requireStaged refuses ids that are not files the conversation's owner staged
// in this conversation, still unfiled. The query runs in the caller's tenant
// (row-level security), so another tenant's id is not found; one of another
// conversation or person is not found either, and the answer is the same
// for all of them.
func requireStaged(ctx context.Context, q db.Querier, c *Conversation, ids []uuid.UUID) error {
	if len(ids) == 0 {
		return nil
	}
	if c.OwnerUserID == nil {
		return invalid("unknown attachment")
	}
	var found int
	if err := q.QueryRow(ctx, `
		SELECT count(*) FROM attachments
		 WHERE id = ANY($1) AND conversation_id = $2 AND ticket_id IS NULL
		   AND uploader_kind = 'user' AND uploader_user_id = $3`,
		ids, c.ID, *c.OwnerUserID).Scan(&found); err != nil {
		return fmt.Errorf("conductor: check attachments: %w", err)
	}
	if found != len(ids) {
		return invalid("unknown attachment")
	}
	return nil
}

func saveAttachmentIDs(ctx context.Context, q db.Querier, conversation uuid.UUID, seq int64, ids []uuid.UUID) error {
	if len(ids) == 0 {
		return nil
	}
	if _, err := q.Exec(ctx, `
		UPDATE conductor_conversation_events SET attachment_ids = $3
		 WHERE conversation_id = $1 AND seq = $2`, conversation, seq, ids); err != nil {
		return fmt.Errorf("conductor: save attachment ids: %w", err)
	}
	return nil
}

// attachmentRefs fills the work items' attachments, in the order the owner
// added them. One since filed onto a ticket, or deleted, is still named if it
// exists, and left out if it does not: the host then has nothing to fetch.
func (s *PostgresStore) attachmentRefs(ctx context.Context, items []WorkItem, ids [][]uuid.UUID) error {
	var all []uuid.UUID
	for _, l := range ids {
		all = append(all, l...)
	}
	if len(all) == 0 {
		return nil
	}
	rows, err := s.db.Query(ctx, `
		SELECT id, filename, content_type, size_bytes FROM attachments WHERE id = ANY($1)`, all)
	if err != nil {
		return fmt.Errorf("conductor: read attachments: %w", err)
	}
	defer rows.Close()
	byID := map[uuid.UUID]AttachmentRef{}
	for rows.Next() {
		var r AttachmentRef
		if err := rows.Scan(&r.ID, &r.Filename, &r.ContentType, &r.SizeBytes); err != nil {
			return fmt.Errorf("conductor: scan attachment: %w", err)
		}
		byID[r.ID] = r
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("conductor: read attachments: %w", err)
	}
	for i, l := range ids {
		for _, id := range l {
			if r, ok := byID[id]; ok {
				items[i].Attachments = append(items[i].Attachments, r)
			}
		}
	}
	return nil
}
