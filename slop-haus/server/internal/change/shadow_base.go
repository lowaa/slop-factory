package change

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

var (
	// ErrTookInMain is an unpromoted attempt's head that contains commits of
	// the repository's base branch made after the ticket's base commit. Every
	// attempt starts from that commit so none sees code its siblings did not;
	// a head that took in main carries the siblings' merged work, and
	// promoting it would merge that history through the attempt.
	ErrTookInMain = errors.New("change: this attempt's branch contains commits from the main branch made after the ticket's base commit")
	// ErrBaseUnverified is the code host not being able to say whether an
	// unpromoted attempt's head took in main. Nothing is let through on a
	// guess: the call is safe to repeat.
	ErrBaseUnverified = errors.New("change: slop-haus could not check this attempt's branch against the ticket's base commit; this is temporary, call again in a minute")
)

// forChange is the target as the change's own repository sees it: the base
// branch is the ticket's, but the repository and host are the ones the change
// is in, which is where its head is read.
func (t *target) forChange(rec *Record) *target {
	c := *t
	c.host, c.repo = rec.Change.Host, rec.Change.InRepo()
	return &c
}

// checkShadowBase refuses an unpromoted attempt's head (a branch name or a
// commit) that contains commits of the base branch past the ticket's pinned
// base commit. Merging the ticket's own base branch is fine: it is at that
// commit. The host is asked each time: a push can move the head, and main
// moves on its own.
func (s *Service) checkShadowBase(ctx context.Context, tenant, agentID uuid.UUID, ticketID int64, baseCommit *string, tgt *target, branch, head string) error {
	if baseCommit == nil {
		return fmt.Errorf("%w: the ticket has no pinned base commit for this attempt's base branch", ErrInvalid)
	}
	took, err := tgt.changes.TakesInBranchPast(ctx, tenant, tgt.repo, tgt.base, *baseCommit, head)
	if err != nil {
		s.record(ctx, audit.ChangeOperationFailed, s.attrs(tenant, agentID, ticketID, "check_shadow_base", "host", tgt.host, "repo", tgt.repo.Owner+"/"+tgt.repo.Name, "branch", branch, "err", err.Error())...)
		return fmt.Errorf("%w: %w", ErrBaseUnverified, err)
	}
	if took {
		return fmt.Errorf("%w (%s, base commit %s). It must start from the base commit and catch up by merging %s, never origin/%s. To fix it: "+
			"`git fetch origin && git switch -c %s-fix %s`, cherry-pick your own commits onto it, then `git push --force-with-lease origin HEAD:%s`",
			ErrTookInMain, tgt.base, *baseCommit, ticket.UnpromotedBase(ticketID), tgt.base, branch, *baseCommit, branch)
	}
	return nil
}
