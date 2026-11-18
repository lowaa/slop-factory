package wakeup

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// DefaultWorkerPollTimeout is how long one worker poll is held open: as long
// as a remote control host's, so the same proxies and write deadlines bound it.
const DefaultWorkerPollTimeout = 30 * time.Second

// LockStates reads the integration lock without touching it. AwaitIntegrationLock
// is no use to a poll: it joins the queue and reserves the lock for the head,
// and a poll repeating every few seconds would hold the reservation open
// for an agent that is not going to take it yet.
type LockStates interface {
	IntegrationLockStateFor(ctx context.Context, agentID uuid.UUID) (*ticket.LockState, error)
}

// PollRecorder notes that an agent's supervisor polled. It is how a supervisor
// that has gone quiet is told from one with nothing to do.
type PollRecorder interface {
	RecordPoll(ctx context.Context, agentID uuid.UUID, version string, protocol int) error
}

// SupervisorProtocol is the protocol version this server speaks to a
// supervisor, sent on every poll as `protocol` and in X-Supervisor-Version.
// Raise it when the poll or slot-state contract changes in a way a removal of
// support for older supervisors will need to key on.
const SupervisorProtocol = 1

// MinSupervisorProtocol is the oldest protocol the server still serves.
// Additive for now: it is today's value, so nothing is refused, and a
// supervisor that sends none (older than the field) is never refused.
const MinSupervisorProtocol = 1

// WorkerPollRequest is one supervisor poll for one agent.
type WorkerPollRequest struct {
	// Version is the supervisor's own version. The server ships the
	// supervisor, so any version but the server's expected one is a stale
	// copy by definition; empty is not checked.
	Version string
	// Protocol is the supervisor's protocol version; 0 when it sent none.
	Protocol int
	// LeaseID is the lease the supervisor is working, if any. A poll can only
	// report that a lease ended if it is told which one to watch: asked
	// afterwards, "the agent holds nothing" cannot tell a revoked lease from
	// one that never was.
	LeaseID int64
	// SlotLeases is, for a supervisor's poll, the lease being worked by each
	// of its slots (see LeaseID); slots not named watch none.
	SlotLeases map[uuid.UUID]int64
	// Wait is how long to hold the poll open when there is nothing to report,
	// at most the waiter's worker poll timeout.
	Wait time.Duration
}

// Slots lists a supervisor's slots that can act: enabled workers it has not
// retired.
type Slots interface {
	SlotIDs(ctx context.Context, supervisorID uuid.UUID) ([]uuid.UUID, error)
}

// errSuperseded is why a poll was cancelled: the same agent polled again.
var errSuperseded = errors.New("wakeup: worker poll superseded by a newer one")

// workerPolls holds the one poll an agent has open on this replica. A
// supervisor that restarts, or retries a poll it gave up on, would otherwise
// leave its old poll running beside the new one for up to the timeout, each
// probing the database every interval: the same agent twice, for nothing.
type workerPolls struct {
	mu   sync.Mutex
	open map[uuid.UUID]*openPoll
}

type openPoll struct{ cancel context.CancelCauseFunc }

// begin ends the agent's open poll, if any, and makes cancel its new one.
// The returned func forgets it, unless a newer poll has taken its place.
func (p *workerPolls) begin(agentID uuid.UUID, cancel context.CancelCauseFunc) (forget func()) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.open == nil {
		p.open = map[uuid.UUID]*openPoll{}
	}
	if previous, ok := p.open[agentID]; ok {
		previous.cancel(errSuperseded)
	}
	mine := &openPoll{cancel: cancel}
	p.open[agentID] = mine
	return func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.open[agentID] == mine {
			delete(p.open, agentID)
		}
	}
}

// WorkerPoll is what a poll ended with: no events when it timed out or the
// server is shutting down, in which case the supervisor simply polls again.
type WorkerPoll struct {
	Events []*Wake
}

// WorkerPoll holds the supervisor's poll open until the agent has something
// to do, using the same probes as a claim so a poll cannot disagree about
// what is claimable.
//
// Events, by what the agent holds:
//   - always: restart, when Version is not the one expected.
//   - nothing: claimable work (a ticket owed to this agent by resume
//     affinity included), or disabled when ticket processing is off.
//   - a lease: revoked, finished or lease_lost once LeaseID's lease ends;
//     offered when the integration lock is free and this agent is first in
//     its queue; messages when a person has written to it. Messages are not
//     reported to an agent holding nothing: nothing over REST takes a pending
//     message, so an idle slot would be woken by it on every poll.
func (w *Waiter) WorkerPoll(ctx context.Context, agentID uuid.UUID, req *WorkerPollRequest) (*WorkerPoll, error) {
	return w.pollWith(ctx, agentID, req, func(ctx context.Context) []*Wake {
		return w.workerEvents(ctx, agentID, req.LeaseID)
	})
}

// SupervisorPoll is WorkerPoll for a supervisor, which polls once for all of
// its slots: each event is a slot's, named by SlotID, and a poll that
// finds nothing for any of them runs out. The poll is recorded as the
// supervisor's, which is what detection reads. A supervisor with no slots
// has nothing to hear, and simply polls on.
func (w *Waiter) SupervisorPoll(ctx context.Context, supervisorID uuid.UUID, req *WorkerPollRequest) (*WorkerPoll, error) {
	return w.pollWith(ctx, supervisorID, req, func(ctx context.Context) []*Wake {
		return w.slotEvents(ctx, supervisorID, req.SlotLeases)
	})
}

// slotEvents is one look at what each of the supervisor's slots has to do.
// The slots are read on every look: they come and go while a poll is held.
func (w *Waiter) slotEvents(ctx context.Context, supervisorID uuid.UUID, leases map[uuid.UUID]int64) []*Wake {
	if w.deps.Slots == nil {
		return nil
	}
	slots, err := w.deps.Slots.SlotIDs(ctx, supervisorID)
	if err != nil {
		w.logger.Warn("supervisor poll: list the slots", "supervisor", supervisorID, "err", err)
		return nil
	}
	var events []*Wake
	for _, slot := range slots {
		// Only the supervisor's own slots are looked at, so a lease id for
		// anyone else's is never read.
		for _, e := range w.workerEvents(ctx, slot, leases[slot]) {
			e.SlotID = slot
			events = append(events, e)
		}
	}
	return events
}

func (w *Waiter) pollWith(ctx context.Context, agentID uuid.UUID, req *WorkerPollRequest, look func(context.Context) []*Wake) (*WorkerPoll, error) {
	// Before the poll is recorded: an archived tenant's agent leaves no trace
	// of being polled.
	if w.archived(ctx, agentID) {
		return &WorkerPoll{Events: []*Wake{{Reason: ReasonDisabled}}}, nil
	}
	if w.deps.Polls != nil {
		if err := w.deps.Polls.RecordPoll(ctx, agentID, req.Version, req.Protocol); err != nil {
			// Detection is the only reader; a poll that cannot be recorded is
			// still a poll worth answering.
			w.logger.Warn("worker poll: record the poll", "agent_id", agentID, "err", err)
		}
	}
	if w.unsupported(req.Protocol) {
		return &WorkerPoll{Events: []*Wake{{Reason: ReasonProtocolUnsupported, Message: unsupportedMessage(req.Protocol)}}}, nil
	}
	if w.stale(req.Version) {
		return &WorkerPoll{Events: []*Wake{{Reason: ReasonRestart}}}, nil
	}

	pollCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	defer w.polls.begin(agentID, cancel)()

	out := &WorkerPoll{}
	outcome, err := Poll(pollCtx, w.clock, w.poll, min(max(req.Wait, 0), w.workerTimeout), w.closed,
		func(ctx context.Context) (bool, error) {
			if w.archived(ctx, agentID) {
				out.Events = []*Wake{{Reason: ReasonDisabled}}
				return true, nil
			}
			out.Events = look(ctx)
			return len(out.Events) > 0, nil
		})
	if err != nil {
		if ctx.Err() == nil && errors.Is(context.Cause(pollCtx), errSuperseded) {
			// The supervisor's newer poll is the one to answer.
			return &WorkerPoll{}, nil
		}
		return nil, err
	}
	if outcome == Closed {
		// Shutdown: what was found may never reach the supervisor, and is
		// still there to be found by its next poll.
		out.Events = nil
	}
	return out, nil
}

// WorkerPollTimeout is the longest a worker poll is held open.
func (w *Waiter) WorkerPollTimeout() time.Duration { return w.workerTimeout }

// unsupported is a supervisor that says which protocol it speaks and it is
// older than the minimum. One that says nothing is not refused: it predates
// the field, and the restart check is what updates it.
func (w *Waiter) unsupported(protocol int) bool { return protocol > 0 && protocol < w.minProtocol }

func unsupportedMessage(protocol int) string {
	return fmt.Sprintf("supervisor protocol %d is no longer supported; run `slop-haus update`", protocol)
}

func (w *Waiter) stale(version string) bool {
	return w.expectedVersion != "" && version != "" && version != w.expectedVersion
}

// workerEvents is one look at what the agent has to do. A read that fails
// reports nothing, like check: the poll then ends at its timeout rather than
// waking the supervisor to call straight back.
func (w *Waiter) workerEvents(ctx context.Context, agentID uuid.UUID, watchedLease int64) []*Wake {
	held, err := w.deps.Tickets.LeaseFor(ctx, agentID)
	switch {
	case err == nil:
	case errors.Is(err, ticket.ErrNoLease):
		held = nil
	default:
		w.logger.Warn("worker poll: look up held lease", "agent_id", agentID, "err", err)
		return nil
	}

	if watchedLease != 0 {
		end, err := w.deps.Tickets.LeaseEnd(ctx, watchedLease)
		if err != nil {
			w.logger.Warn("worker poll: look up the watched lease", "agent_id", agentID, "lease_id", watchedLease, "err", err)
			return nil
		}
		if end != nil {
			if end.AgentID != uuid.Nil && end.AgentID != agentID {
				// Not this agent's lease: the id came from the client, and
				// what became of someone else's ticket is not for it to learn.
				// Told as a lease that is no longer there.
				return []*Wake{{Reason: ReasonLeaseLost, LeaseID: watchedLease}}
			}
			// Said of the lease asked about, which may no longer be the one the
			// agent holds: the id lets the supervisor tell a stale end from news.
			wake := wakeForEnd(end.TicketID, end)
			wake.LeaseID = watchedLease
			return []*Wake{wake}
		}
	}
	if held == nil {
		return w.idleEvents(ctx, agentID)
	}

	var events []*Wake
	if w.deps.Messages != nil {
		if pending, err := w.deps.Messages.Pending(ctx, agentID); err != nil {
			w.logger.Warn("worker poll: count pending messages", "agent_id", agentID, "err", err)
		} else if pending > 0 {
			events = append(events, &Wake{Reason: ReasonMessages, TicketID: held.TicketID, Messages: pending})
		}
	}
	if w.lockOffered(ctx, agentID) {
		events = append(events, &Wake{Reason: ReasonOffered, TicketID: held.TicketID})
	}
	return events
}

func (w *Waiter) idleEvents(ctx context.Context, agentID uuid.UUID) []*Wake {
	enabled, err := w.deps.Policy.TicketProcessingEnabled(ctx)
	if err != nil {
		w.logger.Warn("worker poll: check ticket processing", "agent_id", agentID, "err", err)
		return nil
	}
	if !enabled {
		return []*Wake{{Reason: ReasonDisabled}}
	}
	result, err := w.deps.Tickets.Peek(ctx, agentID, nil)
	if err != nil {
		w.logger.Warn("worker poll: probe for claimable work", "agent_id", agentID, "err", err)
		return nil
	}
	if result.Ticket != nil {
		return []*Wake{{Reason: ReasonClaimable, TicketID: result.Ticket.ID}}
	}
	return nil
}

// lockOffered reports whether the integration lock is free and the agent is
// first in the queue for it. Whether the offer has lapsed is not judged here:
// taking the lock is what decides, under the claim lock, and a lapsed offer
// is passed over there.
func (w *Waiter) lockOffered(ctx context.Context, agentID uuid.UUID) bool {
	if w.deps.LockStates == nil {
		return false
	}
	state, err := w.deps.LockStates.IntegrationLockStateFor(ctx, agentID)
	if err != nil {
		w.logger.Warn("worker poll: read the integration lock", "agent_id", agentID, "err", err)
		return false
	}
	return state.Holder == nil && len(state.Queue) > 0 && state.Queue[0].AgentID == agentID
}
