package ticket_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

func overlapIDs(overlaps []ticket.ScopeOverlap) []int64 {
	ids := []int64{}
	for _, o := range overlaps {
		ids = append(ids, o.TicketID)
	}
	return ids
}

func (f *fixture) overlaps(t *testing.T, id int64) []ticket.ScopeOverlap {
	t.Helper()
	overlaps, err := f.svc.ScopeOverlaps(f.ctx, id)
	require.NoError(t, err)
	return overlaps
}

func TestScopeOverlaps_ADirectoryOverlapsTheFilesAndDirectoriesInIt(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	broad := f.ticket(t, "Broad", "app/src/")
	file := f.ticket(t, "File", "app/src/panel/a.go")
	nested := f.ticket(t, "Nested", "app/src/panel/")
	same := f.ticket(t, "Same", "app/src/")

	got := f.overlaps(t, broad.ID)
	assert.Equal(t, []int64{file.ID, nested.ID, same.ID}, overlapIDs(got))
	assert.Equal(t, ticket.ScopeOverlap{
		TicketID: file.ID, Title: "File", Status: ticket.StatusOpen, Path: "app/src/", Other: "app/src/panel/a.go",
	}, got[0])

	// The rule is symmetric: the narrow ticket sees the broad one.
	assert.Contains(t, overlapIDs(f.overlaps(t, file.ID)), broad.ID)
}

func TestScopeOverlaps_SiblingsAndPrefixLookalikesDoNotOverlap(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	mine := f.ticket(t, "Mine", "app/src/panel/", "app/core")
	f.ticket(t, "Sibling dir", "app/src/board/")
	f.ticket(t, "Lookalike dir", "app/src/panel_extra/")
	f.ticket(t, "Lookalike file", "app/coreutil")
	f.ticket(t, "No scope")

	assert.Empty(t, f.overlaps(t, mine.ID))
}

func TestScopeOverlaps_FinishedCancelledAndParkedTicketsAreIgnored(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	mine := f.ticket(t, "Mine", "app/src/")
	for _, status := range []ticket.Status{ticket.StatusDone, ticket.StatusCancelled, ticket.StatusParked} {
		other := f.ticket(t, string(status), "app/src/x.go")
		f.forceStatus(t, other.ID, status)
	}
	live := f.ticket(t, "Live", "app/src/y.go")

	assert.Equal(t, []int64{live.ID}, overlapIDs(f.overlaps(t, mine.ID)))
}

func TestScopeOverlaps_EveryLiveStatusCounts(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	mine := f.ticket(t, "Mine", "app/")
	want := []int64{}
	for _, status := range []ticket.Status{
		ticket.StatusPreparing, ticket.StatusOpen, ticket.StatusInProgress, ticket.StatusInMergeQueue,
		ticket.StatusBlocked, ticket.StatusDiscussion, ticket.StatusReview,
	} {
		other := f.ticket(t, string(status), "app/"+string(status)+".go")
		f.forceStatus(t, other.ID, status)
		want = append(want, other.ID)
	}
	assert.Equal(t, want, overlapIDs(f.overlaps(t, mine.ID)))
}

func TestScopeOverlaps_AnotherTenantsTicketsAreNeverListed(t *testing.T) {
	t.Parallel()
	a, b := newTwoTenants(t)
	mine := a.ticket(t, "A's", "app/src/")
	b.ticket(t, "B's", "app/src/x.go")
	own := a.ticket(t, "A's other", "app/src/y.go")

	got, err := a.svc.ScopeOverlaps(a.ctx, mine.ID)
	require.NoError(t, err)
	assert.Equal(t, []int64{own.ID}, overlapIDs(got))
}

func TestScopeOverlaps_AnUnknownTicketIsNotFound(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	_, err := f.svc.ScopeOverlaps(f.ctx, 999)
	assert.ErrorIs(t, err, ticket.ErrNotFound)
}
