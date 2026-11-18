package pendingaction

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// MergeCloser is what approving a close_merged proposal runs: the same
// service the board's "Close as merged" button calls, so the same checks
// apply (owner, merged into the ticket's base branch on the code host).
type MergeCloser interface {
	MergeFacts(ctx context.Context, ticketID int64) (*ticket.MergeFacts, error)
	CloseAsMerged(ctx context.Context, ticketID int64, person uuid.UUID, confirm bool) (*ticket.CloseAsMergedResult, error)
}

// WithMergeCloser lets close_merged proposals be approved and shown with what
// the code host says. Without it they cannot be approved.
func WithMergeCloser(c MergeCloser) Option {
	return func(s *Service) { s.closer = c }
}

// defaultMergeFactsDeadline bounds how long a listing waits on the code host
// in total: a browser gives up on a slow list, and the owner cannot approve
// what they cannot see.
const defaultMergeFactsDeadline = 5 * time.Second

// WithMergeFactsDeadline overrides how long a listing waits for the code host
// before showing the proposals it has not heard about as unchecked.
func WithMergeFactsDeadline(d time.Duration) Option {
	return func(s *Service) { s.mergeFactsDeadline = d }
}

// CloseMergedView is what the code host says about a close_merged proposal's
// ticket now, for the approver to read before approving.
type CloseMergedView struct {
	// Confirmations are what the merge leaves the approver to accept:
	// failed or missing checks, a head nobody approved. Approving is the
	// confirmation of them.
	Confirmations []string
	// Problem is why approving would be refused now (not merged into the
	// base branch, host unreadable); empty when it would not.
	Problem string
}

// fillCloseMerged asks the code host about every pending close_merged view at
// once, under one deadline for the lot, so a listing's latency does not grow
// with the number of proposals. Approving asks the host again itself.
func (s *Service) fillCloseMerged(ctx context.Context, views []View) {
	var todo []*View
	for i := range views {
		if views[i].Kind == KindCloseMerged {
			views[i].CloseMerged = &CloseMergedView{}
			if views[i].State == StatePending {
				todo = append(todo, &views[i])
			}
		}
	}
	if len(todo) == 0 || s.closer == nil {
		return
	}
	deadline := s.mergeFactsDeadline
	if deadline <= 0 {
		deadline = defaultMergeFactsDeadline
	}
	ctx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()
	var wg sync.WaitGroup
	for _, v := range todo {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v.CloseMerged = s.closeMergedView(ctx, &v.Action)
		}()
	}
	wg.Wait()
}

func (s *Service) closeMergedView(ctx context.Context, a *Action) *CloseMergedView {
	if a.State != StatePending || s.closer == nil {
		return &CloseMergedView{}
	}
	f, err := s.closer.MergeFacts(ctx, a.ticketID())
	if err != nil {
		if ctx.Err() != nil {
			return &CloseMergedView{Problem: "couldn't check the code host yet; reload to try again"}
		}
		return &CloseMergedView{Problem: err.Error()}
	}
	if !f.MergedIntoBase {
		return &CloseMergedView{Problem: fmt.Sprintf("the code host does not show the change merged into %s", f.Base)}
	}
	return &CloseMergedView{Confirmations: f.Confirmations()}
}

// driftSinceRecheck narrows the gap between the recheck's transaction and the
// close, which cannot share one (the close reads the code host): a ticket
// that was edited, moved or given another change in between is not what the
// owner approved. The close still checks the host itself, so what remains is
// a claim landing in these milliseconds, whose lease the close then ends.
func (s *Service) driftSinceRecheck(ctx context.Context, a *Action) string {
	live, err := s.tickets.Get(ctx, a.ticketID())
	if err != nil {
		return "" // The close reads the ticket again and reports it.
	}
	return a.Basis.differsFrom(&Basis{Status: live.Status, Revision: live.Revision, PRURL: live.PRURL, ReviewStartedAt: a.Basis.ReviewStartedAt})
}

func (s *Service) settleStale(ctx context.Context, approver *authn.User, a *Action, reason string) (*Action, error) {
	settled, err := s.store.SettleOwn(ctx, a.ID, StateStale, "the ticket has changed since it was proposed: "+reason, approver.ID, s.now())
	if err != nil {
		return nil, err
	}
	s.refused(ctx, approver, settled, byClick)
	return settled, fmt.Errorf("%w: %s", ErrStale, settled.Outcome)
}

// closeMerged applies a close_merged proposal that passed its checks and
// settles it. The close is the ticket service's own transaction, so a refusal
// leaves the ticket as it was and fails the proposal; confirm is true because
// the approver has been shown what the merge leaves to confirm.
func (s *Service) closeMerged(ctx context.Context, approver *authn.User, a *Action) (*Action, error) {
	var err error
	if s.closer == nil {
		err = ErrNotConfigured
	} else if reason := s.driftSinceRecheck(ctx, a); reason != "" {
		return s.settleStale(ctx, approver, a, reason)
	} else {
		_, err = s.closer.CloseAsMerged(ctx, a.ticketID(), approver.ID, true)
	}
	if errors.Is(err, ticket.ErrTicketFinished) {
		// Another approval of this proposal closed it first and is settling
		// it: failing the proposal here could overwrite that success.
		return nil, fmt.Errorf("%w: it is being decided", ErrNotPending)
	}
	if err != nil {
		settled, serr := s.store.SettleOwn(ctx, a.ID, StateFailed, err.Error(), approver.ID, s.now())
		if serr != nil {
			return nil, serr
		}
		s.refused(ctx, approver, settled, byClick)
		return settled, fmt.Errorf("%w: %w", ErrRefused, err)
	}
	return s.store.SettleOwn(ctx, a.ID, StateApproved, "", approver.ID, s.now())
}
