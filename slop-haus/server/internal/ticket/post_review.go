package ticket

import (
	"context"
	"strings"

	"github.com/google/uuid"
)

// MarkPostReviewed is a person recording that they have looked over a done
// ticket's merged change. The comment is optional -- for anything they
// found; a problem worth fixing is a follow-up ticket, filed separately.
//
// Purely a human's bookkeeping: nothing in the claim lifecycle reads it,
// and there is no agent route to it.
func (s *Service) MarkPostReviewed(ctx context.Context, ticketID int64, userID uuid.UUID, comment string) (*Ticket, error) {
	return s.store.MarkPostReviewed(ctx, ticketID, userID, strings.TrimSpace(comment))
}
