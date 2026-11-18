package stewardwire_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/steward/discussionanswer"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/steward/discussionlabel"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/stewardwire"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

type fakeRounds struct{ open *discussionanswer.Open }

func (f fakeRounds) Round(context.Context, int64, int, int) (*discussionanswer.Open, error) {
	return f.open, nil
}

type recordingQueuer struct{ got []*discussionlabel.Question }

func (r *recordingQueuer) Queue(_ context.Context, q *discussionlabel.Question) (*discussionlabel.Record, error) {
	r.got = append(r.got, q)
	return &discussionlabel.Record{}, nil
}

func TestDiscussionQueue_QueuesTheRoundWithWhatTheDiscussionStandsOn(t *testing.T) {
	queuer := &recordingQueuer{}
	q := stewardwire.NewDiscussionQueue(
		fakeRounds{&discussionanswer.Open{Attempt: 2, Round: 3, Revision: 7, HeadSHA: "abc", Question: "merge?", Handoff: "green"}}, queuer)

	require.NoError(t, q.Entered(context.Background(), &ticket.DiscussionEntry{TicketID: 9, Attempt: 2, Round: 3}))

	require.Len(t, queuer.got, 1)
	assert.Equal(t, discussionlabel.Target{TicketID: 9, Attempt: 2, Round: 3, Revision: 7, HeadSHA: "abc"}, queuer.got[0].Target)
	assert.Equal(t, "merge?", queuer.got[0].Question)
	assert.Equal(t, "green", queuer.got[0].Handoff)
}
