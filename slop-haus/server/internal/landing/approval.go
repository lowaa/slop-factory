package landing

import (
	"context"
	"errors"
	"fmt"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/change"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// ApprovalRouting is the one decision of which engine an approval belongs to:
// the landing queue when the enqueue path is switched on and the tenant is on
// the queue, otherwise servermerge/runner. Both engines read it, so an
// approval is never handled by neither or by both.
type ApprovalRouting struct {
	enabled bool
	mode    Mode
}

// NewApprovalRouting routes to the queue for tenants mode serves, while
// enabled (MERGE_QUEUE_APPROVAL_ENABLED) is true.
func NewApprovalRouting(mode Mode, enabled bool) *ApprovalRouting {
	return &ApprovalRouting{enabled: enabled, mode: mode}
}

// Enqueues reports whether an approval in the context's tenant is enqueued.
// The flag is checked first, so off, no tenant setting is read.
func (r *ApprovalRouting) Enqueues(ctx context.Context) (bool, error) {
	if !r.enabled {
		return false, nil
	}
	return r.mode.Enabled(ctx)
}

// ApprovalEnqueuer is the queue side of an approval.
type ApprovalEnqueuer interface {
	Enqueue(ctx context.Context, r *LandingRow) error
}

// ApprovalHook is ticket.ApprovalHook for a tenant on the queue: the approved,
// now queued attempt gets its landing row and the runner is kicked, as
// Finisher.Land does for a finish. A tenant on the worker flow is left alone:
// its author's agent lands the change.
type ApprovalHook struct {
	routing *ApprovalRouting
	host    Host
	queue   ApprovalEnqueuer
	kicker  Kicker
}

func NewApprovalHook(routing *ApprovalRouting, host Host, queue ApprovalEnqueuer, kicker Kicker) *ApprovalHook {
	return &ApprovalHook{routing: routing, host: host, queue: queue, kicker: kicker}
}

func (h *ApprovalHook) Approved(ctx context.Context, a *ticket.Approval) error {
	ctx = db.WithTenant(ctx, a.TenantID)
	on, err := h.routing.Enqueues(ctx)
	if err != nil || !on {
		return err
	}
	// Read before the write, as in Finisher.Land: a head that moves after this
	// is caught by the merge gate.
	state, err := h.host.State(ctx, a.TenantID, a.TicketID, a.Attempt)
	if errors.Is(err, change.ErrNoChange) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("approval: read the change: %w", err)
	}
	row := &LandingRow{TicketID: a.TicketID, AttemptNumber: a.Attempt, ChangeURL: state.URL, HeadSHA: state.HeadSHA}
	if err := h.queue.Enqueue(ctx, row); err != nil {
		return err
	}
	h.kicker.Kick(a.TenantID)
	return nil
}

var _ ticket.ApprovalHook = (*ApprovalHook)(nil)
