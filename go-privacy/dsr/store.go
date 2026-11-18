package dsr

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

// ErrNotFound is a request that does not exist.
var ErrNotFound = errors.New("dsr: request not found")

// Store holds the request log. It decides nothing about deadlines or which
// status changes are allowed; Service does.
type Store interface {
	// Insert adds the request and its first event as one unit.
	Insert(ctx context.Context, r *Request, first *Event) error
	Get(ctx context.Context, id uuid.UUID) (*Request, error)
	// Events returns the request's audit trail, oldest first.
	Events(ctx context.Context, id uuid.UUID) ([]*Event, error)
	// List returns requests, open ones soonest due first, then closed ones,
	// most recently received first.
	List(ctx context.Context, f *Filter) ([]*Request, error)
	// Update runs change against the request while holding its lock, so two
	// changes to one request never overlap, and writes back what it left in
	// the request along with the event it returned, as one unit. An error
	// from change writes nothing and is returned as it is.
	Update(ctx context.Context, id uuid.UUID, change func(r *Request) (*Event, error)) (*Request, error)
	// ClaimAlerts marks and returns the open requests whose alert is owed
	// at now: overdue ones, and ones due within soon. A request already
	// alerted at a threshold is not returned for it again, unless its mark
	// is older than lease and no alert event followed it: the claimer died
	// before the alert went out, and nothing else will send it.
	ClaimAlerts(ctx context.Context, now time.Time, soon, lease time.Duration) ([]*Alert, error)
	// ReleaseAlerts undoes ClaimAlerts' marks, for alerts that could not be
	// sent, so the next check offers them again.
	ReleaseAlerts(ctx context.Context, alerts []*Alert) error
	// AppendEvents adds events to the audit trail.
	AppendEvents(ctx context.Context, events []*Event) error
}
