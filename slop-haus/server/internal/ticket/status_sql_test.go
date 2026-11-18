package ticket_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// The fragments are spliced into queries in other packages, so their exact
// text is the contract: a changed set changes what those queries select.
func TestStatusSQLFragments(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "'open'", ticket.SQLStatusOpen)
	assert.Equal(t, "'in_progress'", ticket.SQLStatusInProgress)
	assert.Equal(t, "'in_merge_queue'", ticket.SQLStatusInMergeQueue)
	assert.Equal(t, "'done'", ticket.SQLStatusDone)
	assert.Equal(t, "('done', 'cancelled')", ticket.TerminalStatusesSQL)
	assert.Equal(t, "('discussion', 'review')", ticket.WaitingOnPersonStatusesSQL)
	assert.Equal(t, "('done', 'closed')", ticket.AttemptEndedStatusesSQL)
	assert.Equal(t, "('review', 'done', 'closed')", ticket.AttemptFinishedStatusesSQL)
	assert.Equal(t, "('queued', 'in_progress', 'in_merge_queue', 'discussion')", ticket.AttemptRunningStatusesSQL)
	assert.Equal(t, "('queued', 'in_progress', 'in_merge_queue', 'discussion', 'review')", ticket.AttemptLiveStatusesSQL)
}
