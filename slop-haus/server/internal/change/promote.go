package change

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// doNotMergePrefix marks the change of an attempt the ticket has chosen
// against, so nobody merges it from the host by hand.
const doNotMergePrefix = "[DO NOT MERGE] "

// doNotMergeTitle is title with the prefix, once.
func doNotMergeTitle(title string) string {
	if strings.HasPrefix(title, doNotMergePrefix) {
		return title
	}
	return doNotMergePrefix + title
}

func mergeableTitle(title string) string {
	return strings.TrimPrefix(title, doNotMergePrefix)
}

// CheckPromotable refuses, before the promotion is made, an unpromoted
// attempt whose change was handed over on a head that took in main: a shadow
// can push after its hand-over, and nothing else has judged that head since.
// An attempt with no live change, or one that already targets the base
// branch, has nothing to check. The answer is as of the call: a push after it
// is not seen, which matters little, since the promoted attempt takes in main
// to merge as every ticket's does and is unchecked from then on. The wrapped ticket errors let the caller map
// them.
func (s *Service) CheckPromotable(ctx context.Context, tenant uuid.UUID, ticketID int64, attempt int) error {
	if err := tenantOf(ctx, tenant); err != nil {
		return err
	}
	rec, err := s.store.Current(ctx, ticketID, attempt)
	if errors.Is(err, ErrNoChange) || (err == nil && (rec.MergedAt != nil || rec.Base != ticket.UnpromotedBase(ticketID))) {
		return nil
	}
	if err != nil {
		return err
	}
	t, err := s.tickets.Get(ctx, ticketID)
	if err != nil {
		return err
	}
	changes, ok := s.hosts.Changes(rec.Change.Host)
	if !ok {
		return fmt.Errorf("%w: %w: %s", ticket.ErrPromotionUnverified, ErrNoHost, rec.Change.Host)
	}
	checks, err := changes.Checks(ctx, tenant, rec.Change)
	if err != nil {
		s.record(ctx, audit.ChangeOperationFailed, s.opAttrs(tenant, ticketID, attempt, "check_promotable", rec, "err", err.Error())...)
		return fmt.Errorf("%w: %w", ticket.ErrPromotionUnverified, err)
	}
	tgt, err := s.targetFor(ctx, t)
	if err != nil {
		return fmt.Errorf("%w: %w", ticket.ErrPromotionUnverified, err)
	}
	tgt = tgt.forChange(rec)
	tgt.changes = changes
	err = s.checkShadowBase(ctx, tenant, rec.OpenedBy, ticketID, t.BaseCommit, tgt, rec.Head, checks.HeadSHA)
	switch {
	case errors.Is(err, ErrBaseUnverified):
		return fmt.Errorf("%w: %w", ticket.ErrPromotionUnverified, err)
	case err != nil:
		return fmt.Errorf("%w: %w", ticket.ErrPromotionBlocked, err)
	}
	return nil
}

// ApplyPromotion carries the ticket's promotion to the code host: the
// promoted attempt's change goes to the repository's base branch and loses the
// do-not-merge prefix, every other live attempt's goes to the ticket's own
// base branch and gains it. It works from the promotion as it is now, not as
// the request that called it left it, so a request finishing late cannot undo
// a later choice; and every step is idempotent, so repeating it is how a
// failure is retried. Made with no lock held. One attempt's failure does not
// stop the others; each is audited, and the errors are returned joined. The
// promotion stands either way, and a change left on the wrong branch or title
// stays so until this is repeated or it is fixed on the host (nothing
// reconciles them yet).
func (s *Service) ApplyPromotion(ctx context.Context, tenant uuid.UUID, ticketID int64) error {
	if err := tenantOf(ctx, tenant); err != nil {
		return err
	}
	t, err := s.tickets.Get(ctx, ticketID)
	if err != nil {
		return err
	}
	attempts, err := s.tickets.Attempts(ctx, ticketID)
	if err != nil {
		return err
	}
	var errs []error
	for i := range attempts {
		if attempts[i].Status == "closed" {
			continue
		}
		if err := s.alignChange(ctx, tenant, t, attempts[i].Number, attempts[i].PromotedAt != nil); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (s *Service) opAttrs(tenant uuid.UUID, ticketID int64, attempt int, op string, rec *Record, more ...any) []any {
	return append([]any{"tenant_id", tenant.String(), "ticket", ticketID, "attempt", attempt, "operation", op, "change", s.hosts.ChangeURL(rec.Change)}, more...)
}

// alignChange makes the attempt's live change match whether it is promoted.
func (s *Service) alignChange(ctx context.Context, tenant uuid.UUID, t *ticket.Ticket, attempt int, promoted bool) error {
	rec, err := s.store.Current(ctx, t.ID, attempt)
	if errors.Is(err, ErrNoChange) || (err == nil && (rec.MergedAt != nil || rec.ClosedAt != nil)) {
		return nil
	}
	if err != nil {
		return err
	}
	changes, ok := s.hosts.Changes(rec.Change.Host)
	if !ok {
		err := fmt.Errorf("%w: %s", ErrNoHost, rec.Change.Host)
		s.record(ctx, audit.ChangeOperationFailed, s.opAttrs(tenant, t.ID, attempt, "promotion", rec, "err", err.Error())...)
		return err
	}
	fail := func(op string, err error) error {
		s.record(ctx, audit.ChangeOperationFailed, s.opAttrs(tenant, t.ID, attempt, op, rec, "err", err.Error())...)
		return fmt.Errorf("attempt %d: %s: %w", attempt, op, err)
	}
	base := ""
	if promoted {
		tgt, err := s.targetFor(ctx, t)
		if err != nil {
			return fail("resolve_base", err)
		}
		base = tgt.base
	} else {
		base = ticket.UnpromotedBase(t.ID)
		// A demoted attempt's change may predate the ticket having a base
		// branch at all. Idempotent.
		if t.BaseCommit != nil {
			if err := changes.CreateBranch(ctx, tenant, rec.Change.InRepo(), base, *t.BaseCommit); err != nil {
				return fail("create_base_branch", err)
			}
		}
	}
	if err := changes.RetargetChange(ctx, tenant, rec.Change, base); err != nil {
		return fail("retarget_change", err)
	}
	if err := s.store.SetBase(ctx, rec.ID, base); err != nil {
		return err
	}
	// A change the ticket chose against is a draft, whatever its attempt did
	// before it was demoted. Only the host can say a change is a draft, so
	// a host that cannot convert one is left as it is.
	if dm, ok := changes.(codehost.DraftMarker); ok && !promoted {
		if err := dm.MarkDraft(ctx, tenant, rec.Change); err != nil {
			return fail("mark_draft", err)
		}
	}
	checks, err := changes.Checks(ctx, tenant, rec.Change)
	if err != nil {
		return fail("read_title", err)
	}
	title := mergeableTitle(checks.Title)
	if !promoted {
		title = doNotMergeTitle(checks.Title)
	}
	if title != checks.Title {
		if err := changes.RetitleChange(ctx, tenant, rec.Change, title); err != nil {
			return fail("retitle_change", err)
		}
	}
	return nil
}
