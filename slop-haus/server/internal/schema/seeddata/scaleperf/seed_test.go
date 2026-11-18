package scaleperf_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/schema/seeddata"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// The timing guard is only worth what it reads, so this is the check that
// every list has rows to draw: a list that came back empty would meet any
// budget.
func TestSeedScaleFillsEveryList(t *testing.T) {
	w := seeded(t, seeddata.Scale{Tickets: 500})

	// The mix is fixed per hundred tickets, so at 500 every figure is exact.
	assert.Equal(t, 500, w.sum.Tickets)
	assert.Equal(t, map[string]int{
		"done": 200, "cancelled": 40, "open": 40, "in_progress": 25, "in_merge_queue": 20, "blocked": 40,
		"discussion": 20, "review": 30, "preparing": 50, "parked": 35,
	}, w.sum.ByStatus)
	assert.Equal(t, 1000, w.sum.Comments, "ticket n has n mod 5 comments")
	assert.Equal(t, 50, w.sum.Handoffs, "one ticket in ten")
	assert.Equal(t, 40+5+4*5, w.sum.Blockers, "each blocked ticket on an open one, every seventh also on the blocked one before it, and a satisfied link where a done ticket follows a done one at every tenth id")
	assert.Equal(t, 20+50+30, w.sum.Proposals, "discussion, preparing and review tickets")
	assert.NotZero(t, w.sum.UsageRows)
	assert.NotZero(t, w.sum.AwaitingReview)

	for _, l := range lists {
		t.Run(l.name, func(t *testing.T) {
			rows, err := l.run(w)
			require.NoError(t, err)
			assert.NotZero(t, rows)
			if l.maxRows > 0 {
				assert.LessOrEqual(t, rows, l.maxRows)
			}
		})
	}
}

// A list that ignored its filter would still draw a page of tickets, so
// what the pages hold is checked too.
func TestSeedScaleListsHoldWhatTheyAreFiltersFor(t *testing.T) {
	w := seeded(t, seeddata.Scale{Tickets: 500})
	page := func(f *ticket.ListFilter) []ticket.Ticket {
		f.Limit = pageSize
		p, err := w.tickets.ListPage(w.ctx, f)
		require.NoError(t, err)
		return p.Tickets
	}

	blocked := page(&ticket.ListFilter{Statuses: []ticket.Status{ticket.StatusBlocked}})
	assert.Len(t, blocked, 40)
	for _, tk := range blocked {
		assert.Equal(t, ticket.StatusBlocked, tk.Status)
	}

	unreviewed := page(&ticket.ListFilter{AwaitingPostReview: true})
	assert.Len(t, unreviewed, pageSize, "more are waiting than a page holds")
	for _, tk := range unreviewed {
		assert.Equal(t, ticket.StatusDone, tk.Status)
		assert.Nil(t, tk.PostReviewedAt)
	}

	// The rare word is in the title of tickets 7, 107, ... and in a comment
	// on 13, 113, ...
	rare := page(&ticket.ListFilter{Text: w.sum.SearchWord})
	assert.Len(t, rare, 10)
	for _, tk := range rare {
		assert.Contains(t, []int64{7, 13}, tk.ID%100)
	}
}

func TestSeedScaleRefusesATenantTooSmallForTheMix(t *testing.T) {
	_, err := seeddata.SeedScale(t.Context(), nil, [16]byte{}, seeddata.Scale{Tickets: 99})
	assert.ErrorContains(t, err, "below the 100")
}
