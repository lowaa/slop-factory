package agent

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
)

// LayoutStore keeps each agent's latest workspace layout report.
type LayoutStore interface {
	// SaveLayout replaces the agent's report.
	SaveLayout(ctx context.Context, agentID uuid.UUID, layout *Layout) error
	// Layouts is every report in the context's tenant, by agent. An agent
	// that never reported has no entry.
	Layouts(ctx context.Context) (map[uuid.UUID]ReportedLayout, error)
}

// PostgresLayoutStore runs every query in the context's tenant; row level
// security hides every other tenant's reports.
type PostgresLayoutStore struct {
	db *db.DB
}

func NewPostgresLayoutStore(database *db.DB) *PostgresLayoutStore {
	return &PostgresLayoutStore{db: database}
}

var _ LayoutStore = (*PostgresLayoutStore)(nil)

func (s *PostgresLayoutStore) SaveLayout(ctx context.Context, agentID uuid.UUID, layout *Layout) error {
	body, err := json.Marshal(layout)
	if err != nil {
		return fmt.Errorf("agent layout: encode: %w", err)
	}
	if _, err := s.db.Exec(ctx, `
		INSERT INTO agent_workspace_layouts (agent_id, layout)
		VALUES ($1, $2)
		ON CONFLICT (tenant_id, agent_id) DO UPDATE
		   SET layout = EXCLUDED.layout, reported_at = now()`, agentID, body); err != nil {
		return fmt.Errorf("agent layout: save: %w", err)
	}
	return nil
}

func (s *PostgresLayoutStore) Layouts(ctx context.Context) (map[uuid.UUID]ReportedLayout, error) {
	rows, err := s.db.Query(ctx, `SELECT agent_id, layout, reported_at FROM agent_workspace_layouts`)
	if err != nil {
		return nil, fmt.Errorf("agent layout: list: %w", err)
	}
	defer rows.Close()

	out := map[uuid.UUID]ReportedLayout{}
	for rows.Next() {
		var id uuid.UUID
		var body []byte
		var r ReportedLayout
		if err := rows.Scan(&id, &body, &r.ReportedAt); err != nil {
			return nil, fmt.Errorf("agent layout: scan: %w", err)
		}
		if err := json.Unmarshal(body, &r.Layout); err != nil {
			return nil, fmt.Errorf("agent layout: decode: %w", err)
		}
		if r.Layout.Repos == nil {
			r.Layout.Repos = []LayoutEntry{}
		}
		out[id] = r
	}
	return out, rows.Err()
}
