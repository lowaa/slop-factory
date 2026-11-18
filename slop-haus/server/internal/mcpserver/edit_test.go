package mcpserver_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

func strPtr(s string) *string { return &s }

func TestTicketEdit_EditsTheHeldTicketAsTheAgent(t *testing.T) {
	r := newRig(t)
	r.tickets.lease = &ticket.Lease{TicketID: 7}

	res, err := r.session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "ticket_edit", Arguments: map[string]any{"revision": 2, "title": "A better title", "body": "What it really is."},
	})
	require.NoError(t, err)
	require.False(t, res.IsError, resultText(t, res))
	require.NotNil(t, r.tickets.edited)
	assert.Equal(t, strPtr("A better title"), r.tickets.edited.Title)
	assert.Equal(t, strPtr("What it really is."), r.tickets.edited.Body)
	require.NotNil(t, r.tickets.edited.BaseRevision, "the revision the agent read is what the edit is checked against")
	assert.Equal(t, 2, *r.tickets.edited.BaseRevision)
	assert.NotEqual(t, uuid.Nil, r.tickets.editedBy, "it acts as the authenticated agent")
	assert.Positive(t, r.sessions.progress)
	assert.Equal(t, "Edited #7 (revision 3).", resultText(t, res))

	_, err = r.session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "ticket_edit", Arguments: map[string]any{"revision": 3, "title": "Only the title"},
	})
	require.NoError(t, err)
	assert.Nil(t, r.tickets.edited.Body, "an omitted body is left alone, not cleared")
}

// The held-only rule is the store's (see ticket's revisions_test.go and
// TestTicketEdit_ThroughTheRealStore); what the tool owes the agent is to
// report the refusal as a failure and not count it as progress.
func TestTicketEdit_ARefusalIsAnErrorAndNotProgress(t *testing.T) {
	for name, refusal := range map[string]error{
		"no lease":       ticket.ErrNoLease,
		"stale revision": ticket.ErrStaleRevision,
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t)
			r.tickets.editErr = refusal

			res, err := r.session.CallTool(context.Background(), &mcp.CallToolParams{
				Name: "ticket_edit", Arguments: map[string]any{"revision": 0, "title": "Renamed"},
			})
			require.NoError(t, err)
			assert.True(t, res.IsError)
			assert.Zero(t, r.sessions.progress)
		})
	}
}

// Restating the current text changes nothing, so it must not reset the
// stop-hook loop breaker -- or an agent stuck re-sending the same edit
// would never be stopped.
func TestTicketEdit_ANoOpIsNotProgress(t *testing.T) {
	r := newRig(t)
	r.tickets.editNoChange = true

	res, err := r.session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "ticket_edit", Arguments: map[string]any{"revision": 3, "title": "Same as before"},
	})
	require.NoError(t, err)
	require.False(t, res.IsError, resultText(t, res))
	assert.Contains(t, resultText(t, res), "Nothing changed")
	assert.Zero(t, r.sessions.progress)
}

// The tool takes the new text and nothing else. With no ticket id to pass,
// an agent has no way to name a ticket it does not hold.
func TestTicketEdit_CannotNameATicket(t *testing.T) {
	r := newRig(t)

	tools, err := r.session.ListTools(context.Background(), nil)
	require.NoError(t, err)
	for _, tool := range tools.Tools {
		if tool.Name != "ticket_edit" {
			continue
		}
		schema, ok := tool.InputSchema.(map[string]any)
		require.True(t, ok)
		props, ok := schema["properties"].(map[string]any)
		require.True(t, ok)
		assert.ElementsMatch(t, []string{"revision", "title", "body"}, keys(props))
		assert.Equal(t, []any{"revision"}, schema["required"])
		return
	}
	t.Fatal("ticket_edit is not registered")
}

var editedAt = time.Date(2026, 9, 26, 21, 5, 30, 0, time.UTC)

func TestTicketGet_ShowsTheLatestEditsCompactly(t *testing.T) {
	r := newRig(t)
	agentID := uuid.New()
	human := uuid.New()

	var revisions []ticket.Revision
	for i := 1; i <= 6; i++ {
		revisions = append(revisions, ticket.Revision{
			Number: i, Editor: ticket.UserAuthor(human), CreatedAt: editedAt,
			OldTitle: strPtr(fmt.Sprintf("title %d", i-1)), NewTitle: strPtr(fmt.Sprintf("title %d", i)),
		})
	}
	var oldLines, newLines []string
	for i := range 20 {
		oldLines = append(oldLines, fmt.Sprintf("old %d", i))
		newLines = append(newLines, fmt.Sprintf("new %d", i))
	}
	oldLines[0] = strings.Repeat("x", 500)
	revisions = append(revisions, ticket.Revision{
		Number: 7, Editor: ticket.AgentAuthor(agentID), CreatedAt: editedAt,
		OldBody: strPtr("keep\n" + strings.Join(oldLines, "\n")),
		NewBody: strPtr("keep\n" + strings.Join(newLines, "\n")),
	})
	r.tickets.revisions = revisions

	res, err := r.session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "ticket_get", Arguments: map[string]any{"ticket_id": 1},
	})
	require.NoError(t, err)
	body := resultText(t, res)

	assert.Contains(t, body, "(2 earlier not shown)")
	assert.NotContains(t, body, `"title 1"`, "only the latest five are listed")
	assert.Contains(t, body, `- r3 by human at 2026-09-26 21:05Z: title "title 2" -> "title 3"`)
	assert.Contains(t, body, "- r7 by builder-1 at 2026-09-26 21:05Z: description:")
	assert.NotContains(t, body, "keep", "unchanged lines are left out: the current text is right above")
	assert.Contains(t, body, "  - "+strings.Repeat("x", 160)+"…", "a long line is cut short")
	assert.NotContains(t, body, strings.Repeat("x", 161))
	assert.Contains(t, body, "(28 more changed lines)", "40 changed lines, 12 shown")

	var out struct {
		Edits []struct {
			Revision int    `json:"revision"`
			By       string `json:"by"`
			Title    string `json:"title"`
			Body     string `json:"body"`
		} `json:"edits"`
		EarlierEdits int `json:"earlier_edits"`
	}
	raw, err := json.Marshal(res.StructuredContent)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &out))
	assert.Equal(t, 2, out.EarlierEdits)
	require.Len(t, out.Edits, 5)
	assert.Equal(t, 3, out.Edits[0].Revision)
	assert.Equal(t, `"title 2" -> "title 3"`, out.Edits[0].Title)
	assert.Equal(t, "builder-1", out.Edits[4].By)
	assert.Empty(t, out.Edits[4].Title)
	assert.Equal(t, 13, strings.Count(out.Edits[4].Body, "\n")+1, "12 changed lines and the count of the rest")
}

func TestTicketGet_ATicketNeverEditedHasNoEditsSection(t *testing.T) {
	r := newRig(t)

	res, err := r.session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "ticket_get", Arguments: map[string]any{"ticket_id": 1},
	})
	require.NoError(t, err)
	assert.NotContains(t, resultText(t, res), "Edits")
}

// Through the real store: the agent's edit lands on the ticket it holds and
// nowhere else, and the next reader of the ticket sees who changed what.
func TestTicketEdit_ThroughTheRealStore(t *testing.T) {
	r := newRealRig(t)
	held := r.openTicket("Delete button")
	other := r.openTicket("Someone else's")
	claimed := r.call("ticket_next", map[string]any{"ticket_id": held.ID})
	require.False(t, claimed.IsError, resultText(t, claimed))

	read := r.call("ticket_get", map[string]any{"ticket_id": held.ID})
	assert.Contains(t, resultText(t, read), "revision 0]", "ticket_get shows the revision to edit against")

	res := r.call("ticket_edit", map[string]any{"revision": 0, "title": "Reinstall script", "body": "Per-agent reinstall script."})
	require.False(t, res.IsError, resultText(t, res))
	assert.Contains(t, resultText(t, res), fmt.Sprintf("Edited #%d (revision 1)", held.ID))

	res = r.call("ticket_edit", map[string]any{"revision": 1, "title": " "})
	assert.True(t, res.IsError, "held to the title rule creation uses")

	// A person edits it meanwhile; an agent edit made against what it read
	// before is refused rather than reverting theirs.
	_, err := r.tickets.Edit(r.ctx, held.ID, r.human, &ticket.EditParams{Body: strPtr("The person's version.")})
	require.NoError(t, err)
	res = r.call("ticket_edit", map[string]any{"revision": 1, "body": "The agent's stale version."})
	assert.True(t, res.IsError)
	assert.Contains(t, resultText(t, res), "ticket_get")
	got, err := r.tickets.Get(r.ctx, held.ID)
	require.NoError(t, err)
	assert.Equal(t, "The person's version.", got.Body)

	got, err = r.tickets.Get(r.ctx, other.ID)
	require.NoError(t, err)
	assert.Equal(t, "Someone else's", got.Title)
	assert.Zero(t, got.Revision)

	res = r.call("ticket_get", map[string]any{"ticket_id": held.ID})
	require.False(t, res.IsError, resultText(t, res))
	body := resultText(t, res)
	assert.Contains(t, body, "#"+fmt.Sprint(held.ID)+" Reinstall script")
	assert.Contains(t, body, `r1 by builder-1`)
	assert.Contains(t, body, `title "Delete button" -> "Reinstall script"`)
	assert.Contains(t, body, "+ Per-agent reinstall script.")
	assert.Contains(t, body, "r2 by human")
}
