package ticket

import (
	"context"
	"time"
)

// AttemptVerdicts reads the verdicts people gave their attempts. The
// attemptverdict package implements it; it imports this one, so the handler
// takes the slice it needs.
type AttemptVerdicts interface {
	// AttemptVerdicts returns each attempt's current verdict and the ones it
	// replaced; an attempt nobody judged is absent.
	AttemptVerdicts(ctx context.Context, refs []AttemptRef) (map[AttemptRef]*AttemptVerdictBody, error)
}

// AttemptVerdictBody is a person's verdict on a finished attempt, with the
// ones it replaced. It is kept apart from the attempt's signals: human
// judgement and automated figures are never combined.
type AttemptVerdictBody struct {
	AttemptVerdictEntry
	// History is the verdicts this one replaced, newest first.
	History []AttemptVerdictEntry `json:"history"`
}

// AttemptVerdictEntry is one verdict a person gave.
type AttemptVerdictEntry struct {
	Verdict          string    `json:"verdict" enum:"would_merge,would_merge_with_changes,would_not_merge"`
	Note             string    `json:"note"`
	SetByUserID      *string   `json:"set_by_user_id"`
	SetByUserDeleted bool      `json:"set_by_user_id_deleted,omitempty" doc:"true when the person's account was deleted; the id is then null"`
	SetAt            time.Time `json:"set_at"`
}

// WithAttemptVerdicts serves each attempt's verdict on the ticket detail, to
// people only: a worker never sees what is said of its own work.
func WithAttemptVerdicts(verdicts AttemptVerdicts) HandlerOption {
	return func(h *Handler) { h.verdicts = verdicts }
}

// fillAttemptVerdicts sets the verdicts of a ticket's attempts.
func fillAttemptVerdicts(ctx context.Context, verdicts AttemptVerdicts, ticketID int64, attempts []AttemptBody) error {
	if verdicts == nil || len(attempts) == 0 {
		return nil
	}
	refs := make([]AttemptRef, len(attempts))
	for i := range attempts {
		refs[i] = AttemptRef{TicketID: ticketID, Number: attempts[i].Number}
	}
	got, err := verdicts.AttemptVerdicts(ctx, refs)
	if err != nil {
		return err
	}
	for i := range attempts {
		attempts[i].Verdict = got[refs[i]]
	}
	return nil
}
