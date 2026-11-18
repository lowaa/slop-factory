package change

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/scope"
)

// NarrowScopeToChange narrows the held ticket's scope to the files its change
// touches, read from the code host (ticket.ScopeNarrowing). It is for the
// hand-off points, where the worker is done with the ticket: a directory
// claimed "in case" would otherwise idle every ticket under it until a human
// acts. Nil, with nothing changed, when the ticket has no change; an error
// when the files could not be read, which the caller logs and goes on from.
func (s *Service) NarrowScopeToChange(ctx context.Context, tenant, agentID uuid.UUID) (*ticket.NarrowResult, error) {
	if err := tenantOf(ctx, tenant); err != nil {
		return nil, err
	}
	gate, err := s.tickets.ChangeGateFor(ctx, agentID)
	if err != nil {
		return nil, err
	}
	rec, err := s.store.Current(ctx, gate.Attempt.TicketID, gate.Attempt.Number)
	if errors.Is(err, ErrNoChange) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if rec.MergedAt != nil {
		return nil, nil
	}
	tgt, err := s.targetFor(ctx, gate.Ticket)
	if err != nil {
		return nil, err
	}
	checks, err := s.readChecks(ctx, tenant, agentID, rec, "narrow_scope")
	if err != nil {
		return nil, err
	}
	if checks.HeadSHA == "" {
		return nil, fmt.Errorf("%w: the host named no head commit", ErrInvalid)
	}
	diff, err := tgt.changes.Diff(ctx, tenant, rec.Change, checks.HeadSHA)
	if err != nil {
		return nil, err
	}
	if diff.Base != tgt.base {
		return nil, fmt.Errorf("%w: the change targets %q, not %q", ErrInvalid, diff.Base, tgt.base)
	}
	changed := make([]string, 0, len(diff.Files))
	for i := range diff.Files {
		f := &diff.Files[i]
		changed = append(changed, tgt.repoPath+"/"+f.Path)
		if f.Status == codehost.DiffRenamed && f.PreviousPath != "" {
			changed = append(changed, tgt.repoPath+"/"+f.PreviousPath)
		}
	}
	shared := scope.NewSharedPaths([]scope.WorkspaceRepo{{Path: tgt.repoPath, Shared: tgt.shared}})
	return s.tickets.NarrowToChange(ctx, agentID, gate.Ticket.ID, changed, shared)
}
