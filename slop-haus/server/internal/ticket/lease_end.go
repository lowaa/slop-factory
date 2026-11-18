package ticket

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// LeaseEndKind is how a lease an agent held stopped being held.
type LeaseEndKind string

const (
	// LeaseRevoked: a human moved or cancelled the ticket (LeaseEnd.Status).
	LeaseRevoked LeaseEndKind = "revoked"
	// LeaseLapsed: the lease ran out, or was released for any reason that
	// is not the holder's doing (an agent being disabled, say), so the
	// ticket may already be someone else's.
	LeaseLapsed LeaseEndKind = "lapsed"
	// LeaseFinished: the holder gave the ticket up (LeaseEnd.Exit).
	LeaseFinished LeaseEndKind = "finished"
)

// LeaseExit is which of the holder's exits ended a finished lease.
type LeaseExit string

const (
	ExitDone       LeaseExit = "done"
	ExitReview     LeaseExit = "review"
	ExitDiscussion LeaseExit = "discussion"
	ExitPreparing  LeaseExit = "preparing"
	// ExitReleased: ticket_release, which can also end up as a discussion
	// when the ticket's cooldowns escalate; the ticket's status says.
	ExitReleased LeaseExit = "released"
)

// LeaseEnd is what became of a lease that is no longer held.
type LeaseEnd struct {
	TicketID int64
	// AgentID is the lease's holder; unset for a lease that no longer exists
	// at all. A caller that takes a lease id from a client checks it is the
	// client's own before telling it anything about the end.
	AgentID uuid.UUID
	Kind    LeaseEndKind
	// Status is where a human moved the ticket, for LeaseRevoked.
	Status Status
	// Exit is how the holder left, for LeaseFinished.
	Exit LeaseExit
}

// LeaseEnd reports what became of the lease, or nil while it is still held.
// It only reads: unlike a heartbeat it neither extends the lease nor takes
// the revocation notice, so a watcher of someone else's lease (a dispatching
// session waiting on its worker's ticket) leaves both as they were.
func (s *PostgresStore) LeaseEnd(ctx context.Context, leaseID int64) (*LeaseEnd, error) {
	var (
		end        = LeaseEnd{}
		held       bool
		released   bool
		reason     *string
		revokedTo  *string
		ticketStat string
	)
	err := s.db.QueryRow(ctx, `
		SELECT l.ticket_id, l.agent_id, (`+activeLeaseClause+`), l.released_at IS NOT NULL,
		       l.release_reason, l.revoked_to, t.status
		  FROM leases l JOIN tickets t ON t.id = l.ticket_id
		 WHERE l.id = $1`, leaseID).
		Scan(&end.TicketID, &end.AgentID, &held, &released, &reason, &revokedTo, &ticketStat)
	if errors.Is(err, pgx.ErrNoRows) {
		return &LeaseEnd{Kind: LeaseLapsed}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load lease end: %w", err)
	}
	if held {
		return nil, nil
	}
	switch {
	case revokedTo != nil:
		end.Kind, end.Status = LeaseRevoked, Status(*revokedTo)
	case !released || reason == nil || *reason == "expired" || *reason == "agent disabled":
		end.Kind = LeaseLapsed
	default:
		end.Kind, end.Exit = LeaseFinished, exitOf(*reason, Status(ticketStat))
	}
	return &end, nil
}

// exitOf names the exit that wrote reason. Release reasons are free text for
// ticket_release, so the fixed words of the other exits count only when the
// ticket is where that exit leaves it: a release that says "done" has not
// finished anything.
func exitOf(reason string, status Status) LeaseExit {
	for _, e := range []struct {
		exit   LeaseExit
		status Status
	}{
		{ExitDone, StatusDone},
		{ExitReview, StatusReview},
		{ExitDiscussion, StatusDiscussion},
		{ExitPreparing, StatusPreparing},
	} {
		if reason == string(e.exit) && status == e.status {
			return e.exit
		}
	}
	return ExitReleased
}
