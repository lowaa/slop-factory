package mcpserver_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

func (r *realRig) ticketIn(title string, status ticket.Status) *ticket.Ticket {
	r.t.Helper()
	start := ticket.StatusOpen
	if status == ticket.StatusPreparing {
		start = status
	}
	created, err := r.tickets.Create(r.ctx, &ticket.CreateParams{Title: title, Status: start})
	require.NoError(r.t, err)
	if status == start {
		return created
	}
	moved, err := r.tickets.Update(r.ctx, created.ID, &ticket.UpdateParams{Status: &status})
	require.NoError(r.t, err)
	return moved
}

func searchRowIDs(t *testing.T, res *mcp.CallToolResult) []int64 {
	t.Helper()
	rows, ok := structured(t, res)["tickets"].([]any)
	require.True(t, ok, "tickets: %#v", structured(t, res)["tickets"])
	ids := make([]int64, 0, len(rows))
	for _, row := range rows {
		id, ok := row.(map[string]any)["id"].(float64)
		require.True(t, ok)
		ids = append(ids, int64(id))
	}
	return ids
}

// What the board shows for the same words, plus parked tickets, marked as
// not work to take.
func TestTicketSearch_SearchesWhatTheBoardShowsAndParkedTickets(t *testing.T) {
	r := newRealRig(t)
	preparing := r.ticketIn("zebra preparing", ticket.StatusPreparing)
	open := r.ticketIn("zebra open", ticket.StatusOpen)
	done := r.ticketIn("zebra done", ticket.StatusDone)
	r.ticketIn("zebra cancelled", ticket.StatusCancelled)
	parked := r.parkedTicket("zebra someday")
	r.ticketIn("unrelated", ticket.StatusOpen)

	res := r.call("ticket_search", map[string]any{"text": "ZEBRA"})
	require.False(t, res.IsError, resultText(t, res))
	assert.Equal(t, []int64{parked.ID, open.ID, preparing.ID}, searchRowIDs(t, res), "newest first")
	assert.Contains(t, resultText(t, res),
		fmt.Sprintf("#%d zebra someday [parked, normal] -- parked: kept, not planned; not work to take", parked.ID))
	assert.Contains(t, resultText(t, res), fmt.Sprintf("#%d zebra open [open, normal]\n", open.ID),
		"only parked tickets carry the mark")
	row := structured(t, res)["tickets"].([]any)[0].(map[string]any)
	assert.Equal(t, map[string]any{"id": float64(parked.ID), "title": "zebra someday", "status": "parked", "priority": "normal"}, row,
		"a compact row, no body")

	withDone := r.call("ticket_search", map[string]any{"text": "zebra", "include_done": true})
	require.False(t, withDone.IsError, resultText(t, withDone))
	assert.Equal(t, []int64{parked.ID, done.ID, open.ID, preparing.ID}, searchRowIDs(t, withDone),
		"done when asked; cancelled never, as on the board")

	none := r.call("ticket_search", map[string]any{"text": "zebra giraffe"})
	require.False(t, none.IsError, resultText(t, none))
	assert.Equal(t, "No tickets match.", resultText(t, none))
	assert.Empty(t, searchRowIDs(t, none))
}

// The service's own refusal, as the board gets it: the tool adds no rules
// of its own.
func TestTicketSearch_RefusesWhatTheBoardRefuses(t *testing.T) {
	r := newRealRig(t)
	res := r.call("ticket_search", map[string]any{"text": "zebra\x00"})
	assert.True(t, res.IsError)
	assert.Contains(t, resultText(t, res), "not valid text")
}

// Ticket numbers are per tenant; each tenant's agent finds only its own,
// through the real store's row-level security.
func TestTicketSearch_StaysInTheCallersTenant(t *testing.T) {
	database := dbtest.WithDB(t)
	a, b := newRealRigIn(t, database), newRealRigIn(t, database)
	aTicket := a.ticketIn("plain title", ticket.StatusOpen)
	bTicket := b.ticketIn("zebra title", ticket.StatusOpen)
	require.Equal(t, aTicket.ID, bTicket.ID, "both tenants' first ticket is #1")

	fromA := a.call("ticket_search", map[string]any{"text": "zebra"})
	require.False(t, fromA.IsError, resultText(t, fromA))
	assert.Empty(t, searchRowIDs(t, fromA))

	fromB := b.call("ticket_search", map[string]any{"text": "zebra"})
	require.False(t, fromB.IsError, resultText(t, fromB))
	assert.Equal(t, []int64{bTicket.ID}, searchRowIDs(t, fromB))
}

// Asks for one past the cap, to say when there is more than it shows.
func TestTicketSearch_CapsTheRowsAndSaysWhenThereAreMore(t *testing.T) {
	r := newRig(t)
	const limit = 50
	for i := limit + 1; i > 0; i-- {
		r.tickets.listed = append(r.tickets.listed,
			ticket.Ticket{ID: int64(i), Title: "zebra", Status: ticket.StatusOpen, Priority: model.PriorityNormal})
	}

	res, err := r.session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "ticket_search", Arguments: map[string]any{"text": "zebra"},
	})
	require.NoError(t, err)
	require.False(t, res.IsError, resultText(t, res))
	require.NotNil(t, r.tickets.listedWith)
	assert.Equal(t, limit+1, r.tickets.listedWith.Limit)
	ids := searchRowIDs(t, res)
	assert.Len(t, ids, limit)
	assert.Equal(t, int64(limit+1), ids[0])
	assert.Equal(t, true, structured(t, res)["more"])
	assert.Contains(t, resultText(t, res), "Showing the newest 50; more match.")
	assert.NotContains(t, resultText(t, res), "#1 zebra", "the one past the cap is not shown")

	r.tickets.listed = r.tickets.listed[1:]
	full, err := r.session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "ticket_search", Arguments: map[string]any{"text": "zebra"},
	})
	require.NoError(t, err)
	assert.Len(t, searchRowIDs(t, full), limit)
	assert.NotContains(t, structured(t, full), "more", "exactly the cap is not truncated")
	assert.NotContains(t, resultText(t, full), "more match")
}

// A search cannot be used to find out what a sibling attempt was told: words
// only an attempt's comment holds match nothing for an agent on a ticket with
// several attempts, and a person's search on the board still finds them.
func TestTicketSearch_DoesNotMatchAnAttemptsOwnWordsOnATicketWithSiblings(t *testing.T) {
	r := newRealRig(t)
	single := r.openTicket("Single attempt")
	two := r.openTicket("Two attempts")
	r.withSiblingAttempt(two.ID)
	say := func(id int64, attempt int, body string) {
		t.Helper()
		_, err := r.tickets.PostComment(r.ctx, &ticket.HumanComment{
			TicketID: id, UserID: r.human, Body: body, Attempt: &ticket.AttemptRef{TicketID: id, Number: attempt},
		})
		require.NoError(t, err)
	}
	say(single.ID, 1, "quokka on the only attempt")
	say(two.ID, 2, "quokka on a sibling")

	res := r.call("ticket_search", map[string]any{"text": "quokka"})
	require.False(t, res.IsError, resultText(t, res))
	assert.Equal(t, []int64{single.ID}, searchRowIDs(t, res), "no sibling to keep it from on the single-attempt ticket")

	onBoard, err := r.tickets.List(r.ctx, &ticket.ListFilter{Text: "quokka"})
	require.NoError(t, err)
	assert.Len(t, onBoard, 2, "the board still searches everything")
}

func TestTicketSearch_ListsByStatusWithoutText(t *testing.T) {
	r := newRealRig(t)
	prepA := r.ticketIn("alpha", ticket.StatusPreparing)
	prepB := r.ticketIn("beta", ticket.StatusPreparing)
	r.ticketIn("gamma", ticket.StatusOpen)
	done := r.ticketIn("delta", ticket.StatusDone)

	res := r.call("ticket_search", map[string]any{"statuses": []string{"preparing"}})
	require.False(t, res.IsError, resultText(t, res))
	assert.Equal(t, []int64{prepB.ID, prepA.ID}, searchRowIDs(t, res))

	both := r.call("ticket_search", map[string]any{"statuses": []string{"preparing", "done"}})
	require.False(t, both.IsError, resultText(t, both))
	assert.Equal(t, []int64{done.ID, prepB.ID, prepA.ID}, searchRowIDs(t, both), "naming done includes it")

	combined := r.call("ticket_search", map[string]any{"text": "alpha", "statuses": []string{"preparing", "open"}})
	require.False(t, combined.IsError, resultText(t, combined))
	assert.Equal(t, []int64{prepA.ID}, searchRowIDs(t, combined), "text and statuses both apply")

	wrongStatus := r.call("ticket_search", map[string]any{"text": "alpha", "statuses": []string{"open"}})
	require.False(t, wrongStatus.IsError, resultText(t, wrongStatus))
	assert.Empty(t, searchRowIDs(t, wrongStatus), "a text match outside the statuses is excluded")

	noText := r.call("ticket_search", map[string]any{})
	require.False(t, noText.IsError, resultText(t, noText))
	assert.Len(t, searchRowIDs(t, noText), 3, "no text, no statuses: the default set, done hidden")
}

func TestTicketSearch_RefusesAnUnknownStatus(t *testing.T) {
	r := newRealRig(t)
	res := r.call("ticket_search", map[string]any{"statuses": []string{"preparing", "nonsense"}})
	assert.True(t, res.IsError)
	assert.Contains(t, resultText(t, res), `unknown status "nonsense"`)
	assert.Contains(t, resultText(t, res), "preparing")
}

func TestTicketSearch_StatusesAreTheWholeSet(t *testing.T) {
	r := newRealRig(t)
	done := r.ticketIn("delta", ticket.StatusDone)
	r.ticketIn("gamma", ticket.StatusOpen)
	parked := r.parkedTicket("idea")

	res := r.call("ticket_search", map[string]any{"statuses": []string{"done"}})
	require.False(t, res.IsError, resultText(t, res))
	assert.Equal(t, []int64{done.ID}, searchRowIDs(t, res))

	pk := r.call("ticket_search", map[string]any{"statuses": []string{"parked"}})
	require.False(t, pk.IsError, resultText(t, pk))
	assert.Equal(t, []int64{parked.ID}, searchRowIDs(t, pk))
	assert.Contains(t, resultText(t, pk), "not work to take")

	both := r.call("ticket_search", map[string]any{"statuses": []string{"done"}, "include_done": true})
	assert.True(t, both.IsError)
	assert.Contains(t, resultText(t, both), "include_done and statuses")

	cancelled := r.call("ticket_search", map[string]any{"statuses": []string{"cancelled"}})
	assert.True(t, cancelled.IsError)
	assert.Contains(t, resultText(t, cancelled), "cancelled")
}
