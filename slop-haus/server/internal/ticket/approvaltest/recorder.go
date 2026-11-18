// Package approvaltest is a ticket.ApprovalHook for tests.
package approvaltest

import (
	"context"
	"sync"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// Recorder keeps every approval it is told of. Behave, when set, runs after
// the approval is recorded, to make the hook slow, failing or panicking.
type Recorder struct {
	Behave func(ctx context.Context, a *ticket.Approval) error

	mu  sync.Mutex
	got []ticket.Approval
}

func (r *Recorder) Approved(ctx context.Context, a *ticket.Approval) error {
	r.mu.Lock()
	r.got = append(r.got, *a)
	r.mu.Unlock()
	if r.Behave != nil {
		return r.Behave(ctx, a)
	}
	return nil
}

// Approvals is what the hook has been told so far, oldest first.
func (r *Recorder) Approvals() []ticket.Approval {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]ticket.Approval(nil), r.got...)
}
