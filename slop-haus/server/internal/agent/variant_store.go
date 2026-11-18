package agent

import (
	"context"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
)

// VariantWriter sets the variant an agent is on. It takes the caller's
// querier because variant assigns inside its own transaction, under the
// agent row lock it already holds.
type VariantWriter struct{}

// SetVariant puts the agent on a version, or off any with a nil versionID.
func (VariantWriter) SetVariant(ctx context.Context, q db.Querier, agentID uuid.UUID, versionID *uuid.UUID, revision int64) error {
	if versionID == nil {
		_, err := q.Exec(ctx, `UPDATE agents SET variant_id = NULL, variant_revision = NULL WHERE id = $1`, agentID)
		return err
	}
	_, err := q.Exec(ctx, `UPDATE agents SET variant_id = $2, variant_revision = $3 WHERE id = $1`, agentID, *versionID, revision)
	return err
}
