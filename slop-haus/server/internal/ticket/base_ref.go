package ticket

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

var (
	// ErrBaseRefInvalid is a base_ref that cannot be a base: unknown to the
	// host, a branch, or on a ticket slop-haus cannot place in one repository.
	// The error's text says which.
	ErrBaseRefInvalid = errors.New("ticket: invalid base_ref")
	// ErrBaseRefUnverifiable is a base_ref the code host could not be asked
	// about, so it was neither accepted nor refused.
	ErrBaseRefUnverifiable = errors.New("ticket: the code host could not resolve base_ref")
	// ErrBaseRefLocked is an edit of base_ref on a ticket that has been
	// claimed: its agent has already branched from the base.
	ErrBaseRefLocked = errors.New("ticket: base_ref cannot change once the ticket has been claimed")
)

// BaseTagPrefix names the tag that keeps a ticket's base commit reachable:
// BaseTagPrefix + the ticket's id.
const BaseTagPrefix = "slop-haus/base/"

// resolvedBase is a base_ref the host resolved, with the repository it was
// resolved in.
type resolvedBase struct {
	ref, sha string
	kind     codehost.RefKind
	host     *ticketHost
}

// resolveBaseRef resolves ref in the one repository t's paths place it in.
// Unlike PinBase it refuses rather than falls back: a filer who named a base
// must not silently get the default branch.
func (s *Service) resolveBaseRef(ctx context.Context, t *Ticket, ref string) (*resolvedBase, error) {
	ref = strings.TrimSpace(ref)
	host, err := s.hostFor(ctx, t)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrBaseRefUnverifiable, err)
	}
	if host == nil {
		return nil, fmt.Errorf("%w: the ticket's paths must place it in exactly one repository on a code host slop-haus can act on", ErrBaseRefInvalid)
	}
	ctx, cancel := context.WithTimeout(ctx, baseReadTimeout)
	defer cancel()
	resolved, err := host.changes.ResolveRef(ctx, host.tenant, host.repo, ref)
	switch {
	case errors.Is(err, codehost.ErrRefIsBranch):
		return nil, fmt.Errorf("%w: %q is a branch, and a base must not move: name a commit hash or a tag", ErrBaseRefInvalid, ref)
	case errors.Is(err, codehost.ErrChangeNotFound):
		return nil, fmt.Errorf("%w: %q is not a commit or tag in %s/%s", ErrBaseRefInvalid, ref, host.repo.Owner, host.repo.Name)
	case errors.Is(err, codehost.ErrRejected):
		return nil, fmt.Errorf("%w: %q is not a usable commit hash or tag (a short hash may be ambiguous)", ErrBaseRefInvalid, ref)
	case err != nil:
		return nil, fmt.Errorf("%w: %w", ErrBaseRefUnverifiable, err)
	}
	// The sha goes into the worker's instructions (a git command), as in PinBase.
	if !commitID.MatchString(resolved.SHA) {
		return nil, fmt.Errorf("%w: the host's answer is not a commit id", ErrBaseRefUnverifiable)
	}
	return &resolvedBase{ref: ref, sha: resolved.SHA, kind: resolved.Kind, host: host}, nil
}

// protectBase tags the base commit so it stays reachable if the history it
// was on is rewritten. A host refusal is not the ticket's problem: it is
// returned as a warning for the filer.
func (s *Service) protectBase(ctx context.Context, ticketID int64, b *resolvedBase) (warning string) {
	ctx, cancel := context.WithTimeout(ctx, baseReadTimeout)
	defer cancel()
	tag := fmt.Sprintf("%s%d", BaseTagPrefix, ticketID)
	if err := b.host.changes.CreateTag(ctx, b.host.tenant, b.host.repo, tag, b.sha); err != nil {
		s.logger.Warn("ticket: base tag not created", "ticket", ticketID, "tag", tag, "err", err)
		return fmt.Sprintf("the base commit %s was not tagged as %s, so it may not stay reachable if its history is rewritten", b.sha, tag)
	}
	return ""
}

// baseRefWrite is what an edit of base_ref stores; nil sha clears the base
// back to the default branch's head, pinned at the first claim.
type baseRefWrite struct {
	ref, sha *string
	// paths are the scopes the ref was resolved against, checked again in
	// the transaction unless the same edit replaces them.
	paths []string
}

// prepareBaseRefEdit resolves the edit's base_ref, if it has one, so the
// transaction writes only what the host already answered. Empty clears.
func (s *Service) prepareBaseRefEdit(ctx context.Context, id int64, params *UpdateParams) (*resolvedBase, error) {
	params.baseRef = nil
	if params.BaseRef == nil {
		return nil, nil
	}
	if strings.TrimSpace(*params.BaseRef) == "" {
		params.baseRef = &baseRefWrite{}
		return nil, nil
	}
	t, err := s.store.GetTicket(ctx, id)
	if err != nil {
		return nil, err
	}
	if params.Paths != nil {
		t.Paths = params.Paths
	}
	b, err := s.resolveBaseRef(ctx, t, *params.BaseRef)
	if err != nil {
		return nil, err
	}
	params.baseRef = &baseRefWrite{ref: &b.ref, sha: &b.sha}
	if params.Paths == nil {
		params.baseRef.paths = sortedScopePaths(t.Paths)
	}
	return b, nil
}

// writeBaseRef stores an edit of base_ref, in the claim-locked transaction
// of the edit so that no claim can slip between the check and the write.
func writeBaseRef(ctx context.Context, tx db.Querier, id int64, w *baseRefWrite) error {
	var leased bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM attempts a WHERE a.ticket_id = $1 AND (a.replay OR `+attemptEverLeased("a")+`))`, id).Scan(&leased); err != nil {
		return fmt.Errorf("check the ticket has not been claimed: %w", err)
	}
	// A replay's base is the one it was admitted with, claimed yet or not.
	if leased {
		return fmt.Errorf("%w: #%d", ErrBaseRefLocked, id)
	}
	if w.paths != nil {
		rows, err := tx.Query(ctx, `SELECT path FROM ticket_paths WHERE ticket_id = $1 ORDER BY path`, id)
		if err != nil {
			return fmt.Errorf("read the ticket's paths: %w", err)
		}
		now, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return fmt.Errorf("read the ticket's paths: %w", err)
		}
		// A scope edit between the host read and here would leave a commit
		// from the old repository on the ticket.
		if !slices.Equal(now, w.paths) {
			return fmt.Errorf("%w: the ticket's paths changed while base_ref was being resolved; try again", ErrBaseRefInvalid)
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE tickets SET base_ref = $2, base_commit = $3 WHERE id = $1`, id, w.ref, w.sha); err != nil {
		return fmt.Errorf("set the base: %w", err)
	}
	return nil
}

func sortedScopePaths(paths []model.Path) []string {
	out := scopePaths(paths)
	slices.Sort(out)
	return out
}

// wasDrafted is whether a ticket in s had not yet been offered to agents.
func wasDrafted(s Status) bool { return s == StatusPreparing || s == StatusParked }

// noteBehind adds the behind notice to t, a ticket just taken out of
// preparing or parked, if it has one and is no longer drafted.
func (s *Service) noteBehind(ctx context.Context, t *Ticket) {
	if wasDrafted(t.Status) {
		return
	}
	if w := s.behindNotice(ctx, t); w != "" {
		t.Warnings = append(t.Warnings, w)
	}
}

// behindNotice tells the human opening a ticket with an explicit base that
// the base is behind its base branch. It is awareness only: a promoted
// attempt merges the base branch before it lands. A host that cannot answer
// gives no notice, since the ticket is open either way.
func (s *Service) behindNotice(ctx context.Context, t *Ticket) string {
	if t.BaseRef == nil || t.BaseCommit == nil {
		return ""
	}
	host, err := s.hostFor(ctx, t)
	if err != nil || host == nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, baseReadTimeout)
	defer cancel()
	rb, err := s.resolveBase(ctx, host)
	if err != nil {
		s.logger.Warn("ticket: behind notice not made", "ticket", t.ID, "err", err)
		return ""
	}
	// CommitsAhead counts a branch's commits past since, so the base commit
	// is "behind" by the commits the base branch has gained since it.
	n, err := host.changes.CommitsAhead(ctx, host.tenant, host.repo, *t.BaseCommit, rb.Branch)
	if err != nil {
		s.logger.Warn("ticket: behind notice not made", "ticket", t.ID, "err", err)
		return ""
	}
	if n == 0 {
		return ""
	}
	return fmt.Sprintf("this ticket starts from %s, %d commits behind %s; a promoted attempt still merges %s before it lands", *t.BaseRef, n, rb.Branch, rb.Branch)
}
