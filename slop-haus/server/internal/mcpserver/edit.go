package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// ticketEditInput names no ticket: the tool edits the one the agent holds,
// so there is no way to point it at anyone else's.
//
// Revision is required: a human can edit the ticket while the agent works
// it, and a body sent without knowing that would silently revert their
// change. With it, an edit made against stale text is refused instead.
type ticketEditInput struct {
	Revision int     `json:"revision" jsonschema:"the ticket's revision as ticket_get last showed it; refused if the ticket has been edited since"`
	Title    *string `json:"title,omitempty" jsonschema:"the new title"`
	Body     *string `json:"body,omitempty" jsonschema:"the new description, in full: it replaces the old one"`
}

type ticketEditOutput struct {
	toolNotes
	Revision int `json:"revision"`
}

func (h *Handler) ticketEdit(ctx context.Context, agentID uuid.UUID, in ticketEditInput) (*mcp.CallToolResult, ticketEditOutput, error) {
	result, err := h.tickets.EditHeld(ctx, agentID, &ticket.EditParams{
		Title: in.Title, Body: in.Body, BaseRevision: &in.Revision,
	})
	if errors.Is(err, ticket.ErrStaleRevision) {
		return nil, ticketEditOutput{}, fmt.Errorf("%w. Read it again with ticket_get, and make your edit on top of what it says now", err)
	}
	if err != nil {
		return nil, ticketEditOutput{}, h.leaseLost(ctx, agentID, err)
	}
	edited := result.Ticket
	if !result.Changed {
		// Not progress: counting a restatement of the current text would let
		// an agent looping on it evade the stop-hook loop breaker.
		return text("Nothing changed: #%d already says that (revision %d).", edited.ID, edited.Revision),
			ticketEditOutput{Revision: edited.Revision}, nil
	}
	h.progress(ctx, agentID)
	return text("Edited #%d (revision %d).", edited.ID, edited.Revision),
		ticketEditOutput{Revision: edited.Revision}, nil
}

// editRef is one edit as ticket_get shows it. Every agent that reads the
// ticket carries this, so it says what changed rather than repeating the
// full texts the revision stores: the title's old and new, and only the
// changed lines of the description.
type editRef struct {
	Revision int    `json:"revision"`
	By       string `json:"by"`
	At       string `json:"at"`
	Title    string `json:"title,omitempty"`
	Body     string `json:"body,omitempty"`
}

const (
	// editsShown is how many of the latest edits ticket_get lists. Older
	// ones are counted, not shown: the current text already reflects
	// them, and the full history is on the board.
	editsShown = 5
	// diffLinesShown and diffLineRunes cap one edit's description diff, so
	// a rewritten description costs a resuming agent a few lines, not two
	// copies of itself.
	diffLinesShown = 12
	diffLineRunes  = 160
)

// editTimeLayout is minute precision in UTC. An agent has no reliable local
// zone to read a time in, and seconds are noise in a line the model carries
// in context.
const editTimeLayout = "2006-01-02 15:04Z"

// editRefs renders a ticket's latest edits for an agent, and how many
// earlier ones were left out.
func (h *Handler) editRefs(ctx context.Context, revisions []ticket.Revision, names map[uuid.UUID]string) ([]editRef, int) {
	omitted := max(len(revisions)-editsShown, 0)
	refs := make([]editRef, 0, len(revisions)-omitted)
	for _, r := range revisions[omitted:] {
		ref := editRef{Revision: r.Number, By: "human", At: r.CreatedAt.UTC().Format(editTimeLayout)}
		if r.Editor.AgentID != nil {
			ref.By = h.agentName(ctx, *r.Editor.AgentID, names)
		} else if r.EditorDeleted {
			ref.By = deletedUserLabel
		}
		if r.OldTitle != nil && r.NewTitle != nil {
			ref.Title = fmt.Sprintf("%q -> %q", *r.OldTitle, *r.NewTitle)
		}
		if r.OldBody != nil && r.NewBody != nil {
			ref.Body = compactDiff(ticket.DiffLines(*r.OldBody, *r.NewBody))
		}
		refs = append(refs, ref)
	}
	return refs, omitted
}

// compactDiff is the changed lines of a diff, marked - and +, with no
// context: the agent has the current description in full just above.
func compactDiff(diff []ticket.DiffLine) string {
	var b strings.Builder
	shown, changed := 0, 0
	for _, line := range diff {
		var mark string
		switch line.Op {
		case ticket.DiffDelete:
			mark = "- "
		case ticket.DiffInsert:
			mark = "+ "
		default:
			continue
		}
		changed++
		if shown == diffLinesShown {
			continue
		}
		shown++
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(mark + truncateRunes(line.Text, diffLineRunes))
	}
	if changed > shown {
		fmt.Fprintf(&b, "\n(%d more changed lines)", changed-shown)
	}
	return b.String()
}

func truncateRunes(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "…"
}

// writeEdits appends ticket_get's text rendering of the edits.
func writeEdits(b *strings.Builder, edits []editRef, omitted int) {
	if len(edits) == 0 {
		return
	}
	b.WriteString("\n\nEdits to the title/description, oldest first:")
	if omitted > 0 {
		fmt.Fprintf(b, " (%d earlier not shown)", omitted)
	}
	for _, e := range edits {
		fmt.Fprintf(b, "\n- r%d by %s at %s:", e.Revision, e.By, e.At)
		if e.Title != "" {
			fmt.Fprintf(b, " title %s", e.Title)
		}
		if e.Body != "" {
			b.WriteString(" description:\n  " + strings.ReplaceAll(e.Body, "\n", "\n  "))
		}
	}
}
