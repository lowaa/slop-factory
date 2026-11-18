package stewardwire

import (
	"context"
	"fmt"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/steward/discussionpick"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// PickDiscussions reads a ticket's open discussion for discussion_pick. It
// implements discussionpick.DiscussionReader.
type PickDiscussions struct{ tickets DiscussionTickets }

func NewPickDiscussions(tickets DiscussionTickets) *PickDiscussions {
	return &PickDiscussions{tickets: tickets}
}

// Discussion is the question and handoff of the newest attempt that is in
// discussion, or nil when none is.
func (r *PickDiscussions) Discussion(ctx context.Context, ticketID int64) (*discussionpick.Discussion, error) {
	attempts, err := r.tickets.Attempts(ctx, ticketID)
	if err != nil {
		return nil, fmt.Errorf("list attempts of ticket %d: %w", ticketID, err)
	}
	attempt := -1
	for _, a := range attempts {
		if a.Status == string(ticket.StatusDiscussion) && a.Number > attempt {
			attempt = a.Number
		}
	}
	if attempt < 0 {
		return nil, nil
	}
	comments, err := r.tickets.ListComments(ctx, ticketID)
	if err != nil {
		return nil, fmt.Errorf("list comments of ticket %d: %w", ticketID, err)
	}
	d := &discussionpick.Discussion{}
	if c := newestAgentComment(forAttempt(comments, attempt)); c != nil {
		d.Question = c.Body
	}
	contexts, err := r.tickets.ListAgentContexts(ctx, ticketID)
	if err != nil {
		return nil, fmt.Errorf("list handoffs of ticket %d: %w", ticketID, err)
	}
	for i := len(contexts) - 1; i >= 0; i-- {
		if contexts[i].AttemptNumber == int64(attempt) {
			d.Handoff = contexts[i].Body
			break
		}
	}
	return d, nil
}

// noPinnedWords is the word list when no instructions store is wired: nothing
// is pinned, so topic_not_sensitive fails closed.
type noPinnedWords struct{}

func (noPinnedWords) Pinned(context.Context) (*discussionpick.WordList, error) { return nil, nil }

// pinnedWords is the pinned sensitive word list, or none when the service has
// no instructions store.
func pinnedWords(i InstructionsReader) discussionpick.WordListReader {
	if i == nil {
		return noPinnedWords{}
	}
	return discussionpick.NewPinnedWords(i)
}

var (
	_ discussionpick.DiscussionReader    = (*PickDiscussions)(nil)
	_ discussionpick.ProtectedPathReader = (*ProtectedPaths)(nil)
)
