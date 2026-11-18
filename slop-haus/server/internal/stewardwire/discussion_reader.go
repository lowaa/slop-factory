package stewardwire

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/change"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/steward"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/steward/discussionanswer"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/steward/discussionlabel"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// DiscussionTickets is the part of the ticket service the open discussion is read from.
type DiscussionTickets interface {
	Comments
	ListAgentContexts(ctx context.Context, ticketID int64) ([]ticket.AgentContext, error)
}

// LatestLabels finds an attempt's newest labelled round.
type LatestLabels interface {
	Latest(ctx context.Context, ticketID int64, attempt int) (*discussionlabel.Record, error)
}

// DiscussionReader reads a ticket's open discussion for the owner's answer. It
// implements discussionanswer.Discussions.
type DiscussionReader struct {
	tickets DiscussionTickets
	labels  LatestLabels
	heads   steward.HeadReader
}

func NewDiscussionReader(tickets DiscussionTickets, labels LatestLabels, heads steward.HeadReader) *DiscussionReader {
	return &DiscussionReader{tickets: tickets, labels: labels, heads: heads}
}

// Open is the attempt's newest labelled round, when the attempt is in
// discussion. Only that attempt's comments and handoff are read as its
// question: a sibling attempt's discussion is its own.
func (r *DiscussionReader) Open(ctx context.Context, ticketID int64, attempt int) (*discussionanswer.Open, error) {
	attempts, err := r.tickets.Attempts(ctx, ticketID)
	if err != nil {
		return nil, fmt.Errorf("list attempts of ticket %d: %w", ticketID, err)
	}
	if !slices.ContainsFunc(attempts, func(a ticket.Attempt) bool {
		return a.Number == attempt && a.Status == string(ticket.StatusDiscussion)
	}) {
		return nil, discussionanswer.ErrNoOpen
	}
	rec, err := r.labels.Latest(ctx, ticketID, attempt)
	if errors.Is(err, discussionlabel.ErrNotFound) {
		return nil, discussionanswer.ErrNoOpen
	}
	if err != nil {
		return nil, err
	}
	return r.read(ctx, ticketID, attempt, rec.Round)
}

// Round reads the question of one round of the attempt's discussion.
func (r *DiscussionReader) Round(ctx context.Context, ticketID int64, attempt, round int) (*discussionanswer.Open, error) {
	return r.read(ctx, ticketID, attempt, round)
}

func (r *DiscussionReader) read(ctx context.Context, ticketID int64, attempt, round int) (*discussionanswer.Open, error) {
	t, err := r.tickets.Get(ctx, ticketID)
	if err != nil {
		return nil, fmt.Errorf("read ticket %d: %w", ticketID, err)
	}
	open := &discussionanswer.Open{Attempt: attempt, Round: round, Revision: t.Revision}
	// A ticket with no change has no head, and a head that cannot be read must
	// not match the label's: the empty head makes an approval stale rather than
	// letting it through on a fact that could not be checked.
	if head, err := r.heads.CurrentHead(ctx, ticketID); err == nil {
		open.HeadSHA = head
	}
	comments, err := r.tickets.ListComments(ctx, ticketID)
	if err != nil {
		return nil, fmt.Errorf("list comments of ticket %d: %w", ticketID, err)
	}
	// A round no worker asked (an escalated release, a merge into the base
	// branch, a person's action) has the system's comment as its question.
	mine := forAttempt(comments, attempt)
	if c := newestAgentComment(mine); c != nil {
		open.Question = c.Body
	} else if len(mine) > 0 {
		open.Question = mine[len(mine)-1].Body
	}
	contexts, err := r.tickets.ListAgentContexts(ctx, ticketID)
	if err != nil {
		return nil, fmt.Errorf("list handoffs of ticket %d: %w", ticketID, err)
	}
	for i := len(contexts) - 1; i >= 0; i-- {
		if contexts[i].AttemptNumber == int64(attempt) {
			open.Handoff = contexts[i].Body
			break
		}
	}
	return open, nil
}

// forAttempt is the comments addressed to the attempt or to the whole ticket.
func forAttempt(comments []ticket.Comment, attempt int) []ticket.Comment {
	var out []ticket.Comment
	for _, c := range comments {
		if c.AttemptNumber == nil || *c.AttemptNumber == int64(attempt) {
			out = append(out, c)
		}
	}
	return out
}

// NewDiscussionHeads is the head reader the open discussion is checked against.
func NewDiscussionHeads(d *Deps) steward.HeadReader {
	return NewChangeReader(d.Hosts, change.NewPostgresStore(d.Database), d.Tickets)
}

// NewDiscussionAssessor is the engine a discussion answer's checks run on: the
// same checks as a live decision.
func NewDiscussionAssessor(d *Deps) discussionanswer.Assessor { return newEngine(d) }
