package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/pendingaction"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/steward"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/steward/assist"
)

// draftReader adapts the draft store to pendingaction.Drafts.
type draftReader struct{ store *assist.Store }

func (r *draftReader) Drafts(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]*pendingaction.DraftView, error) {
	found, err := r.store.ByAction(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make(map[uuid.UUID]*pendingaction.DraftView, len(found))
	for id, d := range found {
		v := &pendingaction.DraftView{
			Category: d.Category, Body: d.Body, WouldSendBack: d.WouldSendBack, Reason: d.Reason,
			TicketRevision: d.TicketRevision, HeadSHA: d.HeadSHA, Applied: d.AppliedAt != nil,
		}
		for _, e := range d.Evidence {
			v.Evidence = append(v.Evidence, pendingaction.AssessmentEvidence{Check: e.Check, OK: e.OK, Ref: e.Ref, Detail: e.Detail})
		}
		out[id] = v
	}
	return out, nil
}

// draftApplier is the owner's click on a draft: it refuses anyone who is not
// an owner before assist is asked, and names assist's outcomes in the
// proposal service's terms, which the approvals routes map to responses.
type draftApplier struct {
	assist *assist.Service
	roles  steward.Roles
}

func (a *draftApplier) ApplyDraft(ctx context.Context, owner *authn.User, id uuid.UUID) error {
	if err := authn.RequirePerson(owner); err != nil {
		return err
	}
	isOwner, err := a.roles.IsTenantOwner(ctx, owner.ID, owner.TenantID)
	if err != nil {
		return fmt.Errorf("read role: %w", err)
	}
	if !isOwner {
		return fmt.Errorf("%w: only a tenant owner can apply the steward's draft", pendingaction.ErrNotApprover)
	}
	return mapAssistErr(a.assist.Apply(ctx, owner.ID, id))
}

func mapAssistErr(err error) error {
	switch {
	case errors.Is(err, assist.ErrNotFound):
		return fmt.Errorf("%w: %w", pendingaction.ErrNotFound, err)
	case errors.Is(err, assist.ErrNotApplicable):
		return fmt.Errorf("%w: %w", pendingaction.ErrInvalid, err)
	case errors.Is(err, steward.ErrNotPending), errors.Is(err, assist.ErrAlreadyApplied):
		return fmt.Errorf("%w: %w", pendingaction.ErrNotPending, err)
	case errors.Is(err, assist.ErrNotAssist), errors.Is(err, assist.ErrStale), errors.Is(err, steward.ErrApprovalRefused):
		return fmt.Errorf("%w: %w", pendingaction.ErrRefused, err)
	}
	return err
}
