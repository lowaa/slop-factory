package stewardwire

import (
	"context"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/steward/discussionlabel"
)

// RoundSubmitter completes a queued round; *discussionlabel.Service is one.
type RoundSubmitter interface {
	Submit(ctx context.Context, q *discussionlabel.Question, c *discussionlabel.Classification) (*discussionlabel.Record, error)
}

// DiscussionSubmitter completes a pending label with the steward agent's claim,
// against the revision and head the round has now: a label queued against
// older ones is refused as stale rather than completed on facts that moved.
type DiscussionSubmitter struct {
	rounds DiscussionRounds
	labels RoundSubmitter
}

func NewDiscussionSubmitter(rounds DiscussionRounds, labels RoundSubmitter) *DiscussionSubmitter {
	return &DiscussionSubmitter{rounds: rounds, labels: labels}
}

// Submit reads the round as it stands and completes its label.
func (d *DiscussionSubmitter) Submit(ctx context.Context, ticketID int64, attempt, round int, c *discussionlabel.Classification) (*discussionlabel.Record, error) {
	open, err := d.rounds.Round(ctx, ticketID, attempt, round)
	if err != nil {
		return nil, err
	}
	return d.labels.Submit(ctx, &discussionlabel.Question{
		Target: discussionlabel.Target{
			TicketID: ticketID, Attempt: attempt, Round: round, Revision: open.Revision, HeadSHA: open.HeadSHA,
		},
		Question: open.Question, Handoff: open.Handoff,
	}, c)
}
