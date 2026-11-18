package stewardwire

import (
	"context"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/steward/discussionanswer"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/steward/discussionlabel"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// DiscussionRounds reads the discussion of one round of an attempt.
type DiscussionRounds interface {
	Round(ctx context.Context, ticketID int64, attempt, round int) (*discussionanswer.Open, error)
}

// RoundQueuer queues a round's classification; *discussionlabel.Service is one.
type RoundQueuer interface {
	Queue(ctx context.Context, q *discussionlabel.Question) (*discussionlabel.Record, error)
}

// DiscussionQueue labels each discussion round as it opens. It implements
// ticket.DiscussionHook.
type DiscussionQueue struct {
	rounds DiscussionRounds
	labels RoundQueuer
}

func NewDiscussionQueue(rounds DiscussionRounds, labels RoundQueuer) *DiscussionQueue {
	return &DiscussionQueue{rounds: rounds, labels: labels}
}

var _ ticket.DiscussionHook = (*DiscussionQueue)(nil)

// Entered reads the round's question, revision and head as they stand and
// queues the label made against them. A repeat for the round makes nothing new.
func (q *DiscussionQueue) Entered(ctx context.Context, e *ticket.DiscussionEntry) error {
	return q.QueueRound(ctx, discussionlabel.Round{TicketID: e.TicketID, Attempt: e.Attempt, Round: e.Round})
}

var _ discussionlabel.RoundQueuer = (*DiscussionQueue)(nil)

// QueueRound is Entered for a round found by the sweep.
func (q *DiscussionQueue) QueueRound(ctx context.Context, r discussionlabel.Round) error {
	open, err := q.rounds.Round(ctx, r.TicketID, r.Attempt, r.Round)
	if err != nil {
		return err
	}
	_, err = q.labels.Queue(ctx, &discussionlabel.Question{
		Target: discussionlabel.Target{
			TicketID: r.TicketID, Attempt: r.Attempt, Round: r.Round, Revision: open.Revision, HeadSHA: open.HeadSHA,
		},
		Question: open.Question, Handoff: open.Handoff,
	})
	return err
}
