package ticket

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/scope"
)

// baseReadTimeout bounds the code-host read a claim waits on: the agent is
// holding its ticket_next call open for it, and an unpinned ticket is a
// fallback the worker already handles.
const baseReadTimeout = 10 * time.Second

// commitID is a full SHA-1 or SHA-256 commit id.
var commitID = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)

// ErrNoBase is a shadow added to a ticket whose base commit is not pinned. A
// shadow must start from the first attempt's base, which is unknown until it
// is pinned.
var ErrNoBase = errors.New("ticket: the ticket has no pinned base commit, so a shadow attempt cannot start from the first attempt's base")

// PinBase pins the base commit of a ticket that was just claimed, if it has
// none, and returns the ticket as stored. The code host's answer is a head
// read now, so the first pin wins and a later claim never moves it.
//
// It runs after the claim, never inside it: reading the head is a network
// call, and the claim lock is held by every claim in the tenant. A ticket
// whose repository or host cannot be resolved, or whose host does not
// answer, comes back unpinned and the claim stands; the worker then
// branches from the default branch and the next claim tries again.
func (s *Service) PinBase(ctx context.Context, t *Ticket) *Ticket {
	if t == nil || t.BaseCommit != nil {
		return t
	}
	sha, err := s.readBase(ctx, t)
	if err != nil {
		s.logger.Warn("ticket: base commit not pinned", "ticket", t.ID, "err", err)
		return t
	}
	if sha == "" {
		return t
	}
	// The sha goes into the worker's instructions (a git command), so only
	// a commit id is kept, whatever the host answered.
	if !commitID.MatchString(sha) {
		s.logger.Warn("ticket: base commit not pinned", "ticket", t.ID, "err", "the host's answer is not a commit id")
		return t
	}
	stored, err := s.store.PinBaseCommit(ctx, t.ID, sha)
	if err != nil {
		s.logger.Warn("ticket: base commit not pinned", "ticket", t.ID, "err", err)
		return t
	}
	out := *t
	out.BaseCommit = &stored
	return &out
}

// resolveBase is the branch the ticket merges into (BaseResolver).
func (s *Service) resolveBase(ctx context.Context, host *ticketHost) (*ResolvedBase, error) {
	return s.bases.Resolve(ctx, &BaseQuery{Tenant: host.tenant, Repo: host.repo, Changes: host.changes, Manifest: host.manifest})
}

// readBase is the head of the base branch the ticket merges into, in its
// repository, or "" when the ticket has no single repository on a host slop-haus can act on.
func (s *Service) readBase(ctx context.Context, t *Ticket) (string, error) {
	host, err := s.hostFor(ctx, t)
	if err != nil || host == nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, baseReadTimeout)
	defer cancel()
	rb, err := s.resolveBase(ctx, host)
	if err != nil {
		return "", err
	}
	head := rb.Head
	if head == nil {
		if head, err = host.changes.BranchHead(ctx, host.tenant, host.repo, rb.Branch); err != nil {
			return "", fmt.Errorf("read %s of %s/%s: %w", rb.Branch, host.repo.Owner, host.repo.Name, err)
		}
	}
	return head.SHA, nil
}

// ticketHost is the repository a ticket's work is in and the code host's
// operations on it.
type ticketHost struct {
	tenant  uuid.UUID
	repo    codehost.Repo
	changes codehost.Changes
	// manifest is the ticket's repo as the workspace manifest declares it.
	manifest *scope.WorkspaceRepo
}

// hostFor places the ticket's scopes in the workspace manifest, or returns
// nil when the ticket has no single repository on a host slop-haus can act
// on: those tickets are left to the worker, as before slop-haus made
// anything on the host.
func (s *Service) hostFor(ctx context.Context, t *Ticket) (*ticketHost, error) {
	if s.workspace == nil {
		return nil, nil
	}
	tenant, err := db.TenantFrom(ctx)
	if err != nil {
		return nil, err
	}
	pl, err := s.workspace.Place(ctx, scopePaths(t.Paths))
	if err != nil {
		return nil, fmt.Errorf("place the ticket: %w", err)
	}
	if !pl.Declared || len(pl.Unplaced) > 0 || len(pl.Repos) != 1 {
		return nil, nil
	}
	host, repo, ok := s.hosts.ParseRemote(pl.Repos[0].Remote)
	if !ok {
		return nil, nil
	}
	changes, ok := s.hosts.Changes(host)
	if !ok {
		return nil, nil
	}
	return &ticketHost{tenant: tenant, repo: repo, changes: changes, manifest: &pl.Repos[0]}, nil
}

// PinBaseCommit stores sha as the ticket's base commit unless it already has
// one, and returns what is stored: the first writer's value. Outside any
// claim transaction by design (see Service.PinBase).
func (s *PostgresStore) PinBaseCommit(ctx context.Context, ticketID int64, sha string) (string, error) {
	var stored *string
	err := s.db.QueryRow(ctx, `UPDATE tickets SET base_commit = $2 WHERE id = $1 AND base_commit IS NULL RETURNING base_commit`,
		ticketID, sha).Scan(&stored)
	if err == nil {
		return *stored, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", fmt.Errorf("pin the base commit: %w", err)
	}
	// Zero rows: the ticket is gone, or someone pinned it first. A statement
	// of its own, so it sees what that writer committed while this update
	// waited for its row; folded into one statement it would read the
	// snapshot from before.
	err = s.db.QueryRow(ctx, `SELECT base_commit FROM tickets WHERE id = $1`, ticketID).Scan(&stored)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && stored == nil {
		return "", fmt.Errorf("%w: #%d", ErrNotFound, ticketID)
	}
	if err != nil {
		return "", fmt.Errorf("read the pinned base commit: %w", err)
	}
	return *stored, nil
}
