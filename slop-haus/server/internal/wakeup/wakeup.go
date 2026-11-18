// Package wakeup answers the supervisor's worker polls: it holds a poll
// open until the agent (or any of a supervisor's slots) has something to do.
//
// It polls the database rather than being notified. Several of the ways a
// ticket becomes claimable involve no write to notify on -- a
// resume-affinity window closing, a lease lapsing -- and the probe is
// ticket.Service.Peek, the same read-only question the Stop hook asks, so a
// poll can't disagree with what a claim would grant.
package wakeup

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/tenantstate"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// DefaultPollInterval is how often a held poll probes: how
// late an agent can hear about new work, against one read-only query per
// idle agent per interval.
const DefaultPollInterval = 5 * time.Second

// Reason is why a poll reported an event.
type Reason string

const (
	// ReasonClaimable: a ticket the agent can take (Wake.TicketID).
	ReasonClaimable Reason = "claimable"
	// ReasonMessages: the human has said something to the agent
	// (Wake.Messages undelivered), which the woken call delivers.
	ReasonMessages Reason = "messages"
	// ReasonRevoked: a human moved or cancelled the ticket the agent was
	// holding (Wake.TicketID, Wake.Ended the status it was moved to).
	ReasonRevoked Reason = "revoked"
	// ReasonLeaseLost: the lease on the ticket lapsed, or was released by
	// something other than the holder (Wake.TicketID). Whoever was working
	// it has gone quiet, or the ticket has been given to another agent.
	ReasonLeaseLost Reason = "lease_lost"
	// ReasonFinished: the holder gave the ticket up (Wake.TicketID,
	// Wake.Ended the exit: done, review, discussion, preparing or
	// released).
	ReasonFinished Reason = "finished"
	// ReasonDisabled: ticket processing is switched off, so nothing will
	// become claimable and the agent should stop.
	ReasonDisabled Reason = "disabled"
	// ReasonRestart: the server is shutting down for a deploy. Its
	// replacement is already serving, so the agent should call again.
	ReasonRestart Reason = "restart"
	// ReasonProtocolUnsupported: the supervisor speaks a protocol older than
	// the server still supports (Wake.Message says what to do). Nothing else
	// is reported to it.
	ReasonProtocolUnsupported Reason = "protocol_unsupported"
	// ReasonOffered: the integration lock is free and the agent is first in
	// its queue.
	ReasonOffered Reason = "offered"
)

// Wake is one thing a poll found for an agent.
type Wake struct {
	Reason Reason
	// TicketID is the ticket the wake is about.
	TicketID int64
	Messages int
	// LeaseID names the lease a worker poll's end event is about.
	LeaseID int64
	// SlotID is the supervisor's slot the wake is about; the zero UUID for a poll
	// that is not a supervisor's.
	SlotID uuid.UUID
	// Ended says how a held ticket's lease ended: for ReasonRevoked the
	// status a human moved it to, for ReasonFinished the exit.
	Ended string
	// Message is readable text for the operator, for ReasonProtocolUnsupported.
	Message string
}

// Tickets is the slice of ticket.Service a poll probes.
type Tickets interface {
	LeaseFor(ctx context.Context, agentID uuid.UUID) (*ticket.Lease, error)
	// LeaseEnd reads what became of a lease, nil while it is held. It must
	// neither extend the lease nor take the agent's revocation notice.
	LeaseEnd(ctx context.Context, leaseID int64) (*ticket.LeaseEnd, error)
	Peek(ctx context.Context, agentID uuid.UUID, only *int64) (*ticket.ClaimResult, error)
}

// Messages reports whether the human has said something to the agent that
// it has not yet been handed.
type Messages interface {
	Pending(ctx context.Context, agentID uuid.UUID) (int, error)
}

// Policy is the processing switch.
type Policy interface {
	TicketProcessingEnabled(ctx context.Context) (bool, error)
}

// Clock is the time a poll runs on. Tests drive it by hand, so a poll's
// behaviour is checked step by step instead of against wall-clock sleeps.
type Clock interface {
	// Ticker fires every d until stop is called.
	Ticker(d time.Duration) (ticks <-chan time.Time, stop func())
	// After fires once, after d.
	After(d time.Duration) <-chan time.Time
}

type Deps struct {
	Tickets Tickets
	// Messages may be nil, which hears nothing.
	Messages Messages
	Policy   Policy
	// LockStates and Polls: without LockStates no lock offer is reported,
	// without Polls no poll is recorded.
	LockStates LockStates
	Polls      PollRecorder
	// Slots is needed only by SupervisorPoll: without it a supervisor hears
	// nothing.
	Slots Slots
	// Archive may be nil, which sees no tenant as archived. A poll for an
	// archived tenant ends as ReasonDisabled and records nothing.
	Archive tenantstate.Archive
}

type Options struct {
	PollInterval time.Duration
	// Clock defaults to the real one.
	Clock Clock
	// WorkerPollTimeout caps how long one worker poll is held open.
	WorkerPollTimeout time.Duration
	// SupervisorVersion is the version a supervisor should be running; a
	// worker poll from any other is told to restart. Empty never restarts.
	SupervisorVersion string
	// MinSupervisorProtocol overrides MinSupervisorProtocol, which tests raise.
	MinSupervisorProtocol int
}

// Waiter holds the supervisors' polls.
type Waiter struct {
	deps  Deps
	poll  time.Duration
	clock Clock
	// workerTimeout and expectedVersion are for WorkerPoll.
	polls           workerPolls
	workerTimeout   time.Duration
	expectedVersion string
	minProtocol     int
	closed          chan struct{}
	closeOnce       sync.Once
	logger          *slog.Logger
}

func New(deps Deps, opts *Options, logHandler slog.Handler) *Waiter {
	o := Options{PollInterval: DefaultPollInterval, Clock: RealClock{}}
	if opts != nil {
		if opts.PollInterval > 0 {
			o.PollInterval = opts.PollInterval
		}
		if opts.Clock != nil {
			o.Clock = opts.Clock
		}
		o.WorkerPollTimeout, o.SupervisorVersion = opts.WorkerPollTimeout, opts.SupervisorVersion
		o.MinSupervisorProtocol = opts.MinSupervisorProtocol
	}
	if o.MinSupervisorProtocol <= 0 {
		o.MinSupervisorProtocol = MinSupervisorProtocol
	}
	if o.WorkerPollTimeout <= 0 {
		o.WorkerPollTimeout = DefaultWorkerPollTimeout
	}
	return &Waiter{
		deps:            deps,
		poll:            o.PollInterval,
		clock:           o.Clock,
		workerTimeout:   o.WorkerPollTimeout,
		expectedVersion: o.SupervisorVersion,
		minProtocol:     o.MinSupervisorProtocol,
		closed:          make(chan struct{}),
		logger:          slog.New(logHandler),
	}
}

// Close ends every poll in progress, and every later one at once, so the
// supervisor polls again against the replacement. Called at shutdown:
// http.Server.Shutdown waits for in-flight requests, and a poll left to run
// would hold a deploy up until the shutdown deadline cut the connection
// instead.
func (w *Waiter) Close() {
	w.closeOnce.Do(func() { close(w.closed) })
}

func wakeForEnd(ticketID int64, end *ticket.LeaseEnd) *Wake {
	switch end.Kind {
	case ticket.LeaseRevoked:
		return &Wake{Reason: ReasonRevoked, TicketID: ticketID, Ended: string(end.Status)}
	case ticket.LeaseFinished:
		return &Wake{Reason: ReasonFinished, TicketID: ticketID, Ended: string(end.Exit)}
	default:
		return &Wake{Reason: ReasonLeaseLost, TicketID: ticketID}
	}
}

// archived reports whether the agent's tenant is archived, so that a poll
// for it is ended instead of held, and re-asked on every look so a tenant
// archived mid-poll releases the call it already has open. A failed read is
// "not archived": like the other reads here it must not wake an agent that
// is only waiting, and the archive's own token freeze is what shuts the
// agent out.
func (w *Waiter) archived(ctx context.Context, agentID uuid.UUID) bool {
	archived, err := tenantstate.ArchivedInContext(ctx, w.deps.Archive)
	if err != nil {
		w.logger.Warn("polling: look up whether the tenant is archived", "agent_id", agentID, "err", err)
		return false
	}
	return archived
}
