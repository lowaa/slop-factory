package steward

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
)

var (
	// ErrNotOwner is a veto or undo by someone who does not own the tenant:
	// the steward acts for the owner, so only they take its decisions back.
	ErrNotOwner = errors.New("steward: only a tenant owner can veto or undo a steward decision")
	// ErrNotRevertable is an application that cannot be taken back, because
	// the kind has no inverse or the ticket moved on since.
	ErrNotRevertable = errors.New("steward: the decision can no longer be taken back")
)

// Roles says whether a person owns a tenant.
type Roles interface {
	IsTenantOwner(ctx context.Context, userID, tenantID uuid.UUID) (bool, error)
}

// Reverter takes back what the steward applied for a proposal, as by. It
// reports ErrNotRevertable for one that cannot be.
type Reverter interface {
	RevertStewardApproval(ctx context.Context, by *authn.User, proposalID uuid.UUID) error
}

// Controls is the owner's side of live mode: cancelling a decision inside its
// veto window and reverting one already applied. Both are audited and mark the
// assessment, which the agreement report counts as a disagreement whatever
// the owner's reason.
type Controls struct {
	store    *Store
	roles    Roles
	reverter Reverter
	comments Commenter
	events   *audit.Recorder
	now      func() time.Time
	logger   *slog.Logger
}

// NewControls wires the controls. comments and events may be nil; now is
// injectable for tests.
func NewControls(logHandler slog.Handler, store *Store, roles Roles, reverter Reverter, comments Commenter, events *audit.Recorder, now func() time.Time) *Controls {
	if now == nil {
		now = time.Now
	}
	return &Controls{store: store, roles: roles, reverter: reverter, comments: comments, events: events, now: now, logger: slog.New(logHandler)}
}

// record finds the proposal's assessment once owner is known to own the tenant
// in ctx.
func (c *Controls) record(ctx context.Context, owner *authn.User, proposalID uuid.UUID) (*Record, error) {
	if err := authn.RequirePerson(owner); err != nil {
		return nil, err
	}
	isOwner, err := c.roles.IsTenantOwner(ctx, owner.ID, owner.TenantID)
	if err != nil {
		return nil, fmt.Errorf("steward: read role: %w", err)
	}
	if !isOwner {
		return nil, ErrNotOwner
	}
	return c.store.GetByAction(ctx, proposalID)
}

// Veto cancels a decision the steward has not applied yet. The proposal stays
// pending and is the owner's to decide. ErrNotApplicable when the steward
// applied it already, or it is voided, vetoed or decided.
func (c *Controls) Veto(ctx context.Context, owner *authn.User, proposalID uuid.UUID) error {
	rec, err := c.record(ctx, owner, proposalID)
	if err != nil {
		return err
	}
	if err := c.store.Veto(ctx, rec.ID, owner.ID, c.now()); err != nil {
		return err
	}
	c.audit(ctx, audit.StewardVetoed, owner, rec)
	postComment(ctx, c.comments, c.logger, rec, vetoedComment(rec))
	return nil
}

// Undo reverts a decision the steward applied, as far as the ticket still
// allows (see Reverter), and records it as undone. ErrNotApplicable when
// there is nothing applied to undo.
func (c *Controls) Undo(ctx context.Context, owner *authn.User, proposalID uuid.UUID) error {
	rec, err := c.record(ctx, owner, proposalID)
	if err != nil {
		return err
	}
	if rec.AppliedAt == nil || rec.UndoneAt != nil {
		return ErrNotApplicable
	}
	if err := c.reverter.RevertStewardApproval(ctx, owner, proposalID); err != nil {
		return err
	}
	// The revert has committed; a failure here must not hide it.
	if err := c.store.MarkUndone(ctx, rec.ID, c.now()); err != nil {
		return fmt.Errorf("steward: the decision was reverted but not recorded as undone: %w", err)
	}
	c.audit(ctx, audit.StewardUndone, owner, rec)
	return nil
}

func (c *Controls) audit(ctx context.Context, action audit.Action, owner *authn.User, rec *Record) {
	if c.events == nil {
		return
	}
	c.events.Record(ctx, action, "proposal", rec.PendingActionID, "assessment", rec.ID, "category", rec.Category,
		"ticket", rec.TicketID, "user_id", owner.ID, "tenant", owner.TenantID)
}
