package change

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
)

// The closing rule: an experiment's change (an unpromoted attempt's) is closed
// once its ticket is done and its own attempt has finished, and the ticket's
// base branch is deleted once nothing live targets it. The rule is one query
// (Store.DueClosings) applied from three places so it holds however events
// interleave: after each status transition (TicketsSettled), after change_open
// records a change, and by the periodic Reconciler for a crash between a
// commit and its host call.
//
// A parked benchmark's finished replays are closed like any experiment's, but
// its base branch is never due: only a done ticket's is (DeletableBases), so
// replays of a parked ticket keep theirs for as long as it stays parked. The claim makes it again from base_commit if it is gone.
//
// Every step is idempotent, so those callers may race: the host treats a
// repeated close or delete as success, and the record is written once.

// settledBound caps the host calls made after a transition: they run after
// its commit and must not outlive a request that has already answered.
const settledBound = time.Minute

// ClosingReport is what one application of the rule did.
type ClosingReport struct {
	Closed       []string
	BasesDeleted []string
}

// TicketsSettled applies the rule to tickets whose status, or one of whose
// attempts' status, a transition just committed. It is called with no lock
// held. Failures are audited and left for the reconciler.
func (s *Service) TicketsSettled(ctx context.Context, ticketIDs []int64) {
	tenant, err := db.TenantFrom(ctx)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), settledBound)
	defer cancel()
	for _, id := range ticketIDs {
		if _, err := s.ApplyClosingRule(ctx, tenant, id); err != nil {
			s.logger.Warn("change: closing rule left work for the reconciler", "ticket", id, "err", err)
		}
	}
}

// ApplyClosingRule applies the rule to one ticket, or to every ticket of the
// tenant when ticketID is 0. One failure does not stop the rest; the errors
// are returned joined.
func (s *Service) ApplyClosingRule(ctx context.Context, tenant uuid.UUID, ticketID int64) (*ClosingReport, error) {
	if err := tenantOf(ctx, tenant); err != nil {
		return nil, err
	}
	report := &ClosingReport{}
	due, err := s.store.DueClosings(ctx, ticketID)
	if err != nil {
		return nil, err
	}
	var errs []error
	for _, rec := range due {
		url := s.hosts.ChangeURL(rec.Change)
		closed, err := s.closeByRule(ctx, tenant, rec)
		if err != nil {
			errs = append(errs, err)
		}
		if closed {
			report.Closed = append(report.Closed, url)
		}
	}
	bases, err := s.store.DeletableBases(ctx, ticketID)
	if err != nil {
		return report, errors.Join(append(errs, err)...)
	}
	for _, b := range bases {
		deleted, err := s.deleteBase(ctx, tenant, b)
		if err != nil {
			errs = append(errs, err)
		}
		if deleted {
			report.BasesDeleted = append(report.BasesDeleted, b.Name)
		}
	}
	return report, errors.Join(errs...)
}

// closeByRule closes rec on the host, then records it. A change that was
// merged meanwhile is left alone, not an error: landed work is never closed.
func (s *Service) closeByRule(ctx context.Context, tenant uuid.UUID, rec *Record) (bool, error) {
	url := s.hosts.ChangeURL(rec.Change)
	attrs := []any{"tenant_id", tenant.String(), "ticket", rec.TicketID, "attempt", rec.Attempt, "operation", "close_by_rule", "change", url}
	changes, ok := s.hosts.Changes(rec.Change.Host)
	if !ok {
		err := fmt.Errorf("%w: %s", ErrNoHost, rec.Change.Host)
		s.record(ctx, audit.ChangeOperationFailed, append(attrs, "err", err.Error())...)
		return false, err
	}
	switch err := changes.CloseChange(ctx, tenant, rec.Change); {
	case errors.Is(err, codehost.ErrAlreadyMerged):
		return false, nil
	case err != nil:
		s.record(ctx, audit.ChangeOperationFailed, append(attrs, "err", err.Error())...)
		return false, fmt.Errorf("close %s: %w", url, err)
	}
	recorded, err := s.store.MarkClosed(ctx, rec.ID, closedByRule)
	if err != nil {
		return false, err
	}
	if recorded {
		s.record(ctx, audit.ChangeClosedByRule, attrs...)
	}
	return recorded, nil
}

func (s *Service) deleteBase(ctx context.Context, tenant uuid.UUID, b BaseBranch) (bool, error) {
	attrs := []any{"tenant_id", tenant.String(), "ticket", b.TicketID, "operation", "delete_base_branch", "branch", b.Name,
		"repo", b.Repo.Owner + "/" + b.Repo.Name}
	changes, ok := s.hosts.Changes(b.Host)
	if !ok {
		err := fmt.Errorf("%w: %s", ErrNoHost, b.Host)
		s.record(ctx, audit.ChangeOperationFailed, append(attrs, "err", err.Error())...)
		return false, err
	}
	if err := changes.DeleteBranch(ctx, tenant, b.Repo, b.Name); err != nil {
		s.record(ctx, audit.ChangeOperationFailed, append(attrs, "err", err.Error())...)
		return false, fmt.Errorf("delete %s: %w", b.Name, err)
	}
	if err := s.store.MarkBaseDeleted(ctx, b); err != nil {
		return false, err
	}
	s.record(ctx, audit.BaseBranchDeleted, attrs...)
	return true, nil
}
