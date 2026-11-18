package ticket

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
)

// Merger is who merged on the host. UserID is nil when no member has that
// GitHub account linked (a bot, an outside collaborator): the merge is still
// recorded, under the GitHub login and id, since the safeguard must not
// depend on who made the mistake.
type Merger struct {
	UserID      *uuid.UUID
	GitHubLogin string
	GitHubID    int64
}

// UnpromotedAttemptForPR is the attempt whose change pr is, when only
// unpromoted attempts report it. Any other PR, even an unknown or ambiguous
// one, is nil: those are for the merge rules to refuse.
func (s *Service) UnpromotedAttemptForPR(ctx context.Context, pr codehost.ChangeRef) (*AttemptRef, error) {
	_, err := s.ticketForPR(ctx, pr, ErrNotMergeable, func(*Ticket) bool { return false })
	var unpromoted *AttemptNotPromotedError
	switch {
	case errors.As(err, &unpromoted):
		return &unpromoted.Attempt, nil
	case errors.Is(err, ErrNoTicketForPR), errors.Is(err, ErrAmbiguousPR), errors.Is(err, ErrNotMergeable):
		return nil, nil
	}
	return nil, err
}

// MergedIntoBase is a merge, on the host, of an unpromoted attempt's
// change into the ticket's base branch (UnpromotedBase). Only that throwaway
// branch moved: nothing reached main. The attempt's change is recorded as
// merged into the base and the attempt alone goes to discussion, with a
// comment asking the person what to do; the promoted attempt and the
// siblings are untouched.
//
// attempt is the one ticketForPR matched. Everything is judged again under
// the claim lock, since a promotion, a close or a new change may have landed
// since: those refuse with ErrNotMergeable, which a delivery treats as
// nothing to do.
func (s *Service) MergedIntoBase(ctx context.Context, attempt AttemptRef, pr codehost.ChangeRef, merger Merger) (*Ticket, error) {
	if merger.UserID != nil {
		ctx = WithUser(ctx, *merger.UserID)
	}
	var out *Ticket
	err := s.store.InTransition(ctx, func(tx TransitionTx) error {
		var err error
		out, err = mergedIntoBase(ctx, tx, s.hosts, &attempt, pr, merger)
		return err
	})
	if err != nil {
		return nil, err
	}
	out.MatchedAttempt = attempt.Number
	// After the commit, so a refused call leaves no record of one.
	if s.events != nil {
		tenant, _ := db.TenantFrom(ctx)
		args := []any{"tenant", tenant, "ticket", attempt.TicketID, "attempt", attempt.Number,
			"change", s.hosts.ChangeURL(pr), "base", UnpromotedBase(attempt.TicketID)}
		if merger.UserID != nil {
			args = append(args, "user_id", *merger.UserID)
		} else {
			args = append(args, "github_login", merger.GitHubLogin, "github_id", merger.GitHubID)
		}
		s.events.Record(ctx, audit.ChangeMergedIntoBase, args...)
	}
	return out, nil
}

func mergedIntoBase(ctx context.Context, tx TransitionTx, hosts *codehost.Registry, attempt *AttemptRef, pr codehost.ChangeRef, merger Merger) (*Ticket, error) {
	q := tx.Querier()
	notMergeable := func(why string) error {
		return fmt.Errorf("%w: %s is no longer a live change of #%d attempt %d (%s)", ErrNotMergeable, hosts.ChangeURL(pr), attempt.TicketID, attempt.Number, why)
	}
	states, err := attemptStates(ctx, q, attempt.TicketID)
	if err != nil {
		return nil, err
	}
	var state *attemptState
	for i := range states {
		if states[i].number == attempt.Number {
			state = &states[i]
		}
	}
	switch {
	case state == nil:
		return nil, notMergeable("no such attempt")
	case state.promoted:
		return nil, notMergeable("it was promoted")
	case !state.live():
		return nil, notMergeable("the attempt is " + state.status)
	}

	// The live row for this very change: the write that supersedes it is
	// also what makes a redelivery find nothing.
	var recorded int64
	var openedBy uuid.UUID
	err = q.QueryRow(ctx, `
		UPDATE ticket_changes SET superseded_at = now(), merged_into_base_at = now()
		 WHERE ticket_id = $1 AND attempt_number = $2 AND host = $3 AND owner = $4 AND repo = $5 AND number = $6
		   AND superseded_at IS NULL AND merged_at IS NULL AND closed_at IS NULL
		RETURNING id, opened_by_agent_id`,
		attempt.TicketID, attempt.Number, pr.Host, pr.Owner, pr.Repo, pr.Number).Scan(&recorded, &openedBy)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, notMergeable("it is not the attempt's live change")
	}
	if err != nil {
		return nil, fmt.Errorf("record the merge into the base branch: %w", err)
	}

	// An agent still working on the attempt is working on a change that is
	// already merged.
	if err := revokeLeases(ctx, q, `ticket_id = $1 AND attempt_number = $2`, []any{attempt.TicketID, attempt.Number},
		"revoked: change merged into the base branch by a human", StatusDiscussion); err != nil {
		return nil, err
	}
	on := tx.ForAttempt(attempt)
	// The schema has no system author, so a merger who is no member is
	// named in the body and the comment is recorded under the agent that
	// opened the change.
	by, who := Author{AgentID: &openedBy}, ""
	if merger.UserID != nil {
		by = UserAuthor(*merger.UserID)
	} else {
		who = fmt.Sprintf("[GitHub user %s (id %d), not a member here] ", merger.GitHubLogin, merger.GitHubID)
	}
	body := who + fmt.Sprintf("This attempt's change %s was merged into the ticket's base branch %s, not main. Nothing reached main. "+
		"What should happen to this attempt: promote it, close it, or something else?", hosts.ChangeURL(pr), UnpromotedBase(attempt.TicketID))
	if _, err := decideAndApply(ctx, on, nil, attempt.TicketID, func(*Ticket) (*Change, error) {
		return &Change{
			Comment: &NewComment{By: by, Body: body, Attempt: attempt},
			Ticket:  TicketWrite{Status: To(StatusDiscussion)},
		}, nil
	}); err != nil {
		return nil, err
	}
	return tx.Ticket(ctx, attempt.TicketID)
}
