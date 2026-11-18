package change

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
)

// CloseAttemptChange closes, on the code host, the change an attempt a person
// has closed still has open. It leaves the attempt's branch and the ticket's
// base branch: the work stays readable. An attempt with no change, or whose
// change merged, has nothing to close.
//
// A failure is audited as a failed operation and returned; the attempt is
// closed either way, so the caller reports it and carries on. The host call is
// made with no lock held.
func (s *Service) CloseAttemptChange(ctx context.Context, tenant uuid.UUID, ticketID int64, attempt int) error {
	if err := tenantOf(ctx, tenant); err != nil {
		return err
	}
	rec, err := s.store.Current(ctx, ticketID, attempt)
	if errors.Is(err, ErrNoChange) || (err == nil && (rec.MergedAt != nil || rec.ClosedAt != nil)) {
		return nil
	}
	if err != nil {
		return err
	}
	url := s.hosts.ChangeURL(rec.Change)
	attrs := []any{"tenant_id", tenant.String(), "ticket", ticketID, "attempt", attempt, "operation", "close_change", "change", url}
	changes, ok := s.hosts.Changes(rec.Change.Host)
	if !ok {
		err := fmt.Errorf("%w: %s", ErrNoHost, rec.Change.Host)
		s.record(ctx, audit.ChangeOperationFailed, append(attrs, "err", err.Error())...)
		return err
	}
	if err := changes.CloseChange(ctx, tenant, rec.Change); err != nil {
		s.record(ctx, audit.ChangeOperationFailed, append(attrs, "err", err.Error())...)
		return err
	}
	return nil
}
