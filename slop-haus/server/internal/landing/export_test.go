package landing

import (
	"context"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
)

// MoveForTest is a bare guarded move that writes only the phase.
func MoveForTest(ctx context.Context, q db.Querier, ticketID int64, from, to Phase, cause Cause) error {
	return transition(ctx, q, &move{TicketID: ticketID, From: from, To: to, Cause: cause})
}
