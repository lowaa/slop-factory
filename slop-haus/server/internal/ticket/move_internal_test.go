package ticket

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A move's whole decision on a ticket it may move: every side effect on the
// ticket's own row, and what goes with it -- whether a single edit or one
// of a batch.
func TestMoveChange(t *testing.T) {
	t.Parallel()
	move := func(w TicketWrite, revoke bool) *Change {
		return &Change{Ticket: w, Unblock: true, Revoke: revoke}
	}
	cases := []struct {
		move Move
		want *Change
	}{
		{MoveTo(StatusPreparing), move(TicketWrite{
			Status: To(StatusPreparing), Closing: Reopens(), FirstClaim: FirstClaimCleared(), ClearsPostReview: true,
		}, true)},
		{MoveTo(StatusParked), move(TicketWrite{
			Status: To(StatusParked), Closing: Reopens(), FirstClaim: FirstClaimCleared(), ClearsPostReview: true,
		}, true)},
		// Open is the queue: blocked while a dependency is unfinished.
		{MoveTo(StatusOpen), move(TicketWrite{Status: ToQueue(), Closing: Reopens(), ClearsPostReview: true}, true)},
		{MoveTo(StatusBlocked), move(TicketWrite{Status: To(StatusBlocked), Closing: Reopens(), ClearsPostReview: true}, true)},
		// In progress is the one status a move leaves a lease standing on.
		{MoveTo(StatusInProgress), move(TicketWrite{
			Status: To(StatusInProgress), Closing: Reopens(), ClearsPostReview: true,
		}, false)},
		{MoveTo(StatusDiscussion), move(TicketWrite{
			Status: To(StatusDiscussion), Closing: Reopens(), ClearsPostReview: true,
		}, true)},
		{MoveTo(StatusReview), move(TicketWrite{
			Status: To(StatusReview), Closing: Reopens(), Review: ReviewStarts(), ClearsPostReview: true,
		}, true)},
		// Done keeps its post-merge review: that is a look at this change.
		{MoveTo(StatusDone), move(TicketWrite{Status: To(StatusDone), Closing: Closes()}, true)},
		{MoveTo(StatusCancelled), move(TicketWrite{
			Status: To(StatusCancelled), Closing: Closes(), ClearsPostReview: true,
		}, true)},
		// Ready writes the status alone, by the queue's own rule.
		{MoveReady(), move(TicketWrite{Status: ToQueue()}, true)},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, tc.move.change(), "%+v", tc.move)
	}
}

// A batch is decided in id order, whatever order it was named in; any
// refusal refuses all of it, naming every refused ticket in that order.
func TestMoveDecide(t *testing.T) {
	t.Parallel()
	park := MoveTo(StatusParked)
	loaded := []Ticket{{ID: 3, Status: StatusOpen}, {ID: 1, Status: StatusPreparing}, {ID: 2, Status: StatusInProgress}}

	got, err := park.decide([]int64{3, 1}, loaded)
	require.NoError(t, err)
	assert.Equal(t, []TicketChange{{TicketID: 1, Change: park.change()}, {TicketID: 3, Change: park.change()}}, got)

	got, err = park.decide([]int64{9, 3, 2, 1}, loaded)
	assert.Nil(t, got)
	var refused *BatchRefusedError
	require.ErrorAs(t, err, &refused)
	assert.Equal(t, []Refusal{
		{TicketID: 2, From: StatusInProgress, Err: ErrNotParkable, move: &park},
		{TicketID: 9, Err: ErrNotFound, move: &park},
	}, refused.Refusals)
}

func concat(parts ...[]string) []string {
	var out []string
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// A batch of one reads as the single routes always have; a batch's reason
// names the ticket and the status it was refused in.
func TestRefusalWording(t *testing.T) {
	t.Parallel()
	park := MoveTo(StatusParked)
	held := &Refusal{TicketID: 12, From: StatusInProgress, Err: ErrNotParkable, move: &park}
	assert.Equal(t, ErrNotParkable.Error()+": #12 to parked", held.Error())
	assert.Equal(t, "#12 in progress: only a preparing or open ticket can be parked -- "+
		"release a held ticket, or send it back to preparing, first", held.Reason())

	ready := MoveReady()
	notPreparing := &Refusal{TicketID: 3, From: StatusOpen, Err: ErrNotPreparing, move: &ready}
	assert.Equal(t, ErrNotPreparing.Error()+": #3", notPreparing.Error())

	missing := &Refusal{TicketID: 9, Err: ErrNotFound, move: &park}
	assert.Equal(t, ErrNotFound.Error()+": #9", missing.Error())
	assert.Equal(t, "#9: not found", missing.Reason())
}

// Every status, in board order, listed here rather than read from statuses
// so a status dropped from that list fails this test instead of vanishing
// from it.
var everyStatus = []Status{
	StatusPreparing, StatusParked, StatusOpen, StatusInProgress, StatusBlocked, StatusDiscussion, StatusReview,
	StatusDone, StatusCancelled,
}

// What the board is told it may do is what the server lets it do: every
// (from, to) pair is judged by humanMove itself, not by a list of what the
// rules currently are.
func TestAllowedMovesFollowHumanMove(t *testing.T) {
	t.Parallel()
	for _, from := range everyStatus {
		var want []Status
		for _, to := range everyStatus {
			if humanMove(from, to) == nil {
				want = append(want, to)
			}
		}
		assert.Equal(t, want, AllowedMoves(from), "from %s", from)
		assert.Contains(t, AllowedMoves(from), from, "the current status is offered, so the select can show it")
	}
}

func TestCanReadyOnlyFromPreparing(t *testing.T) {
	t.Parallel()
	for _, from := range everyStatus {
		assert.Equal(t, from == StatusPreparing, CanReady(from), string(from))
		assert.Equal(t, MoveReady().check(from) == nil, CanReady(from), string(from))
	}
}

func TestMergeableIncludesATicketInTheMergeQueue(t *testing.T) {
	t.Parallel()
	assert.True(t, mergeable(&Ticket{Status: StatusInMergeQueue}), "a change merged on the host closes the ticket the queue is landing")
	assert.True(t, mergeable(&Ticket{Status: StatusReview}))
	assert.False(t, mergeable(&Ticket{Status: StatusOpen}))
}
