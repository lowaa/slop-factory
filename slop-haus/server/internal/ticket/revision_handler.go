package ticket

import (
	"context"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
)

// DiffLineBody is one line of a displayed diff.
type DiffLineBody struct {
	Op   string `json:"op" enum:"equal,delete,insert"`
	Text string `json:"text"`
}

// RevisionBody is one edit on the wire: the full old and new text of each
// field it changed (null on both sides for a field it left alone), plus the
// body's diff worked out for display. The diff is derived from the two
// texts beside it on every read, never stored.
type RevisionBody struct {
	ID       int64 `json:"id"`
	Revision int   `json:"revision"`
	// Exactly one of these is set: the agent that made the edit, or the
	// person.
	EditorAgentID *string `json:"editor_agent_id"`
	EditorUserID  *string `json:"editor_user_id"`
	// EditorUserDeleted is true when the editor is a person whose account
	// was deleted: editor_user_id is then null.
	EditorUserDeleted bool    `json:"editor_user_id_deleted,omitempty"`
	OldTitle          *string `json:"old_title"`
	NewTitle          *string `json:"new_title"`
	OldBody           *string `json:"old_body"`
	NewBody           *string `json:"new_body"`
	// BodyDiff is null when the edit left the body alone, rather than an
	// empty diff.
	BodyDiff  []DiffLineBody `json:"body_diff" nullable:"true"`
	CreatedAt time.Time      `json:"created_at"`
}

func revisionToBody(r Revision) RevisionBody {
	body := RevisionBody{
		ID: r.ID, Revision: r.Number,
		OldTitle: r.OldTitle, NewTitle: r.NewTitle, OldBody: r.OldBody, NewBody: r.NewBody,
		CreatedAt: r.CreatedAt, EditorUserDeleted: r.EditorDeleted,
	}
	if r.Editor.AgentID != nil {
		id := r.Editor.AgentID.String()
		body.EditorAgentID = &id
	}
	if r.Editor.UserID != nil {
		id := r.Editor.UserID.String()
		body.EditorUserID = &id
	}
	if r.OldBody != nil && r.NewBody != nil {
		for _, line := range DiffLines(*r.OldBody, *r.NewBody) {
			body.BodyDiff = append(body.BodyDiff, DiffLineBody{Op: string(line.Op), Text: line.Text})
		}
	}
	return body
}

type ListTicketRevisionsRequest struct {
	ID int64 `path:"id"`
}

type ListTicketRevisionsResponse struct {
	Body struct {
		Revisions []RevisionBody `json:"revisions"`
	}
}

func (h *Handler) ListTicketRevisions(ctx context.Context, input *ListTicketRevisionsRequest) (*ListTicketRevisionsResponse, error) {
	revisions, err := h.svc.ListRevisions(ctx, input.ID)
	if err != nil {
		return nil, h.mapError(err)
	}
	resp := &ListTicketRevisionsResponse{}
	resp.Body.Revisions = make([]RevisionBody, len(revisions))
	for i, r := range revisions {
		resp.Body.Revisions[i] = revisionToBody(r)
	}
	return resp, nil
}

// CreateTicketRevisionBody carries no editor: the route is RequireUser, so
// the editor is the signed-in user, taken from the request context.
type CreateTicketRevisionBody struct {
	Title *string `json:"title,omitempty"`
	Body  *string `json:"body,omitempty"`
	// BaseRevision is the ticket's revision the editor was opened on. Sent,
	// it turns an edit made meanwhile by someone else into a 409 instead of
	// a silent overwrite.
	BaseRevision *int `json:"base_revision,omitempty" minimum:"0"`
}

type CreateTicketRevisionRequest struct {
	ID   int64 `path:"id"`
	Body CreateTicketRevisionBody
}

// CreateTicketRevision is a person editing a ticket's title or body. It
// answers with the ticket as edited; an edit that changes nothing records
// no revision and answers with the ticket as it was.
func (h *Handler) CreateTicketRevision(ctx context.Context, input *CreateTicketRevisionRequest) (*TicketResponse, error) {
	user, err := authn.UserFromContext(ctx)
	if err != nil {
		return nil, huma.Error500InternalServerError("ticket route is missing its auth middleware")
	}
	result, err := h.svc.Edit(ctx, input.ID, user.ID, &EditParams{
		Title:        input.Body.Title,
		Body:         input.Body.Body,
		BaseRevision: input.Body.BaseRevision,
	})
	if err != nil {
		return nil, h.mapError(err)
	}
	return &TicketResponse{Body: ticketToBody(*result.Ticket)}, nil
}
