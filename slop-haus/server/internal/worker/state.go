// Package worker is the REST surface a headless worker's supervisor runs on:
// what an agent holds and what became of it, and a long poll for when that
// changes. Everything the MCP tools tell a model in-turn, it must be able to
// ask outside one.
package worker

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/change"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/reviewreport"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// Tickets is the slice of ticket.Service the state is read from.
type Tickets interface {
	WorkerSnapshot(ctx context.Context, agentID uuid.UUID) (*ticket.WorkerSnapshot, error)
	LeaseEnd(ctx context.Context, leaseID int64) (*ticket.LeaseEnd, error)
	IntegrationLockStateFor(ctx context.Context, agentID uuid.UUID) (*ticket.LockState, error)
	Get(ctx context.Context, id int64) (*ticket.Ticket, error)
}

// Changes is the slice of change.Service the checks are read from. The
// tenant is the authenticated agent's.
type Changes interface {
	Checks(ctx context.Context, tenant, agentID uuid.UUID, expectedSHA string) (*change.Report, error)
}

// Reviews is the slice of reviewreport.Service the review request is read
// from.
type Reviews interface {
	OpenRequest(ctx context.Context, agentID uuid.UUID, ticketID int64, attempt int) (*reviewreport.Request, error)
}

// ChecksUnknown is the state of a change whose checks could not be read just
// now (the code host did not answer). It is not a pass, and not a failure
// either: the supervisor reads again.
const ChecksUnknown = "unknown"

// State is where an agent stands, as a supervisor decides its next step from.
type State struct {
	// Lease is the lease the agent holds; nil when it holds none.
	Lease *LeaseState
	// Ticket and Attempt are those of the agent's latest lease, held or ended:
	// after a run that called ticket_finish (which went to review) the lease is gone and the ticket's
	// status is how the supervisor knows why.
	Ticket  *TicketState
	Attempt *AttemptState
	// Ended says how that latest lease ended; nil while it is held.
	Ended *EndState
	// Lock is the agent's place at the integration lock; only a lease holds or
	// queues for it.
	Lock LockPlace
	// Checks are those of the held attempt's change; nil with no change open
	// or no lease.
	Checks *ChecksState
	// ReviewRequest is the agent's open request for a review of the held
	// attempt, which its supervisor runs; nil with none open or no lease.
	ReviewRequest *reviewreport.Request
}

type LeaseState struct {
	ID        int64
	TicketID  int64
	Attempt   int
	ExpiresAt time.Time
}

type TicketState struct {
	ID     int64
	Status ticket.Status
	// Paths are the ticket's path scopes as they are now, which tell a supervisor which repo of a
	// nested workspace the ticket's run belongs in.
	Paths []string
}

type AttemptState struct {
	Number   int
	Status   string
	Promoted bool
	// Session is what a run reported on the attempt; nil when none has.
	Session *ticket.AttemptSession
}

// EndState is a ticket.LeaseEnd.
type EndState struct {
	Kind ticket.LeaseEndKind
	// Status is where a human moved the ticket, for a revoked lease.
	Status ticket.Status
	// Exit is how the holder left, for a finished one.
	Exit ticket.LeaseExit
}

// LockPlace is the agent's standing at the integration lock.
type LockPlace struct {
	// Holds is whether the agent has the lock.
	Holds bool
	// Position is its place in the queue, counting from 1; 0 when not queued.
	Position int
	// Offered is whether the lock is free and the agent is first in line to
	// take it.
	Offered bool
}

type ChecksState struct {
	URL     string
	HeadSHA string
	// State is success, failure, pending or none as change_checks says, or
	// ChecksUnknown.
	State  string
	Passed bool
	// Error is why the state is unknown.
	Error string
}

// Service reads an agent's state.
type Service struct {
	tickets Tickets
	// changes may be nil, which reports no checks: a tenant without a code
	// host credential has no changes slop-haus can read.
	changes Changes
	// reviews may be nil, which reports no review request.
	reviews Reviews
	logger  *slog.Logger
}

func NewService(tickets Tickets, changes Changes, reviews Reviews, logHandler slog.Handler) *Service {
	return &Service{tickets: tickets, changes: changes, reviews: reviews, logger: slog.New(logHandler)}
}

// State reads the agent's standing without changing any of it: it extends no
// lease, takes no revocation notice and joins no queue, so a supervisor can
// ask as often as it likes.
//
// It is assembled from several reads, with a claim or a release able to land
// between them. A state whose lease is not the one it started from would say
// "no lease" about an agent that has just claimed one, or describe the new
// lease's change under the old one, so it is read again until the lease it
// began with is the lease it ends with.
func (s *Service) State(ctx context.Context, tenant, agentID uuid.UUID) (*State, error) {
	var state *State
	for range stateReads {
		began, err := s.snapshot(ctx, agentID)
		if err != nil {
			return nil, err
		}
		if state, err = s.read(ctx, tenant, agentID, began); err != nil {
			return nil, err
		}
		ended, err := s.snapshot(ctx, agentID)
		if err != nil {
			return nil, err
		}
		if sameLease(began, ended) {
			return state, nil
		}
	}
	// Still moving after every look: the last one is as true as any.
	return state, nil
}

// stateReads bounds how often a state is read again for a lease that keeps
// changing under it.
const stateReads = 3

// snapshot is the agent's latest lease, or nil if it never held one.
func (s *Service) snapshot(ctx context.Context, agentID uuid.UUID) (*ticket.WorkerSnapshot, error) {
	snap, err := s.tickets.WorkerSnapshot(ctx, agentID)
	if errors.Is(err, ticket.ErrNoLease) {
		return nil, nil
	}
	return snap, err
}

func sameLease(a, b *ticket.WorkerSnapshot) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.LeaseID == b.LeaseID && a.Active == b.Active
}

// read builds the state around snap, the latest lease as it began.
func (s *Service) read(ctx context.Context, tenant, agentID uuid.UUID, snap *ticket.WorkerSnapshot) (*State, error) {
	if snap == nil {
		return &State{}, nil
	}
	state := &State{
		Ticket:  &TicketState{ID: snap.TicketID, Status: snap.TicketStatus},
		Attempt: &AttemptState{Number: snap.Attempt, Status: snap.AttemptStatus, Promoted: snap.Promoted, Session: snap.Session},
	}
	t, err := s.tickets.Get(ctx, snap.TicketID)
	if err != nil {
		return nil, err
	}
	for _, p := range t.Paths {
		state.Ticket.Paths = append(state.Ticket.Paths, p.Path)
	}
	if !snap.Active {
		end, err := s.tickets.LeaseEnd(ctx, snap.LeaseID)
		if err != nil {
			return nil, err
		}
		if end != nil {
			state.Ended = &EndState{Kind: end.Kind, Status: end.Status, Exit: end.Exit}
			// A ticket handed to review can be merged and closed by the
			// server afterwards; the lease then ended as a review (or a
			// release) but the ticket is finished, and a supervisor must not
			// resume or re-claim it.
			if end.Kind == ticket.LeaseFinished && snap.TicketStatus == ticket.StatusDone {
				state.Ended.Exit = ticket.ExitDone
			}
		}
		return state, nil
	}
	state.Lease = &LeaseState{ID: snap.LeaseID, TicketID: snap.TicketID, Attempt: snap.Attempt, ExpiresAt: snap.ExpiresAt}

	locks, err := s.tickets.IntegrationLockStateFor(ctx, agentID)
	if err != nil {
		return nil, err
	}
	state.Lock = lockPlace(locks, agentID)
	state.Checks = s.checks(ctx, tenant, agentID)
	if s.reviews != nil {
		// Unlike the checks, an unreadable request fails the read: a state
		// saying "no request" would leave the run waiting on a review its
		// supervisor never starts.
		if state.ReviewRequest, err = s.reviews.OpenRequest(ctx, agentID, snap.TicketID, snap.Attempt); err != nil {
			return nil, err
		}
	}
	return state, nil
}

func lockPlace(locks *ticket.LockState, agentID uuid.UUID) LockPlace {
	var place LockPlace
	place.Holds = locks.Holder != nil && locks.Holder.AgentID == agentID
	for i := range locks.Queue {
		if locks.Queue[i].AgentID == agentID {
			place.Position = i + 1
		}
	}
	place.Offered = locks.Holder == nil && place.Position == 1
	return place
}

func (s *Service) checks(ctx context.Context, tenant, agentID uuid.UUID) *ChecksState {
	if s.changes == nil {
		return nil
	}
	report, err := s.changes.Checks(ctx, tenant, agentID, "")
	switch {
	case errors.Is(err, change.ErrNoChange):
		return nil
	case err != nil:
		// The rest of the state is still true, and is what a supervisor
		// needs to decide on: a host that is down for a minute does not make
		// the lock queue unreadable.
		s.logger.Warn("worker state: read the change's checks", "agent_id", agentID, "err", err)
		return &ChecksState{State: ChecksUnknown, Error: err.Error()}
	}
	return &ChecksState{URL: report.URL, HeadSHA: report.HeadSHA, State: report.State, Passed: report.Passed}
}
