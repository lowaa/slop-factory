package ticket_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

func TestGuidance_TellsAnAgentReadingAQueuedTicketItDoesNotHoldIt(t *testing.T) {
	t.Parallel()
	queued := ticket.GuidanceFor(&ticket.Ticket{ID: 7, Status: ticket.StatusInMergeQueue}, nil)
	assert.Contains(t, queued.MergeQueue, "merge queue")
	assert.Empty(t, ticket.GuidanceFor(&ticket.Ticket{ID: 7, Status: ticket.StatusInProgress}, nil).MergeQueue)
}

func TestGuidance_NamesTheTicketsBaseBranch(t *testing.T) {
	t.Parallel()
	now := time.Now()
	commit := "abc123"
	tk := &ticket.Ticket{ID: 7, BaseCommit: &commit, ApprovedAt: &now, Status: ticket.StatusInProgress}

	for _, tc := range []struct{ name, base, want, notWant string }{
		{"explicit base", "slop-haus-acme", "slop-haus-acme", "main"},
		{"default base", "", "main", "slop-haus-acme"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := &ticket.AttemptBranch{Name: "ticket-7-a1", Managed: true, Ready: true, Unpromoted: true, Base: tc.base}
			review := ticket.GuidanceFor(tk, b).Review
			assert.Contains(t, review, "merge "+tc.want+" into the branch")
			assert.Contains(t, review, "even if "+tc.want+" has moved")
			assert.Contains(t, ticket.BranchGuidance(tk, b), "never origin/"+tc.want)
			if tc.base != "" {
				assert.NotContains(t, review, " main ")
				assert.NotContains(t, ticket.BranchGuidance(tk, b), "origin/main")
			}

			unpinned := ticket.BranchGuidance(tk, &ticket.AttemptBranch{Name: "ticket-7-a1", Base: tc.base})
			assert.Contains(t, unpinned, "Branch from origin/"+tc.want+".")
		})
	}
}
