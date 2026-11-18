package consent

import (
	"context"
	"errors"

	"github.com/google/uuid"
)

// ErrUnknownUser means the user has no row to lock, so nothing can be
// recorded for them.
var ErrUnknownUser = errors.New("consent: unknown user")

// Plan is what a store should do once it holds the user's record.
type Plan struct {
	Events []NewEvent
	// MergeToken, if set, is claimed before the events are written. A token
	// that has been claimed before (by any user) makes the store skip the
	// events merged from the anonymous cookie (MergedFromAnonymous) and
	// report TokenAlreadyMerged; the plan's other events are still written.
	MergeToken string
}

// Applied is the outcome of Store.Apply.
type Applied struct {
	Events             []*Event
	TokenAlreadyMerged bool
}

// Store holds the append-only consent record.
type Store interface {
	// Current returns the latest event per purpose.
	Current(ctx context.Context, userID uuid.UUID) (map[string]*Event, error)
	// History returns every event for the user, oldest first.
	History(ctx context.Context, userID uuid.UUID) ([]*Event, error)
	// Apply runs plan against the user's current record and appends what it
	// returns, as one unit that no other Apply for the same user overlaps:
	// plan sees the record exactly as it will be when its events land. A
	// plan error appends nothing and is returned as is.
	Apply(ctx context.Context, userID uuid.UUID, plan func(current map[string]*Event) (*Plan, error)) (*Applied, error)
}
