package mcpserver

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

// searchLimit is how many rows ticket_search shows. Rows are one line each,
// but a word common enough to match more than this is a search to narrow,
// not a list to read.
const searchLimit = 50

// ticket_search is the board's text filter (?q=, ?done=) plus a read-only
// `statuses` narrowing the board has no equivalent for. A listed ticket is
// shown in the ticket's own status, one row per ticket: not the per-attempt
// cards the board's in progress, discussion and review columns show, so a
// ticket with several attempts is one row here.
//
// The property is `statuses` and TestNoAgentToolTakesAStatusButTicketCreate
// guards it as well as `status`, allowlisting this tool as a read filter:
// nothing here moves a ticket.
type ticketSearchInput struct {
	Text        string   `json:"text,omitempty" jsonschema:"words that must each appear, in any case, somewhere in a ticket's title, description, comments or handoff; leave out to list by statuses alone"`
	Statuses    []string `json:"statuses,omitempty" jsonschema:"only tickets in exactly these statuses, e.g. [\"preparing\"]; the whole set, so [\"done\"] alone lists done ones. cancelled is refused. Not with include_done"`
	IncludeDone bool     `json:"include_done,omitempty" jsonschema:"also search done tickets; not with statuses"`
}

// searchRow is deliberately not ticketRef: a body per row would put every
// matching ticket's description into the agent's context, when the point
// is to find which one to ticket_get.
type searchRow struct {
	ID       int64  `json:"id"`
	Title    string `json:"title"`
	Status   string `json:"status"`
	Priority string `json:"priority"`
}

type ticketSearchOutput struct {
	toolNotes
	Tickets []searchRow `json:"tickets"`
	// More means there were more matches than shown.
	More bool `json:"more,omitempty"`
}

// searchStatuses is what the board shows, done only when asked -- except
// that parked is always in. The board hides parked tickets to keep its
// columns about planned work; a search is how an agent finds an idea
// before filing it again, which is exactly what parked tickets are kept
// for. Each is marked, so none reads as work to take.
func searchStatuses(includeDone bool) []ticket.Status {
	out := []ticket.Status{}
	for _, st := range model.Statuses() {
		if st == ticket.StatusCancelled || (st == ticket.StatusDone && !includeDone) {
			continue
		}
		out = append(out, st)
	}
	return out
}

// requestedStatuses parses `statuses`, refusing a name the board does not
// have rather than returning an empty result that reads as "none exist".
func requestedStatuses(raw []string) ([]ticket.Status, error) {
	out := make([]ticket.Status, 0, len(raw))
	for _, name := range raw {
		st := ticket.Status(name)
		if st == ticket.StatusCancelled {
			return nil, fmt.Errorf("cancelled tickets are not searchable")
		}
		if !st.Valid() {
			return nil, fmt.Errorf("unknown status %q: use one of %s", name, statusNames())
		}
		out = append(out, st)
	}
	return out, nil
}

func statusNames() string {
	names := []string{}
	for _, st := range model.Statuses() {
		names = append(names, string(st))
	}
	return strings.Join(names, ", ")
}

func (h *Handler) ticketSearch(ctx context.Context, _ uuid.UUID, in ticketSearchInput) (*mcp.CallToolResult, ticketSearchOutput, error) {
	if in.IncludeDone && len(in.Statuses) > 0 {
		return nil, ticketSearchOutput{}, fmt.Errorf("include_done and statuses together: put done in statuses, or drop statuses")
	}
	statuses, err := requestedStatuses(in.Statuses)
	if err != nil {
		return nil, ticketSearchOutput{}, err
	}
	// An explicit list is the whole set, so it can name done on its own.
	if len(statuses) == 0 {
		statuses = searchStatuses(in.IncludeDone)
	}
	// One past the limit, to tell a full page from a truncated one.
	found, err := h.tickets.List(ctx, &ticket.ListFilter{
		Statuses: statuses, Text: in.Text, TextSkipsAttempts: true, Limit: searchLimit + 1,
	})
	if err != nil {
		return nil, ticketSearchOutput{}, err
	}
	out := ticketSearchOutput{Tickets: make([]searchRow, 0, min(len(found), searchLimit))}
	if len(found) > searchLimit {
		found, out.More = found[:searchLimit], true
	}
	if len(found) == 0 {
		return text("No tickets match."), out, nil
	}

	var b strings.Builder
	for _, t := range found {
		out.Tickets = append(out.Tickets, searchRow{
			ID: t.ID, Title: t.Title, Status: string(t.Status), Priority: string(t.Priority),
		})
		fmt.Fprintf(&b, "#%d %s [%s, %s]", t.ID, t.Title, t.Status, t.Priority)
		if t.Status == ticket.StatusParked {
			b.WriteString(" -- parked: kept, not planned; not work to take")
		}
		b.WriteString("\n")
	}
	if out.More {
		fmt.Fprintf(&b, "Showing the newest %d; more match. Narrow it with statuses or more words.\n", searchLimit)
	}
	b.WriteString("Read one with ticket_get.")
	return text("%s", b.String()), out, nil
}
