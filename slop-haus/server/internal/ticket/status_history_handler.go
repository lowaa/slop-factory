package ticket

import (
	"context"
	"time"
)

// StatusCauseBody is a status change's cause on the wire. proposal_id and
// auto_approved are set for kind proposal, text for kind other.
type StatusCauseBody struct {
	Kind         string  `json:"kind" enum:"proposal,other"`
	ProposalID   *string `json:"proposal_id,omitempty" format:"uuid"`
	AutoApproved bool    `json:"auto_approved,omitempty"`
	Text         *string `json:"text,omitempty"`
}

// StatusChangeBody is one status change on the wire.
type StatusChangeBody struct {
	ID int64 `json:"id"`
	// FromStatus is null for the change recorded at creation.
	FromStatus *Status   `json:"from_status" nullable:"true"`
	ToStatus   Status    `json:"to_status"`
	ChangedAt  time.Time `json:"changed_at"`
	ActorKind  string    `json:"actor_kind" enum:"agent,user,system"`
	// ActorID is null for the system, for a person the route did not know,
	// and for a deleted account (ActorIDDeleted).
	ActorID        *string `json:"actor_id" nullable:"true"`
	ActorIDDeleted bool    `json:"actor_id_deleted,omitempty"`
	// Cause is absent where none was recorded.
	Cause *StatusCauseBody `json:"cause,omitempty"`
}

func statusChangeToBody(c StatusChange) StatusChangeBody {
	body := StatusChangeBody{
		ID: c.ID, FromStatus: c.FromStatus, ToStatus: c.ToStatus, ChangedAt: c.ChangedAt,
		ActorKind: c.ActorKind, ActorIDDeleted: c.ActorDeleted,
	}
	if c.ActorID != nil {
		id := c.ActorID.String()
		body.ActorID = &id
	}
	if c.Cause != nil {
		cause := &StatusCauseBody{Kind: string(c.Cause.Kind), AutoApproved: c.Cause.AutoApproved}
		if c.Cause.ProposalID != nil {
			id := c.Cause.ProposalID.String()
			cause.ProposalID = &id
		}
		if c.Cause.Kind == CauseOther {
			text := c.Cause.Text
			cause.Text = &text
		}
		body.Cause = cause
	}
	return body
}

type ListTicketStatusHistoryRequest struct {
	ID int64 `path:"id"`
}

type ListTicketStatusHistoryResponse struct {
	Body struct {
		Changes []StatusChangeBody `json:"changes"`
	}
}

func (h *Handler) ListTicketStatusHistory(ctx context.Context, input *ListTicketStatusHistoryRequest) (*ListTicketStatusHistoryResponse, error) {
	changes, err := h.svc.ListStatusChanges(ctx, input.ID)
	if err != nil {
		return nil, h.mapError(err)
	}
	resp := &ListTicketStatusHistoryResponse{}
	resp.Body.Changes = make([]StatusChangeBody, len(changes))
	for i, c := range changes {
		resp.Body.Changes[i] = statusChangeToBody(c)
	}
	return resp, nil
}
