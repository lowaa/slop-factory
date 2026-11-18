package conductor

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/lowaa/slop-factory/go-db/pgerr"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
)

func (s *PostgresStore) HostStatus(ctx context.Context, conductorID uuid.UUID, ttl time.Duration) (HostStatus, error) {
	var seen *bool
	st := HostStatus{Capabilities: []string{}}
	err := s.db.QueryRow(ctx, `
		SELECT host_seen_at >= now() - make_interval(secs => $2), host_capabilities, host_version
		  FROM conductor_queues WHERE conductor_id = $1`, conductorID, ttl.Seconds()).Scan(&seen, &st.Capabilities, &st.Version)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return HostStatus{}, fmt.Errorf("conductor: read host status: %w", err)
	}
	st.Online = seen != nil && *seen
	return st, nil
}

func (s *PostgresStore) ClaimHost(ctx context.Context, conductorID, hostID uuid.UUID, ack int64, ttl time.Duration, report HostReport) (int64, error) {
	var acked int64
	err := s.db.InTx(ctx, func(q db.Querier) error {
		var next int64
		err := q.QueryRow(ctx, `
			INSERT INTO conductor_queues (conductor_id, host_id, host_seen_at, host_capabilities, host_version) VALUES ($1, $2, now(), $4, $5)
			ON CONFLICT (tenant_id, conductor_id) DO UPDATE
			   SET host_id = EXCLUDED.host_id, host_seen_at = now(),
			       host_capabilities = EXCLUDED.host_capabilities, host_version = EXCLUDED.host_version
			 WHERE conductor_queues.host_id IS NULL
			    OR conductor_queues.host_id = EXCLUDED.host_id
			    OR conductor_queues.host_seen_at < now() - make_interval(secs => $3)
			RETURNING next_seq`, conductorID, hostID, ttl.Seconds(), report.Capabilities, report.Version).Scan(&next)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrHostBusy
		}
		if err != nil {
			return fmt.Errorf("take the queue: %w", err)
		}
		// Clamped to what exists, so a host cannot acknowledge the future and
		// have work not yet queued discarded as handled.
		if err := q.QueryRow(ctx, `
			UPDATE conductor_queues SET acked_seq = LEAST(GREATEST(acked_seq, $2), next_seq)
			 WHERE conductor_id = $1 RETURNING acked_seq`, conductorID, ack).Scan(&acked); err != nil {
			return fmt.Errorf("conductor: acknowledge work: %w", err)
		}
		if _, err := q.Exec(ctx, `DELETE FROM conductor_work WHERE conductor_id = $1 AND seq <= $2`, conductorID, acked); err != nil {
			return fmt.Errorf("conductor: delete handled work: %w", err)
		}
		return nil
	})
	switch {
	case pgerr.IsForeignKeyViolation(err):
		// The conductor is not in the context's tenant.
		return 0, ErrNotFound
	case err != nil:
		return 0, wrap(err, "claim host")
	}
	return acked, nil
}

func (s *PostgresStore) ReleaseHost(ctx context.Context, conductorID, hostID uuid.UUID) error {
	// Gated on the host in the statement itself, so a host that has already
	// been taken over cannot release its successor. The queue and its
	// acknowledgements stay: what the next host is owed is unchanged.
	if _, err := s.db.Exec(ctx, `
		UPDATE conductor_queues SET host_id = NULL, host_seen_at = NULL
		 WHERE conductor_id = $1 AND host_id = $2`, conductorID, hostID); err != nil {
		return fmt.Errorf("conductor: release host: %w", err)
	}
	return nil
}

func (s *PostgresStore) ReadWork(ctx context.Context, conductorID, hostID uuid.UUID, limit int) ([]WorkItem, error) {
	// One statement, gated on the host being the conductor's current one as it
	// runs: a poll that was waiting when another host took over is handed
	// nothing. (What was delivered a moment before the takeover may still be
	// acted on; delivery is at least once, and the new host is given it again.)
	rows, err := s.db.Query(ctx, `
		SELECT w.seq, w.kind, w.conversation_id, w.resume, w.message_seq, coalesce(e.body, ''), w.turn, coalesce(e.attachment_ids, '{}')
		  FROM conductor_work w
		  JOIN conductor_queues q ON q.tenant_id = w.tenant_id AND q.conductor_id = w.conductor_id
		  LEFT JOIN conductor_conversation_events e
		         ON e.tenant_id = w.tenant_id AND e.conversation_id = w.conversation_id AND e.seq = w.message_seq
		 WHERE w.conductor_id = $1 AND w.seq > q.acked_seq AND q.host_id = $3
		 ORDER BY w.seq LIMIT $2`, conductorID, limit, hostID)
	if err != nil {
		return nil, fmt.Errorf("conductor: read work: %w", err)
	}
	defer rows.Close()
	out := []WorkItem{}
	var attachmentIDs [][]uuid.UUID
	for rows.Next() {
		var w WorkItem
		var ids []uuid.UUID
		if err := rows.Scan(&w.Seq, &w.Kind, &w.Conversation, &w.Resume, &w.MessageSeq, &w.Text, &w.Turn, &ids); err != nil {
			return nil, fmt.Errorf("conductor: scan work: %w", err)
		}
		out = append(out, w)
		attachmentIDs = append(attachmentIDs, ids)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("conductor: read work: %w", err)
	}
	if len(out) > 0 {
		rows.Close()
		if err := s.attachmentRefs(ctx, out, attachmentIDs); err != nil {
			return nil, err
		}
		return out, nil
	}
	// Nothing: either there is no work, or the host is not the current one.
	var current *uuid.UUID
	if err := s.db.QueryRow(ctx, `SELECT host_id FROM conductor_queues WHERE conductor_id = $1`, conductorID).Scan(&current); err != nil &&
		!errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("conductor: read host: %w", err)
	}
	if current == nil || *current != hostID {
		return nil, ErrHostBusy
	}
	return out, nil
}
