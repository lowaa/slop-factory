package pendingaction

import (
	"context"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
)

// StewardControls is the owner's veto and undo of a decision the steward made
// for a proposal. An implementation answers with this package's errors:
// ErrNotFound (no steward decision for it), ErrNotApprover (not an owner),
// ErrNotPending (nothing left to veto or undo) and ErrNotRevertable.
type StewardControls interface {
	Veto(ctx context.Context, owner *authn.User, proposalID uuid.UUID) error
	Undo(ctx context.Context, owner *authn.User, proposalID uuid.UUID) error
}

// WithStewardControls serves the veto and undo routes. Without it they answer
// with a server error.
func (h *Handler) WithStewardControls(c StewardControls) *Handler {
	h.controls = c
	return h
}

func (h *Handler) stewardControl(ctx context.Context, in *ProposalIDRequest, act func(StewardControls, context.Context, *authn.User, uuid.UUID) error) (*ProposalResponse, error) {
	who := authn.PersonFromContext(ctx)
	id, err := parseID(in.ID)
	if err != nil {
		return nil, err
	}
	if h.controls == nil {
		return nil, h.mapError(ErrNotConfigured)
	}
	if err := act(h.controls, ctx, who, id); err != nil {
		return nil, h.mapError(err)
	}
	v, err := h.svc.ViewOf(ctx, who, id)
	if err != nil {
		return nil, h.mapError(err)
	}
	return &ProposalResponse{Body: toBody(v)}, nil
}

func (h *Handler) VetoSteward(ctx context.Context, in *ProposalIDRequest) (*ProposalResponse, error) {
	return h.stewardControl(ctx, in, StewardControls.Veto)
}

func (h *Handler) UndoSteward(ctx context.Context, in *ProposalIDRequest) (*ProposalResponse, error) {
	return h.stewardControl(ctx, in, StewardControls.Undo)
}
