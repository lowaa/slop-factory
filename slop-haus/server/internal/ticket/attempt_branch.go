package ticket

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost"
)

// branchWriteTimeout bounds the code-host writes a claim (or a ticket_get
// retry) waits on, for the same reason as baseReadTimeout.
const branchWriteTimeout = 10 * time.Second

var errNoHost = errors.New("the ticket has no repository on a code host slop-haus acts on")

// UnpromotedBase is the branch every unpromoted attempt of the ticket opens
// its change against: it sits at the ticket's pinned base commit, so the
// attempts' pull requests show only their own work, and nothing in them can
// reach the repository's base branch. It has no `/`: git cannot create
// `slop-haus/base/...` while a branch named `slop-haus` exists.
func UnpromotedBase(ticketID int64) string {
	return fmt.Sprintf("slop-haus-base-ticket-%d", ticketID)
}

// AttemptBranch is where a claimed attempt's work starts: the one place that
// works out its branch and whether slop-haus has made it. ticket_next and
// ticket_get word it for the agent from here.
type AttemptBranch struct {
	// Name is the branch the attempt's work goes on and its PR is opened from.
	Name string
	// Managed is whether slop-haus makes the branch for this ticket. False
	// for a ticket with no pinned base commit (no single repository on a
	// host slop-haus acts on, or the host did not answer): the agent makes
	// the branch itself, as it did before slop-haus made them.
	Managed bool
	// Unpromoted is whether the attempt is not the ticket's promoted one: its
	// work stays on the base commit and never takes in main.
	Unpromoted bool
	// Ready is whether the branch exists on the host. A managed branch that
	// is not ready failed to be made, and is made again on the next
	// ticket_get or claim.
	Ready bool
	// Base is the branch the tenant's manifest sends the ticket's change to;
	// "" is the repo's default branch (or a ticket with no single repository
	// on a host slop-haus acts on). It is the manifest's answer only: naming
	// the default branch is a host read, which a worker's guidance is not worth.
	Base string
}

// EnsureAttemptBranch makes the claimed attempt's branch at the ticket's base
// commit, and for an unpromoted attempt the ticket's base branch too. t is as
// PinBase returned it.
//
// It runs after the claim, never inside it, like PinBase: the claim lock is
// held by every claim in the tenant. A host that fails leaves the branch not
// ready and the claim stands. The attempt's branch is only ever created,
// never moved: a re-claim finds an earlier agent's pushed work on it.
func (s *Service) EnsureAttemptBranch(ctx context.Context, t *Ticket, number int) *AttemptBranch {
	b := &AttemptBranch{Name: BranchName(t.ID, number)}
	if t.BaseCommit == nil {
		return b
	}
	b.Managed = true
	b.Base = s.explicitBaseName(ctx, t)
	state, err := s.store.AttemptBranchState(ctx, t.ID, number)
	if err != nil {
		s.logger.Warn("ticket: attempt branch not made", "ticket", t.ID, "attempt", number, "err", err)
		return b
	}
	b.Unpromoted = !state.Promoted
	if state.CreatedAt != nil {
		b.Ready = !state.Replay || s.ensureReplayBase(ctx, t, number)
		return b
	}
	if err := s.makeBranches(ctx, t, b.Name, !state.Promoted); err != nil {
		s.logger.Warn("ticket: attempt branch not made", "ticket", t.ID, "attempt", number, "err", err)
		return b
	}
	if err := s.store.MarkBranchCreated(ctx, t.ID, number); err != nil {
		s.logger.Warn("ticket: attempt branch made but not recorded", "ticket", t.ID, "attempt", number, "err", err)
		return b
	}
	b.Ready = true
	return b
}

// ensureReplayBase makes the base branch again if the closing rule deleted it
// while the ticket was done: a replay claimed (or re-claimed after a
// discussion) later than that is told to catch up from it. Idempotent, and
// only for replays, whose ticket is the one the rule deletes it of.
func (s *Service) ensureReplayBase(ctx context.Context, t *Ticket, number int) bool {
	host, err := s.hostFor(ctx, t)
	if err == nil && host == nil {
		err = errNoHost
	}
	if err == nil {
		ctx, cancel := context.WithTimeout(ctx, branchWriteTimeout)
		defer cancel()
		err = host.changes.CreateBranch(ctx, host.tenant, host.repo, UnpromotedBase(t.ID), *t.BaseCommit)
	}
	// A base already somewhere else is still a base to catch up from.
	if err != nil && !errors.Is(err, codehost.ErrBranchConflict) {
		s.logger.Warn("ticket: replay base branch not made", "ticket", t.ID, "attempt", number, "err", err)
		return false
	}
	return true
}

// explicitBaseName is the base branch the manifest names for t, or "".
func (s *Service) explicitBaseName(ctx context.Context, t *Ticket) string {
	if s.workspace == nil {
		return ""
	}
	pl, err := s.workspace.Place(ctx, scopePaths(t.Paths))
	if err != nil {
		s.logger.Warn("ticket: base branch not resolved", "ticket", t.ID, "err", err)
		return ""
	}
	if !pl.Declared || len(pl.Unplaced) > 0 || len(pl.Repos) != 1 {
		return ""
	}
	return pl.Repos[0].BaseBranch
}

func (s *Service) makeBranches(ctx context.Context, t *Ticket, branch string, withBase bool) error {
	host, err := s.hostFor(ctx, t)
	if err == nil && host == nil {
		err = errNoHost
	}
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, branchWriteTimeout)
	defer cancel()
	attrs := []any{"tenant_id", host.tenant.String(), "ticket", t.ID, "repo", host.repo.Owner + "/" + host.repo.Name, "commit", *t.BaseCommit}
	if withBase {
		// Strict: the base branch must be at the pinned commit, as change_open
		// requires of it.
		if err := host.changes.CreateBranch(ctx, host.tenant, host.repo, UnpromotedBase(t.ID), *t.BaseCommit); err != nil {
			s.recordBranch(ctx, audit.ChangeOperationFailed, append(attrs, "operation", "create_base_branch", "branch", UnpromotedBase(t.ID), "err", err.Error()))
			return fmt.Errorf("create the base branch %s: %w", UnpromotedBase(t.ID), err)
		}
	}
	if err := host.changes.EnsureBranch(ctx, host.tenant, host.repo, branch, *t.BaseCommit); err != nil {
		s.recordBranch(ctx, audit.ChangeOperationFailed, append(attrs, "operation", "create_attempt_branch", "branch", branch, "err", err.Error()))
		return fmt.Errorf("create the attempt branch %s: %w", branch, err)
	}
	s.recordBranch(ctx, audit.AttemptBranchCreated, append(attrs, "branch", branch, "base_branch", withBase))
	return nil
}

func (s *Service) recordBranch(ctx context.Context, action audit.Action, attrs []any) {
	if s.events != nil {
		s.events.Record(ctx, action, attrs...)
	}
}

// AttemptBranchState is what the store knows of an attempt's branch.
type AttemptBranchState struct {
	// Promoted attempts open their change against the repository's base
	// branch; others against UnpromotedBase.
	Promoted bool
	// CreatedAt is when slop-haus made the attempt's branch, nil until it has.
	CreatedAt *time.Time
	// Replay is whether the attempt is a replay of a done ticket.
	Replay bool
}

// AttemptBranchState reads the attempt's branch state.
func (s *PostgresStore) AttemptBranchState(ctx context.Context, ticketID int64, number int) (*AttemptBranchState, error) {
	var st AttemptBranchState
	err := s.db.QueryRow(ctx, `SELECT promoted_at IS NOT NULL, branch_created_at, replay FROM attempts WHERE ticket_id = $1 AND number = $2`,
		ticketID, number).Scan(&st.Promoted, &st.CreatedAt, &st.Replay)
	if err != nil {
		return nil, fmt.Errorf("read the attempt's branch state: %w", err)
	}
	return &st, nil
}

// MarkBranchCreated records the attempt's branch as made. Setting it twice
// keeps the first time.
func (s *PostgresStore) MarkBranchCreated(ctx context.Context, ticketID int64, number int) error {
	if _, err := s.db.Exec(ctx, `UPDATE attempts SET branch_created_at = now() WHERE ticket_id = $1 AND number = $2 AND branch_created_at IS NULL`,
		ticketID, number); err != nil {
		return fmt.Errorf("record the attempt's branch: %w", err)
	}
	return nil
}
