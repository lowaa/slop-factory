package ticket

import (
	"context"
	"fmt"
	"time"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
)

// DiscussionEntry is an attempt's entry into a round of discussion, committed.
type DiscussionEntry struct {
	TicketID int64
	Attempt  int
	Round    int
}

// DiscussionHook is told of every round of discussion, whatever route opened
// it, after the transaction that opened it has committed. Like ApprovalHook it runs off the
// discussing request, so a slow or failing hook cannot undo the discussion.
// It may be told of a round twice and must key what it makes on the round.
type DiscussionHook interface {
	Entered(ctx context.Context, e *DiscussionEntry) error
}

const discussionHookTimeout = 30 * time.Second

// WithDiscussionHook sets the hook told of discussion rounds. Without one,
// none is.
func WithDiscussionHook(h DiscussionHook) ServiceOption {
	return func(s *Service) { s.discussionHook = h }
}

// discussionNotifier is a store that reports, after commit, the rounds its
// transactions opened. Opening a round is a state change of the attempt, so
// the store sees every route into discussion (a worker's, an escalated
// release, a person's, a merge into the base branch), which a caller of the
// store cannot.
type discussionNotifier interface {
	onDiscussion(func(ctx context.Context, entries []*DiscussionEntry))
}

// notifyDiscussion tells the hook of a committed entry.
func (s *Service) notifyDiscussion(ctx context.Context, e *DiscussionEntry) {
	if e == nil || s.discussionHook == nil {
		return
	}
	hook := s.discussionHook
	s.discussionCalls.Add(1)
	ctx = context.WithoutCancel(ctx)
	go func() {
		defer s.discussionCalls.Done()
		defer func() {
			if r := recover(); r != nil {
				s.logger.ErrorContext(ctx, "discussion hook panicked", "ticket", e.TicketID, "attempt", e.Attempt, "panic", r)
			}
		}()
		ctx, cancel := context.WithTimeout(ctx, discussionHookTimeout)
		defer cancel()
		if err := hook.Entered(ctx, e); err != nil {
			s.logger.ErrorContext(ctx, "discussion hook failed", "ticket", e.TicketID, "attempt", e.Attempt, "error", err)
		}
	}()
}

// WaitDiscussionHooks blocks until the hook calls started so far have
// returned: for a shutdown that should not cut one off, and for tests.
func (s *Service) WaitDiscussionHooks() { s.discussionCalls.Wait() }

// AttemptDiscussionBody is an open discussion round of an attempt and the
// steward's label for it. It is for the board: a worker is never shown it.
type AttemptDiscussionBody struct {
	Round int `json:"round" doc:"The attempt's current discussion round; addresses the steward draft endpoints."`
	// Label is null until the round has one, which says nothing about it.
	Label    *string `json:"label" enum:"pending,routine,yours,not_assessed" doc:"The steward's classification of the round."`
	Category string  `json:"category,omitempty" doc:"The answer template category of a routine label."`
	Reason   string  `json:"reason,omitempty" doc:"Why the steward labelled the round so."`
}

// AttemptDiscussions reads the open discussion of a ticket's attempts.
type AttemptDiscussions interface {
	// AttemptDiscussions maps attempt number to its open round; an attempt
	// not in discussion has no entry.
	AttemptDiscussions(ctx context.Context, ticketID int64) (map[int]*AttemptDiscussionBody, error)
}

// WithAttemptDiscussions serves each attempt's open discussion round and
// label on the ticket detail, to people only. Without it none is served.
func WithAttemptDiscussions(d AttemptDiscussions) HandlerOption {
	return func(h *Handler) { h.discussions = d }
}

// PostgresAttemptDiscussions reads attempt discussions from the attempts and
// the steward's labels.
type PostgresAttemptDiscussions struct{ db *db.DB }

func NewPostgresAttemptDiscussions(database *db.DB) *PostgresAttemptDiscussions {
	return &PostgresAttemptDiscussions{db: database}
}

func (p *PostgresAttemptDiscussions) AttemptDiscussions(ctx context.Context, ticketID int64) (map[int]*AttemptDiscussionBody, error) {
	rows, err := p.db.Query(ctx, `
		SELECT a.number, GREATEST(a.discussion_round, 1), l.label, l.category, l.reason
		  FROM attempts a
		  LEFT JOIN steward_discussion_labels l
		    ON l.tenant_id = a.tenant_id AND l.ticket_id = a.ticket_id AND l.attempt = a.number AND l.round = GREATEST(a.discussion_round, 1)
		 WHERE a.ticket_id = $1 AND a.status = 'discussion'`, ticketID)
	if err != nil {
		return nil, fmt.Errorf("read discussions of ticket #%d: %w", ticketID, err)
	}
	defer rows.Close()
	out := map[int]*AttemptDiscussionBody{}
	for rows.Next() {
		var number int
		var d AttemptDiscussionBody
		var category, reason *string
		if err := rows.Scan(&number, &d.Round, &d.Label, &category, &reason); err != nil {
			return nil, fmt.Errorf("read discussions of ticket #%d: %w", ticketID, err)
		}
		if category != nil {
			d.Category = *category
		}
		if reason != nil {
			d.Reason = *reason
		}
		out[number] = &d
	}
	return out, rows.Err()
}

func fillAttemptDiscussions(ctx context.Context, d AttemptDiscussions, ticketID int64, attempts []AttemptBody) error {
	if d == nil || len(attempts) == 0 {
		return nil
	}
	got, err := d.AttemptDiscussions(ctx, ticketID)
	if err != nil {
		return err
	}
	for i := range attempts {
		attempts[i].Discussion = got[attempts[i].Number]
	}
	return nil
}
