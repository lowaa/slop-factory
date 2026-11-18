package mcpserver_test

import (
	"context"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

func nextTicket(t *testing.T, r *rig) (string, map[string]any) {
	t.Helper()
	res, err := r.session.CallTool(context.Background(), &mcp.CallToolParams{Name: "ticket_next"})
	require.NoError(t, err)
	out, _ := res.StructuredContent.(map[string]any)
	return resultText(t, res), out
}

func TestTicketNext_TellsTheWorkerWhichBranchToSwitchTo(t *testing.T) {
	const sha = "0123456789abcdef0123456789abcdef01234567"
	const name = "ticket-7-a1"

	t.Run("made", func(t *testing.T) {
		r := newRig(t)
		base := sha
		r.tickets.claim = &ticket.ClaimResult{
			Ticket: &ticket.Ticket{ID: 7, Title: "Pin", Status: ticket.StatusInProgress, BaseCommit: &base},
			Branch: &ticket.AttemptBranch{Name: name, Managed: true, Ready: true},
		}

		text, out := nextTicket(t, r)

		assert.Contains(t, text, "Your branch "+name+" is ready")
		assert.Contains(t, text, "git fetch origin && git switch "+name)
		assert.NotContains(t, text, "switch -c")
		assert.Equal(t, sha, out["ticket"].(map[string]any)["base_commit"])
	})

	t.Run("unpromoted attempt: catch up from the base branch, never main", func(t *testing.T) {
		r := newRig(t)
		base := sha
		r.tickets.claim = &ticket.ClaimResult{
			Ticket: &ticket.Ticket{ID: 7, Title: "Pin", Status: ticket.StatusInProgress, BaseCommit: &base},
			Branch: &ticket.AttemptBranch{Name: "ticket-7-a2", Managed: true, Ready: true, Unpromoted: true},
		}

		text, _ := nextTicket(t, r)

		assert.Contains(t, text, "merge the base branch slop-haus-base-ticket-7, never origin/main")
	})

	t.Run("promoted attempt: no base-branch guidance", func(t *testing.T) {
		r := newRig(t)
		base := sha
		r.tickets.claim = &ticket.ClaimResult{
			Ticket: &ticket.Ticket{ID: 7, Title: "Pin", Status: ticket.StatusInProgress, BaseCommit: &base},
			Branch: &ticket.AttemptBranch{Name: name, Managed: true, Ready: true},
		}

		text, _ := nextTicket(t, r)

		assert.NotContains(t, text, "slop-haus-base-ticket")
	})

	t.Run("not made: the agent is not told to make its own", func(t *testing.T) {
		r := newRig(t)
		base := sha
		r.tickets.claim = &ticket.ClaimResult{
			Ticket: &ticket.Ticket{ID: 7, Title: "Pin", Status: ticket.StatusInProgress, BaseCommit: &base},
			Branch: &ticket.AttemptBranch{Name: name, Managed: true},
		}

		text, _ := nextTicket(t, r)

		assert.Contains(t, text, "Branch: "+name+" is NOT READY")
		assert.Contains(t, text, "Don't make a branch of your own")
		assert.NotContains(t, text, "git switch")
	})

	t.Run("unpinned: the fallback, worded as one", func(t *testing.T) {
		r := newRig(t)
		r.tickets.claim = &ticket.ClaimResult{Ticket: &ticket.Ticket{ID: 7, Title: "Pin", Status: ticket.StatusInProgress}}

		text, out := nextTicket(t, r)

		assert.Contains(t, text, "Base commit: not pinned, so slop-haus did not make your branch. Branch from origin/main.")
		assert.Contains(t, text, "Branch: "+name+" -- name your branch this")
		assert.NotContains(t, out["ticket"], "base_commit")
	})
}
