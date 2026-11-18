package mcpserver

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

// ticketDetail is ticket_get's answer: the ticket plus what a resuming agent
// needs to carry on -- the conversation with the human, and the handoff the
// last agent left. Without both, picking a ticket up after a discussion
// means starting over.
type ticketDetail struct {
	ticketRef
	heldView
	toolNotes
	Comments []commentRef `json:"comments,omitempty"`
	// Attachments are images and videos on the ticket; ticket_attachment_get reads one.
	Attachments []attachmentRef `json:"attachments,omitempty"`
	Handoff     string          `json:"handoff,omitempty"`
	// Edits are the latest changes to the title and description, so an
	// agent can tell what the ticket used to ask for.
	Edits        []editRef `json:"edits,omitempty"`
	EarlierEdits int       `json:"earlier_edits,omitempty"`
	// ExternalContent is what an external system sent about this ticket,
	// already rendered as fenced external content. It is the only form it
	// is given in: raw fields here would be text a stranger controls with
	// nothing to say so.
	ExternalContent string `json:"external_content,omitempty"`
	// Discussions is the steward's label on each open discussion round. It
	// is given to the owner's side (a conductor or console) only, unless the
	// server is set to show workers too.
	Discussions []discussionLabelRef `json:"discussions,omitempty"`
}

type discussionLabelRef struct {
	Attempt  int    `json:"attempt"`
	Round    int    `json:"round"`
	Label    string `json:"label,omitempty"`
	Category string `json:"category,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

// deletedUserLabel names a person whose account is gone; their id and name
// are not kept, and "human" would hide that they once had one.
const deletedUserLabel = "deleted user"

type commentRef struct {
	// Author is "human", "deleted user" for a person whose account was
	// deleted, or the agent's name.
	Author string `json:"author"`
	// At is the server's time for the comment, so an agent reading its own
	// thread times a wait from it rather than from a guess.
	At   string `json:"at"`
	Body string `json:"body"`
}

func (h *Handler) ticketGet(ctx context.Context, agentID uuid.UUID, in ticketIDInput) (*mcp.CallToolResult, ticketDetail, error) {
	read, err := h.tickets.DetailFor(ctx, in.TicketID, agentID)
	if err != nil {
		return nil, ticketDetail{}, err
	}
	found := read.Ticket

	guidance := ticket.GuidanceFor(found, read.Branch)
	detail := ticketDetail{ticketRef: h.toRef(ctx, found), heldView: heldView{Guidance: guidance.OrNil()}}
	if read.Branch != nil && read.Held != nil {
		detail.Attempt = ticket.NewAttemptView(found, read.Held.Number, read.Branch)
	}
	detail.Discussions = h.discussionLabels(ctx, agentID, found)
	var b strings.Builder
	fmt.Fprintf(&b, "#%d %s [%s, revision %d]", detail.ID, detail.Title, detail.Status, detail.Revision)
	if detail.NeedsDiscussion {
		b.WriteString(" (needs discussion: plan and ask, don't build)")
	}
	fmt.Fprintf(&b, "\n%s\nScopes: %s", detail.Body, joinOrNone(detail.Paths))
	if detail.URL != "" {
		fmt.Fprintf(&b, "\nLink: %s", detail.URL)
	}
	if found.Intake != nil {
		b.WriteString("\n" + guidance.External)
		detail.ExternalContent = ticket.RenderExternal(read.External, h.boundary)
		if detail.ExternalContent != "" {
			b.WriteString("\n\n" + detail.ExternalContent)
		}
	}
	if len(detail.Roles) > 0 {
		fmt.Fprintf(&b, "\nClaimable only by an agent with %s", ticket.DescribeRoles(detail.Roles, model.RoleMatch(detail.RoleMatch)))
	}
	if len(detail.BlockedBy) > 0 {
		fmt.Fprintf(&b, "\nBlocked by: %s", blockedByLine(detail.BlockedBy))
	}
	if detail.PRURL != "" {
		fmt.Fprintf(&b, "\nPR: %s", detail.PRURL)
	}
	for _, d := range detail.Discussions {
		b.WriteString("\n" + d.line())
	}
	if detail.Attempt != nil {
		fmt.Fprintf(&b, "\n%s", detail.Attempt.Guidance)
	}
	for _, line := range []string{guidance.DoneThroughOther, guidance.Review, guidance.Parked, guidance.MergeQueue} {
		if line != "" {
			b.WriteString("\n" + line)
		}
	}

	names := map[uuid.UUID]string{}
	for _, c := range read.Comments {
		ref := commentRef{Author: "human", At: c.CreatedAt.UTC().Format(time.RFC3339), Body: c.Body}
		if c.AuthorAgentID != nil {
			ref.Author = h.agentName(ctx, *c.AuthorAgentID, names)
		} else if c.AuthorUserDeleted {
			ref.Author = deletedUserLabel
		}
		detail.Comments = append(detail.Comments, ref)
	}
	detail.Attachments = h.ticketAttachments(ctx, agentID, found.ID)
	if len(detail.Attachments) > 0 {
		b.WriteString("\n\nAttachments (ticket_attachment_get reads one; " + untrustedMediaNote + "):")
		for i := range detail.Attachments {
			b.WriteString("\n- " + detail.Attachments[i].line())
		}
	}
	detail.Edits, detail.EarlierEdits = h.editRefs(ctx, read.Revisions, names)
	writeEdits(&b, detail.Edits, detail.EarlierEdits)
	if len(detail.Comments) > 0 {
		b.WriteString("\n\nComments:")
		for _, c := range detail.Comments {
			fmt.Fprintf(&b, "\n- %s (%s): %s", c.Author, c.At, c.Body)
		}
	}
	if read.Handoff != nil {
		detail.Handoff = read.Handoff.Body
		fmt.Fprintf(&b, "\n\nHandoff from %s -- resume from here:\n%s",
			h.agentName(ctx, read.Handoff.AgentID, names), read.Handoff.Body)
	}
	return text("%s", b.String()), detail, nil
}

// agentName resolves an agent for display, falling back to its id: a name
// that cannot be looked up should not cost the reader the comment.
func (h *Handler) agentName(ctx context.Context, id uuid.UUID, cache map[uuid.UUID]string) string {
	if name, ok := cache[id]; ok {
		return name
	}
	name, err := h.agents.Name(ctx, id)
	if err != nil {
		name = id.String()
	}
	cache[id] = name
	return name
}

// discussionLabels reads the steward's label on the ticket's open discussion
// rounds (the reader returns only attempts in discussion, whatever the
// ticket's own status), for a caller allowed to see it. A failed read costs the reader the
// label, not the ticket.
func (h *Handler) discussionLabels(ctx context.Context, agentID uuid.UUID, found *ticket.Ticket) []discussionLabelRef {
	if h.discussions == nil {
		return nil
	}
	if !h.labelToWorkers {
		if h.proposers == nil {
			return nil
		}
		if may, err := h.proposers.MayPropose(ctx, agentID); err != nil || !may {
			return nil
		}
	}
	open, err := h.discussions.AttemptDiscussions(ctx, found.ID)
	if err != nil {
		h.logger.WarnContext(ctx, "read discussion labels", "ticket", found.ID, "error", err)
		return nil
	}
	var out []discussionLabelRef
	for attempt, d := range open {
		ref := discussionLabelRef{Attempt: attempt, Round: d.Round, Category: d.Category, Reason: d.Reason}
		if d.Label != nil {
			ref.Label = *d.Label
		}
		out = append(out, ref)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Attempt < out[j].Attempt })
	return out
}

// line says the label in words. A routine one points at the steward's draft,
// which is what the conductor guidance asks of the conductor.
func (d discussionLabelRef) line() string {
	switch d.Label {
	case "routine":
		return fmt.Sprintf("Discussion (attempt %d, round %d): labelled routine by the steward, category %s. "+
			"Point the owner to \"Answer with steward\" on the ticket rather than writing an answer, unless they insist.",
			d.Attempt, d.Round, d.Category)
	case "yours", "not_assessed":
		return fmt.Sprintf("Discussion (attempt %d, round %d): labelled %s by the steward (%s).", d.Attempt, d.Round, d.Label, d.Reason)
	case "pending":
		return fmt.Sprintf("Discussion (attempt %d, round %d): waiting for the steward to label it (steward_label_discussion).", d.Attempt, d.Round)
	}
	return fmt.Sprintf("Discussion (attempt %d, round %d): not labelled yet.", d.Attempt, d.Round)
}
