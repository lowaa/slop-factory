package manifest

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
)

// A repo's path is also the prefix of every ticket scope and intake source
// path under it (those are workspace-relative), so changing it alone would
// leave open tickets under no repo and, mid-switchover, let old and new
// spellings of one file pass as not overlapping. Moving a repo rewrites them
// in the same transaction.

// ErrMoveBlocked is a move refused because tickets under the repo are being
// worked; the error is a *MoveBlockedError naming them.
var ErrMoveBlocked = errors.New("manifest: the repo has work in flight")

// MoveBlockedError carries the tickets that stopped a move.
type MoveBlockedError struct{ Blockers []MoveBlocker }

func (e *MoveBlockedError) Error() string {
	parts := make([]string, 0, len(e.Blockers))
	for _, b := range e.Blockers {
		parts = append(parts, fmt.Sprintf("#%d (%s)", b.TicketID, b.Reason))
	}
	return fmt.Sprintf("%v: stop the fleet first; blocked by %s", ErrMoveBlocked, strings.Join(parts, ", "))
}

func (e *MoveBlockedError) Unwrap() error { return ErrMoveBlocked }

const (
	BlockerLease = "live lease"
	BlockerLock  = "holds the integration lock"
)

// MoveBlocker is a ticket under the repo that has a live lease, or holds the
// integration lock.
type MoveBlocker struct {
	TicketID int64  `json:"ticket_id"`
	Title    string `json:"title"`
	Reason   string `json:"reason" enum:"live lease,holds the integration lock"`
}

// MovedPath is one scope or intake path and where it goes.
type MovedPath struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// MovedTicket is a non-terminal ticket whose scopes are rewritten.
type MovedTicket struct {
	ID     int64       `json:"id"`
	Title  string      `json:"title"`
	Status string      `json:"status"`
	Paths  []MovedPath `json:"paths"`
}

// MovedIntakeSource is an intake source whose paths are rewritten.
type MovedIntakeSource struct {
	ID    uuid.UUID   `json:"id"`
	Name  string      `json:"name"`
	Paths []MovedPath `json:"paths"`
}

// MovePlan is what a move changes: shown as a dry run, and what a real move
// reports having done.
type MovePlan struct {
	From          string              `json:"from_path"`
	To            string              `json:"to_path"`
	Repos         []Repo              `json:"repos" doc:"The manifest after the move."`
	Tickets       []MovedTicket       `json:"tickets"`
	IntakeSources []MovedIntakeSource `json:"intake_sources"`
	Warnings      []string            `json:"warnings"`
}

// MoveRequest names the repo by its current path.
type MoveRequest struct {
	From string
	To   string
}

// Mover is the store's half of a move. It reads and rewrites in one
// transaction under the manifest's revision lock; with dryRun it writes
// nothing. Blockers are refused with *MoveBlockedError, also on a dry run.
type Mover interface {
	MoveRepo(ctx context.Context, by uuid.UUID, req MoveRequest, dryRun bool) (*MovePlan, error)
}

// Move changes a repo's path and the scopes under it. Owners only.
func (s *Service) Move(ctx context.Context, who *authn.User, req MoveRequest, dryRun bool) (*MovePlan, error) {
	if err := s.gate.RequireOwner(ctx, who); err != nil {
		return nil, err
	}
	mover, ok := s.store.(Mover)
	if !ok {
		return nil, errors.New("manifest: this store cannot move a repo")
	}
	req.From, req.To = strings.TrimSpace(req.From), strings.TrimSpace(req.To)
	plan, err := mover.MoveRepo(ctx, who.ID, req, dryRun)
	if err != nil {
		return nil, err
	}
	if !dryRun && s.events != nil {
		s.events.Record(ctx, audit.RepoMoved, "user_id", who.ID, "tenant", who.TenantID,
			"from", plan.From, "to", plan.To, "tickets", len(plan.Tickets), "intake_sources", len(plan.IntakeSources))
	}
	return plan, nil
}

// planRepos validates the move against the manifest and returns the
// manifest it leaves.
func planRepos(current []Repo, req MoveRequest) ([]Repo, []string, error) {
	if err := validPath(req.To); err != nil {
		return nil, nil, invalid("to_path: %v", err)
	}
	if req.From == req.To {
		return nil, nil, invalid("the repo is already at %q", req.To)
	}
	next := make([]Repo, len(current))
	copy(next, current)
	found := false
	for i := range next {
		switch next[i].Path {
		case req.From:
			next[i].Path = req.To
			found = true
		case req.To:
			return nil, nil, invalid("path %q is already another repo's", req.To)
		}
	}
	if !found {
		return nil, nil, invalid("no repo of the manifest is at %q", req.From)
	}
	next, err := Normalise(next)
	if err != nil {
		return nil, nil, err
	}
	return next, []string{}, nil
}

// movePath is where a workspace-relative scope under the repo goes.
func movePath(p string, req MoveRequest) (string, error) {
	rel, dir := p, strings.HasSuffix(p, "/")
	switch {
	case p == req.From || p == req.From+"/":
		rel = ""
	default:
		rel = strings.TrimPrefix(p, req.From+"/")
	}
	switch {
	case rel == "" && !dir:
		return req.To, nil
	case rel == "":
		return req.To + "/", nil
	}
	return req.To + "/" + rel, nil
}

// ownedBy reports whether a scope belongs to the repo at path, by the
// manifest before the move.
func ownedBy(repos []Repo, p, path string) bool {
	r := RepoFor(repos, p)
	return r != nil && r.Path == path
}
