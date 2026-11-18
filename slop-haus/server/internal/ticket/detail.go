package ticket

import (
	"context"
	"errors"
	"slices"

	"github.com/google/uuid"
)

// Detail is a ticket with what an agent resuming it needs: the conversation
// with the human, the handoff the last agent left, and the edits to what it
// asks for.
type Detail struct {
	Ticket   *Ticket
	Comments []Comment
	// Handoff is the latest agent context, or nil if none was left. Only
	// the latest: each supersedes the one before, and the history is the
	// human's to read on the board, not a cost every resuming agent pays in
	// context.
	Handoff   *AgentContext
	Revisions []Revision
	// External is what an intake source sent, oldest first: never to be
	// shown except as external content (RenderExternal).
	External []ExternalEvent
	// Held is the attempt the reading agent holds a live lease for, or nil:
	// the one whose branch it pushes to.
	Held *AttemptRef
	// Branch is Held's branch, made again if the claim could not make it.
	Branch *AttemptBranch
}

// Detail reads a ticket and its history, every attempt's conversation
// included: for a person. An agent reads DetailFor.
func (s *Service) Detail(ctx context.Context, id int64) (*Detail, error) {
	return s.detail(ctx, id, EveryConversation())
}

// DetailFor is Detail as agentID may see it: the ticket's own comments and its
// attempt's, never a sibling's (see ConversationOf), the latest handoff of
// that attempt, and the ticket as that attempt sees it. The one read an agent
// is given a ticket's conversation through.
func (s *Service) DetailFor(ctx context.Context, id int64, agentID uuid.UUID) (*Detail, error) {
	conv, err := s.store.ConversationOf(ctx, id, agentID)
	if err != nil {
		return nil, err
	}
	detail, err := s.detail(ctx, id, conv)
	if err != nil {
		return nil, err
	}
	held, err := s.store.HeldAttempt(ctx, agentID, id)
	if errors.Is(err, ErrNoLease) {
		return detail, nil
	}
	if err != nil {
		return nil, err
	}
	detail.Held = held
	// The pin and the branch are retried here, as at the claim: a host that
	// was down then must not leave the agent without a branch for good.
	detail.Ticket = s.PinBase(ctx, detail.Ticket)
	detail.Branch = s.EnsureAttemptBranch(ctx, detail.Ticket, held.Number)
	return detail, nil
}

func (s *Service) detail(ctx context.Context, id int64, conv Conversation) (*Detail, error) {
	found, err := s.store.TicketSeenBy(ctx, id, conv)
	if err != nil {
		return nil, err
	}
	comments, err := s.store.ListCommentsIn(ctx, id, conv)
	if err != nil {
		return nil, err
	}
	handoffs, err := s.store.ListAgentContextsIn(ctx, id, conv)
	if err != nil {
		return nil, err
	}
	revisions, err := s.ListRevisions(ctx, id)
	if err != nil {
		return nil, err
	}
	var external []ExternalEvent
	if found.Intake != nil {
		if external, err = s.ListExternal(ctx, id); err != nil {
			return nil, err
		}
	}
	if conv.Replay {
		// The edits and outside events since the snapshot are the hindsight
		// it exists to keep from a replay.
		snap, err := s.store.Snapshot(ctx, id)
		if err != nil {
			return nil, err
		}
		revisions = slices.DeleteFunc(revisions, func(r Revision) bool { return r.Number > snap.Revision })
		external = slices.DeleteFunc(external, func(e ExternalEvent) bool { return e.At.After(snap.AsOf) })
	}
	detail := &Detail{Ticket: found, Comments: comments, Revisions: revisions, External: external}
	if len(handoffs) > 0 {
		detail.Handoff = &handoffs[len(handoffs)-1]
	}
	return detail, nil
}
