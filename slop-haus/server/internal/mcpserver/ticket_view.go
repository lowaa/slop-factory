package mcpserver

import (
	"context"
	"strings"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

type ticketRef struct {
	ID       int64    `json:"id"`
	Title    string   `json:"title"`
	Body     string   `json:"body,omitempty"`
	Status   string   `json:"status"`
	Priority string   `json:"priority"`
	Paths    []string `json:"paths"`
	// Revision is what ticket_edit takes, to refuse an edit made against
	// text someone has changed since.
	Revision int `json:"revision"`
	// NeedsDiscussion means plan and ask, don't build.
	NeedsDiscussion bool `json:"needs_discussion,omitempty"`
	// PRURL is the ticket's PR: the one in review, or the one it was
	// merged through.
	PRURL string `json:"pr_url,omitempty"`
	// BaseCommit is the main commit pinned for the ticket when it was first
	// claimed: where its branch starts. Absent when the code host could not
	// be read then.
	BaseCommit string `json:"base_commit,omitempty"`
	// URL is the ticket's page on the board, which the PR description
	// links back to.
	URL string `json:"url,omitempty"`
	// RequiresApproval means open a PR and call ticket_finish; don't merge
	// until a human approves.
	RequiresApproval bool `json:"requires_approval,omitempty"`
	// Review is the latest review's outcome: approved or changes_requested.
	Review string `json:"review,omitempty"`
	// BlockedBy are the tickets this one waits on, finished or not.
	BlockedBy []blockerRef `json:"blocked_by,omitempty"`
	// Roles are what an agent needs to claim it -- any one of them, or all,
	// as RoleMatch says.
	Roles     []string `json:"roles,omitempty"`
	RoleMatch string   `json:"role_match,omitempty"`
}

func (h *Handler) toRef(ctx context.Context, t *ticket.Ticket) ticketRef {
	paths := make([]string, 0, len(t.Paths))
	for _, p := range t.Paths {
		paths = append(paths, p.Path)
	}
	ref := ticketRef{
		ID: t.ID, Title: t.Title, Body: t.Body,
		Status: string(t.Status), Priority: string(t.Priority), Paths: paths, Revision: t.Revision,
		NeedsDiscussion: t.NeedsDiscussion, RequiresApproval: t.RequiresApproval,
		Review:    string(t.ReviewOutcome()),
		BlockedBy: toBlockerRefs(t.Blockers),
		URL:       h.ticketURL(ctx, t.ID),
	}
	if t.PRURL != nil {
		ref.PRURL = *t.PRURL
	}
	if t.BaseCommit != nil {
		ref.BaseCommit = *t.BaseCommit
	}
	if len(t.RequiredRoles) > 0 {
		ref.Roles = make([]string, len(t.RequiredRoles))
		for i, r := range t.RequiredRoles {
			ref.Roles[i] = r.Name
		}
		ref.RoleMatch = string(t.RoleMatch)
	}
	return ref
}

// heldView is what a tool that hands over a ticket adds to the ticket's own
// fields: the attempt held and the guidance on it.
type heldView struct {
	// Attempt is the attempt you hold; absent when you hold none.
	Attempt *ticket.AttemptView `json:"attempt,omitempty"`
	// Guidance is absent when none of it applies.
	Guidance *ticket.Guidance `json:"guidance,omitempty"`
}

func joinOrNone(paths []string) string {
	if len(paths) == 0 {
		return "(none declared)"
	}
	return strings.Join(paths, ", ")
}
