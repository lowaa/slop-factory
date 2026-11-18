package stewardwire_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/steward/discussionpick"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/stewardwire"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

func TestPickDiscussionsReadsTheNewestAttemptInDiscussion(t *testing.T) {
	one, two := int64(1), int64(2)
	tickets := &fakeDiscussionTickets{
		attempts: []ticket.Attempt{{Number: 1, Status: "discussion"}, {Number: 2, Status: "discussion"}, {Number: 3, Status: "working"}},
		comments: []ticket.Comment{agentComment("first asks", &one), agentComment("second asks", &two)},
		contexts: []ticket.AgentContext{{AttemptNumber: 1, Body: "handoff one"}, {AttemptNumber: 2, Body: "handoff two"}},
	}

	got, err := stewardwire.NewPickDiscussions(tickets).Discussion(context.Background(), 9)

	require.NoError(t, err)
	assert.Equal(t, &discussionpick.Discussion{Question: "second asks", Handoff: "handoff two"}, got)
}

func TestPickDiscussionsHasNoneWhenNoAttemptIsInDiscussion(t *testing.T) {
	tickets := &fakeDiscussionTickets{attempts: []ticket.Attempt{{Number: 1, Status: "working"}}}

	got, err := stewardwire.NewPickDiscussions(tickets).Discussion(context.Background(), 9)

	require.NoError(t, err)
	assert.Nil(t, got)
}
