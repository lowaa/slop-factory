package ticket

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/harness"
)

// Harnesses a run can be on; the same two a variant is on.
const (
	HarnessClaude = "claude"
	HarnessPi     = "pi"
)

// maxSessionIDLen mirrors the column's CHECK.
const maxSessionIDLen = 200

// AttemptSession is the harness a run used on an attempt and the session it
// ran in, which the same agent resumes when it picks the attempt up again.
type AttemptSession struct {
	Harness   string
	SessionID string
}

var (
	// ErrInvalidHarness is a harness the registry does not list.
	ErrInvalidHarness = errors.New("ticket: the harness must be a registered harness")
	// ErrInvalidSessionID is a session id that is empty, too long or holds
	// control characters; it ends up in a command line the supervisor runs.
	ErrInvalidSessionID = errors.New("ticket: the session id must be 1 to 200 printable characters")
	// ErrEmptyNote guards an escalation: a ticket must never reach a human
	// without saying why.
	// ErrSessionHarnessMismatch is a session on another harness than the
	// variant the lease was granted under: the claim was checked against the
	// variant's, and the record must not get round it.
	ErrSessionHarnessMismatch = errors.New("ticket: the session's harness is not the harness of the agent's variant")
	ErrEmptyNote              = errors.New("ticket: an escalation needs a note for the human")
)

func validHarness(h string) bool { return harness.Valid(h) }

func (s *AttemptSession) validate() error {
	if !validHarness(s.Harness) {
		return ErrInvalidHarness
	}
	if s.SessionID == "" || len(s.SessionID) > maxSessionIDLen ||
		strings.IndexFunc(s.SessionID, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) }) >= 0 {
		return ErrInvalidSessionID
	}
	return nil
}

// RecordSession sets the session on the attempt the agent holds, replacing
// the one an earlier run left: a new run of the attempt has a new session.
func (s *Service) RecordSession(ctx context.Context, agentID uuid.UUID, session *AttemptSession) error {
	if err := session.validate(); err != nil {
		return err
	}
	return s.store.OnHeldLease(ctx, agentID, func(tx TransitionTx, lease *HeldLease) error {
		var variantHarness *string
		if err := tx.Querier().QueryRow(ctx, `
			SELECT v.harness FROM lease_configs c
			  JOIN variant_versions v ON v.id = c.variant_id
			 WHERE c.lease_id = $1`, lease.ID).Scan(&variantHarness); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("read the lease's variant: %w", err)
		}
		if variantHarness != nil && *variantHarness != session.Harness {
			return fmt.Errorf("%w: the variant runs %s", ErrSessionHarnessMismatch, *variantHarness)
		}
		if _, err := tx.Querier().Exec(ctx, `
			UPDATE attempts SET harness = $3, harness_session_id = $4
			 WHERE ticket_id = $1 AND number = $2`,
			lease.TicketID, lease.Attempt, session.Harness, session.SessionID); err != nil {
			return fmt.Errorf("record the attempt's session: %w", err)
		}
		return nil
	})
}

// escalationPrefix marks the comment as the supervisor's, not the agent's
// words. The schema has no system author, so the comment is recorded under
// the agent whose run it is about.
const escalationPrefix = "[supervisor] "

// Escalate sends the agent's held ticket to discussion with the supervisor's
// note, as ticket_discuss does for a model that asks: the lease released and
// first claim owed to the agent. It does not count as the attempt's
// discussion (Discussed): a model never asked the question, so a ticket that
// needs discussion still does.
func (s *Service) Escalate(ctx context.Context, agentID uuid.UUID, note string) (*Ticket, error) {
	note = strings.TrimSpace(note)
	if note == "" {
		return nil, ErrEmptyNote
	}
	return s.exitHeld(ctx, agentID, escalateExit(note))
}

func escalateExit(note string) heldExit {
	return func(_ *Ticket, agentID uuid.UUID) (*Change, error) {
		reason := "discussion"
		return &Change{
			Comment:       &NewComment{By: AgentAuthor(agentID), Body: escalationPrefix + note},
			Handoff:       note,
			ReleaseReason: &reason,
			Ticket: TicketWrite{
				Status:     To(StatusDiscussion),
				FirstClaim: FirstClaimOwedTo(agentID),
			},
		}, nil
	}
}
