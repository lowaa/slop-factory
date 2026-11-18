package landing

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// PostgresMemberHandBack is MemberHandBack over the ticket's attempt comments
// and the ticket store.
// A member without a lease has nothing to end: it is queued, and once its
// landing row is handed back its author may take it, as after ServerHandBack.
// Unlike that, no first-claim window is written for the author.
type PostgresMemberHandBack struct {
	db      *db.DB
	tickets *ticket.PostgresStore
}

func NewPostgresMemberHandBack(database *db.DB) *PostgresMemberHandBack {
	return &PostgresMemberHandBack{db: database, tickets: ticket.NewPostgresStore(database)}
}

func (m *PostgresMemberHandBack) Comment(ctx context.Context, server uuid.UUID, row *LandingRow, guidance string) error {
	_, err := ticket.InsertAttemptComment(ctx, m.db, &ticket.AttemptRef{TicketID: row.TicketID, Number: row.AttemptNumber}, ticket.AgentAuthor(server), guidance)
	return err
}

func (m *PostgresMemberHandBack) Requeue(ctx context.Context, server uuid.UUID, row *LandingRow) error {
	return m.tickets.ServerRequeueMember(ctx, server, &ticket.AttemptRef{TicketID: row.TicketID, Number: row.AttemptNumber})
}

var _ MemberHandBack = (*PostgresMemberHandBack)(nil)

// BatchRecovery reads what a restarted runner needs about a member that has
// left the queue.
type BatchRecovery interface {
	// MergeSHA is the merge commit the ticket's merged change got on the base;
	// empty when it is not merged or no merge commit was recorded.
	MergeSHA(ctx context.Context, ticketID int64) (string, error)
}

// PostgresBatchRecovery is BatchRecovery over the change store's table.
type PostgresBatchRecovery struct {
	db *db.DB
}

func NewPostgresBatchRecovery(database *db.DB) *PostgresBatchRecovery {
	return &PostgresBatchRecovery{db: database}
}

func (p *PostgresBatchRecovery) MergeSHA(ctx context.Context, ticketID int64) (string, error) {
	var sha string
	err := p.db.QueryRow(ctx, `
SELECT coalesce(merge_sha, '') FROM ticket_changes WHERE ticket_id = $1 AND merged_at IS NOT NULL ORDER BY merged_at DESC LIMIT 1`, ticketID).Scan(&sha)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("landing: read a merge commit: %w", err)
	}
	return sha, nil
}

var _ BatchRecovery = (*PostgresBatchRecovery)(nil)
