package wakeup_test

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/tenantstate"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/wakeup"
)

// state is what the fakes answer. One mutex for all of them: the test
// changes it while a wait is reading it from another goroutine.
type state struct {
	mu       sync.Mutex
	lease    *ticket.Lease
	leaseErr error
	peek     *ticket.ClaimResult
	peekErr  error
	peeks    int
	// leaseFor is the agent each LeaseFor was for.
	leaseFor []uuid.UUID
	// peeked is the agent each Peek was for.
	peeked    []uuid.UUID
	pending   int
	pendErr   error
	disabled  bool
	policyErr error
	// leaseEnd is what LeaseEnd answers: nil is "still held".
	leaseEnd    *ticket.LeaseEnd
	leaseEndErr error
	leaseEnds   int
	// leaseEndsFor, when set, answers LeaseEnd by lease id instead of
	// leaseEnd, and leaseIDsAsked records each id it was asked about.
	leaseEndsFor  map[int64]*ticket.LeaseEnd
	leaseIDsAsked []int64
	// lockState is what IntegrationLockState answers; nil is a free lock
	// with nobody queued.
	lockState *ticket.LockState
	// looked, when set, is sent to (without blocking) on each LeaseFor: a
	// test's way to know a poll has begun its check.
	looked chan struct{}
	// leaseEndHook, when set, runs at the start of each LeaseEnd, outside
	// the lock, with the number of the call: a test's way to hold a check
	// part-way through while it does something else.
	leaseEndHook func(call int)
}

func (s *state) set(f func(*state)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f(s)
}

func (s *state) LeaseFor(_ context.Context, agentID uuid.UUID) (*ticket.Lease, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.leaseFor = append(s.leaseFor, agentID)
	if s.looked != nil {
		select {
		case s.looked <- struct{}{}:
		default:
		}
	}
	if s.leaseErr != nil {
		return nil, s.leaseErr
	}
	if s.lease == nil {
		return nil, ticket.ErrNoLease
	}
	return s.lease, nil
}

func (s *state) LeaseEnd(_ context.Context, leaseID int64) (*ticket.LeaseEnd, error) {
	s.mu.Lock()
	hook, call := s.leaseEndHook, s.leaseEnds+1
	s.mu.Unlock()
	if hook != nil {
		hook(call)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.leaseEnds++
	s.leaseIDsAsked = append(s.leaseIDsAsked, leaseID)
	if s.leaseEndsFor != nil {
		return s.leaseEndsFor[leaseID], s.leaseEndErr
	}
	return s.leaseEnd, s.leaseEndErr
}

func (s *state) Peek(_ context.Context, agentID uuid.UUID, _ *int64) (*ticket.ClaimResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.peeks++
	s.peeked = append(s.peeked, agentID)
	if s.peekErr != nil {
		return nil, s.peekErr
	}
	if s.peek == nil {
		return &ticket.ClaimResult{}, nil
	}
	return s.peek, nil
}

func (s *state) Pending(context.Context, uuid.UUID) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pending, s.pendErr
}

func (s *state) TicketProcessingEnabled(context.Context) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.disabled, s.policyErr
}

func (s *state) read(f func(*state) int) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return f(s)
}

// manualClock is driven by the test. Both channels are unbuffered, so a
// send returns only once the wait is parked in its select -- that is, once
// the check before it has finished -- which is what lets a test say "the
// wait looked, found nothing, and is still waiting" without a sleep.
type manualClock struct {
	ticks    chan time.Time
	deadline chan time.Time

	// What the waits asked for; several may run at once.
	mu       sync.Mutex
	interval time.Duration
	timeout  time.Duration
}

func newManualClock() *manualClock {
	return &manualClock{ticks: make(chan time.Time), deadline: make(chan time.Time)}
}

func (c *manualClock) Ticker(d time.Duration) (<-chan time.Time, func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.interval = d
	return c.ticks, func() {}
}

func (c *manualClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.timeout = d
	return c.deadline
}

type rig struct {
	state  *state
	clock  *manualClock
	waiter *wakeup.Waiter
	agent  uuid.UUID
	// What the waits are recorded in, which is real: one open wait per
	// agent is the database's rule.
	db     *db.DB
	tenant uuid.UUID
	opts   wakeup.Options
}

func newRig(t *testing.T, opts *wakeup.Options) *rig {
	t.Helper()
	database := dbtest.WithDB(t)
	r := &rig{db: database, tenant: dbtest.NewTenant(t, database)}
	if opts != nil {
		r.opts = *opts
	}
	r.agent = r.newAgent(t)
	r.setUpProcess()
	return r
}

// setUpProcess gives the rig a waiter of its own, over the same database.
func (r *rig) setUpProcess() {
	r.state = &state{}
	r.clock = newManualClock()
	o := r.opts
	o.Clock = r.clock
	r.waiter = wakeup.New(wakeup.Deps{
		Tickets: r.state, Messages: r.state, Policy: r.state,
		Archive: tenantstate.NewChecker(r.db),
	}, &o, slog.DiscardHandler)
}

func (r *rig) newAgent(t *testing.T) uuid.UUID {
	t.Helper()
	id := uuid.New()
	_, err := r.db.Exec(db.WithTenant(context.Background(), r.tenant),
		`INSERT INTO agents (id, name) VALUES ($1, $2)`, id, "agent-"+id.String())
	require.NoError(t, err)
	_, err = r.db.Exec(db.WithTenant(context.Background(), r.tenant),
		`INSERT INTO sessions (agent_id, claude_session_id, started_at) VALUES ($1, 'c', now() - interval '1 hour')`, id)
	require.NoError(t, err)
	return id
}

func (r *rig) ctx(ctx context.Context) context.Context { return db.WithTenant(ctx, r.tenant) }

// tick lets one poll interval pass. It returns once the wait has received
// it, which proves the check before it finished and found nothing; the
// check it starts is proven finished only by the next tick.
func (r *rig) tick(t *testing.T) {
	t.Helper()
	select {
	case r.clock.ticks <- time.Now():
	case <-time.After(5 * time.Second):
		t.Fatal("the wait never came back for the next tick; it has returned or hung")
	}
}

func claimable(id int64) *ticket.ClaimResult {
	return &ticket.ClaimResult{Ticket: &ticket.Ticket{ID: id}}
}
