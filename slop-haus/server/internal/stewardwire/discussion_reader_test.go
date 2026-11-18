package stewardwire_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/steward/discussionanswer"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/steward/discussionlabel"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/stewardwire"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

type fakeDiscussionTickets struct {
	attempts []ticket.Attempt
	comments []ticket.Comment
	contexts []ticket.AgentContext
}

func (f *fakeDiscussionTickets) Attempts(context.Context, int64) ([]ticket.Attempt, error) {
	return f.attempts, nil
}

func (f *fakeDiscussionTickets) Get(context.Context, int64) (*ticket.Ticket, error) {
	return &ticket.Ticket{Revision: 4}, nil
}

func (f *fakeDiscussionTickets) ListComments(context.Context, int64) ([]ticket.Comment, error) {
	return f.comments, nil
}

func (f *fakeDiscussionTickets) ListAgentContexts(context.Context, int64) ([]ticket.AgentContext, error) {
	return f.contexts, nil
}

type fakeLatest map[int]int

func (f fakeLatest) Latest(_ context.Context, _ int64, attempt int) (*discussionlabel.Record, error) {
	if round, ok := f[attempt]; ok {
		return &discussionlabel.Record{Round: round}, nil
	}
	return nil, discussionlabel.ErrNotFound
}

type fakeHead struct {
	sha string
	err error
}

func (f fakeHead) CurrentHead(context.Context, int64) (string, error) { return f.sha, f.err }

func agentComment(body string, attempt *int64) ticket.Comment {
	id := uuid.New()
	return ticket.Comment{AuthorAgentID: &id, Body: body, AttemptNumber: attempt}
}

func TestDiscussionReaderReadsTheAddressedAttempt(t *testing.T) {
	one, two := int64(1), int64(2)
	tickets := &fakeDiscussionTickets{
		attempts: []ticket.Attempt{{Number: 1, Status: "discussion"}, {Number: 2, Status: "discussion"}, {Number: 3, Status: "working"}},
		comments: []ticket.Comment{agentComment("first attempt asks", &one), agentComment("second attempt asks", &two)},
		contexts: []ticket.AgentContext{{AttemptNumber: 1, Body: "handoff one"}, {AttemptNumber: 2, Body: "handoff two"}},
	}
	r := stewardwire.NewDiscussionReader(tickets, fakeLatest{1: 5, 2: 7}, fakeHead{sha: "abc"})

	open, err := r.Open(context.Background(), 9, 1)

	require.NoError(t, err)
	assert.Equal(t, &discussionanswer.Open{Attempt: 1, Round: 5, Revision: 4, HeadSHA: "abc", Question: "first attempt asks", Handoff: "handoff one"}, open)
}

func TestDiscussionReaderHasNoOpenDiscussion(t *testing.T) {
	tickets := &fakeDiscussionTickets{attempts: []ticket.Attempt{{Number: 1, Status: "discussion"}, {Number: 2, Status: "working"}}}
	r := stewardwire.NewDiscussionReader(tickets, fakeLatest{2: 1}, fakeHead{})

	_, err := r.Open(context.Background(), 9, 2)
	assert.ErrorIs(t, err, discussionanswer.ErrNoOpen, "attempt not in discussion")
	_, err = r.Open(context.Background(), 9, 1)
	assert.ErrorIs(t, err, discussionanswer.ErrNoOpen, "no labelled round")
	_, err = r.Open(context.Background(), 9, 4)
	assert.ErrorIs(t, err, discussionanswer.ErrNoOpen, "no such attempt")
}

func TestDiscussionReaderLeavesTheHeadEmptyWhenUnreadable(t *testing.T) {
	tickets := &fakeDiscussionTickets{attempts: []ticket.Attempt{{Number: 1, Status: "discussion"}}}
	r := stewardwire.NewDiscussionReader(tickets, fakeLatest{1: 1}, fakeHead{sha: "stale", err: errors.New("host down")})

	open, err := r.Open(context.Background(), 9, 1)

	require.NoError(t, err)
	assert.Empty(t, open.HeadSHA)
}
