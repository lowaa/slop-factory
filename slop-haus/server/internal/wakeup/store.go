package wakeup

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
)

type PostgresStore struct {
	db *db.DB
}

func NewPostgresStore(database *db.DB) *PostgresStore {
	return &PostgresStore{db: database}
}

var _ PollRecorder = (*PostgresStore)(nil)

// RecordPoll notes that the agent's supervisor polled just now. One row per
// agent, overwritten: see the migration. now() is the transaction's start, so
// overlapping polls can commit out of order; clock_timestamp() with GREATEST
// keeps the stored time from ever moving backwards.
func (s *PostgresStore) RecordPoll(ctx context.Context, agentID uuid.UUID, version string, protocol int) error {
	if _, err := s.db.Exec(ctx, `
		INSERT INTO worker_polls (agent_id, version, protocol, last_polled_at) VALUES ($1, $2, $3, clock_timestamp())
		ON CONFLICT (tenant_id, agent_id) DO UPDATE
		SET last_polled_at = GREATEST(worker_polls.last_polled_at, clock_timestamp()), version = EXCLUDED.version, protocol = EXCLUDED.protocol`,
		agentID, version, protocol); err != nil {
		return fmt.Errorf("record worker poll: %w", err)
	}
	return nil
}
