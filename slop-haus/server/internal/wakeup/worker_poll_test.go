package wakeup_test

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/tenantstate"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/wakeup"
)

func (s *state) IntegrationLockStateFor(context.Context, uuid.UUID) (*ticket.LockState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lockState == nil {
		return &ticket.LockState{}, nil
	}
	return s.lockState, nil
}

// workerWaiter is a waiter over the rig's fakes that also records polls in
// the real database.
func (r *rig) workerWaiter(opts *wakeup.Options) *wakeup.Waiter {
	o := r.opts
	if opts != nil {
		o.SupervisorVersion, o.WorkerPollTimeout = opts.SupervisorVersion, opts.WorkerPollTimeout
		o.MinSupervisorProtocol = opts.MinSupervisorProtocol
	}
	o.Clock = r.clock
	return wakeup.New(wakeup.Deps{
		Tickets: r.state, Messages: r.state, Policy: r.state,
		LockStates: r.state, Polls: wakeup.NewPostgresStore(r.db),
		Archive: tenantstate.NewChecker(r.db),
	}, &o, slog.DiscardHandler)
}

func (r *rig) poll(t *testing.T, w *wakeup.Waiter, req *wakeup.WorkerPollRequest) *wakeup.WorkerPoll {
	t.Helper()
	got, err := w.WorkerPoll(r.ctx(context.Background()), r.agent, req)
	require.NoError(t, err)
	return got
}

func reasons(p *wakeup.WorkerPoll) []wakeup.Reason {
	var out []wakeup.Reason
	for _, e := range p.Events {
		out = append(out, e.Reason)
	}
	return out
}

func held(ticketID int64) *ticket.Lease { return &ticket.Lease{ID: 7, TicketID: ticketID} }

func TestWorkerPoll_IdleAgentIsToldOfClaimableWorkAtOnce(t *testing.T) {
	r := newRig(t, nil)
	r.state.peek = claimable(38)

	got := r.poll(t, r.workerWaiter(nil), &wakeup.WorkerPollRequest{Wait: time.Minute})

	assert.Equal(t, []*wakeup.Wake{{Reason: wakeup.ReasonClaimable, TicketID: 38}}, got.Events)
}

func TestWorkerPoll_IdleAgentWithNothingTimesOutWithNoEvents(t *testing.T) {
	r := newRig(t, nil)
	w := r.workerWaiter(&wakeup.Options{WorkerPollTimeout: 20 * time.Second})
	done := make(chan *wakeup.WorkerPoll, 1)
	go func() { done <- r.poll(t, w, &wakeup.WorkerPollRequest{Wait: time.Hour}) }()

	r.clock.deadline <- time.Now()

	got := <-done
	assert.Empty(t, got.Events)
	r.clock.mu.Lock()
	defer r.clock.mu.Unlock()
	assert.Equal(t, 20*time.Second, r.clock.timeout, "a caller cannot hold a poll longer than the cap")
}

func TestWorkerPoll_WakesWhenWorkBecomesClaimable(t *testing.T) {
	r := newRig(t, nil)
	w := r.workerWaiter(nil)
	done := make(chan *wakeup.WorkerPoll, 1)
	go func() { done <- r.poll(t, w, &wakeup.WorkerPollRequest{Wait: time.Minute}) }()
	r.tick(t)
	r.tick(t)

	r.state.set(func(s *state) { s.peek = claimable(5) })

	for {
		select {
		case got := <-done:
			assert.Equal(t, []wakeup.Reason{wakeup.ReasonClaimable}, reasons(got))
			return
		case r.clock.ticks <- time.Now():
		case <-time.After(5 * time.Second):
			t.Fatal("the poll never woke")
		}
	}
}

func TestWorkerPoll_ProcessingSwitchedOffIsReportedToAnIdleAgent(t *testing.T) {
	r := newRig(t, nil)
	r.state.disabled = true

	got := r.poll(t, r.workerWaiter(nil), &wakeup.WorkerPollRequest{})

	assert.Equal(t, []wakeup.Reason{wakeup.ReasonDisabled}, reasons(got))
}

// Nothing over REST takes a pending message, so telling an idle agent of one
// would end every poll at once, for as long as it stayed pending.
func TestWorkerPoll_MessagesAreNotReportedToAnIdleAgent(t *testing.T) {
	r := newRig(t, nil)
	r.state.pending = 2

	got := r.poll(t, r.workerWaiter(nil), &wakeup.WorkerPollRequest{})

	assert.Empty(t, got.Events)
}

func TestWorkerPoll_AHolderIsToldOfMessages(t *testing.T) {
	r := newRig(t, nil)
	r.state.lease, r.state.pending = held(12), 2

	got := r.poll(t, r.workerWaiter(nil), &wakeup.WorkerPollRequest{LeaseID: 7})

	assert.Equal(t, []*wakeup.Wake{{Reason: wakeup.ReasonMessages, TicketID: 12, Messages: 2}}, got.Events)
}

// A holder is not offered more work: claimable tickets are for an idle agent.
func TestWorkerPoll_AHolderIsNotToldOfClaimableWork(t *testing.T) {
	r := newRig(t, nil)
	r.state.lease, r.state.peek = held(12), claimable(40)

	got := r.poll(t, r.workerWaiter(nil), &wakeup.WorkerPollRequest{LeaseID: 7, Wait: 0})

	assert.Empty(t, got.Events)
	assert.Zero(t, r.state.read(func(s *state) int { return s.peeks }))
}

func TestWorkerPoll_TheWatchedLeaseEndingIsReportedByHowItEnded(t *testing.T) {
	for name, tc := range map[string]struct {
		end  *ticket.LeaseEnd
		want *wakeup.Wake
	}{
		"revoked": {
			end:  &ticket.LeaseEnd{TicketID: 12, Kind: ticket.LeaseRevoked, Status: ticket.StatusOpen},
			want: &wakeup.Wake{Reason: wakeup.ReasonRevoked, TicketID: 12, LeaseID: 7, Ended: "open"},
		},
		"finished": {
			end:  &ticket.LeaseEnd{TicketID: 12, Kind: ticket.LeaseFinished, Exit: ticket.ExitReview},
			want: &wakeup.Wake{Reason: wakeup.ReasonFinished, TicketID: 12, LeaseID: 7, Ended: "review"},
		},
		"lapsed": {
			end:  &ticket.LeaseEnd{TicketID: 12, Kind: ticket.LeaseLapsed},
			want: &wakeup.Wake{Reason: wakeup.ReasonLeaseLost, TicketID: 12, LeaseID: 7},
		},
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t, nil)
			// The agent holds nothing now, and work is waiting: the end of the
			// lease it was working is still the news.
			r.state.leaseEnd, r.state.peek = tc.end, claimable(40)

			got := r.poll(t, r.workerWaiter(nil), &wakeup.WorkerPollRequest{LeaseID: 7})

			assert.Equal(t, []*wakeup.Wake{tc.want}, got.Events)
			assert.Equal(t, []int64{7}, r.state.leaseIDsAsked)
		})
	}
}

func TestWorkerPoll_WithoutALeaseToWatchAnEndIsNotLookedFor(t *testing.T) {
	r := newRig(t, nil)
	r.state.lease = held(12)

	r.poll(t, r.workerWaiter(nil), &wakeup.WorkerPollRequest{})

	assert.Empty(t, r.state.leaseIDsAsked)
}

func TestWorkerPoll_LockOfferedOnlyToTheHeadOfTheQueueWhenTheLockIsFree(t *testing.T) {
	other := uuid.New()
	for name, tc := range map[string]struct {
		state func(me uuid.UUID) *ticket.LockState
		want  []wakeup.Reason
	}{
		"free and first": {
			state: func(me uuid.UUID) *ticket.LockState {
				return &ticket.LockState{Queue: []ticket.LockQueueEntry{{AgentID: me}, {AgentID: other}}}
			},
			want: []wakeup.Reason{wakeup.ReasonOffered},
		},
		"free but second": {
			state: func(me uuid.UUID) *ticket.LockState {
				return &ticket.LockState{Queue: []ticket.LockQueueEntry{{AgentID: other}, {AgentID: me}}}
			},
		},
		"first but held by another": {
			state: func(me uuid.UUID) *ticket.LockState {
				return &ticket.LockState{Holder: &ticket.LockHolder{AgentID: other}, Queue: []ticket.LockQueueEntry{{AgentID: me}}}
			},
		},
		"not queued": {
			state: func(uuid.UUID) *ticket.LockState { return &ticket.LockState{} },
		},
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t, nil)
			r.state.lease, r.state.lockState = held(12), tc.state(r.agent)

			got := r.poll(t, r.workerWaiter(nil), &wakeup.WorkerPollRequest{LeaseID: 7})

			assert.Equal(t, tc.want, reasons(got))
		})
	}
}

func TestWorkerPoll_ASupervisorOnAnotherVersionIsToldToRestartWithoutProbing(t *testing.T) {
	r := newRig(t, nil)
	r.state.peek = claimable(38)

	got := r.poll(t, r.workerWaiter(&wakeup.Options{SupervisorVersion: "2"}), &wakeup.WorkerPollRequest{Version: "1"})

	assert.Equal(t, []*wakeup.Wake{{Reason: wakeup.ReasonRestart}}, got.Events)
	assert.Zero(t, r.state.read(func(s *state) int { return s.peeks }), "it is going away: no point handing it work")
}

func TestWorkerPoll_TheExpectedVersionOrNoneOrNoExpectationIsNotStale(t *testing.T) {
	for name, tc := range map[string]struct{ expected, sent string }{
		"the same version":    {expected: "2", sent: "2"},
		"a client with none":  {expected: "2", sent: ""},
		"nothing is expected": {expected: "", sent: "1"},
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t, nil)
			r.state.peek = claimable(38)

			got := r.poll(t, r.workerWaiter(&wakeup.Options{SupervisorVersion: tc.expected}), &wakeup.WorkerPollRequest{Version: tc.sent})

			assert.Equal(t, []wakeup.Reason{wakeup.ReasonClaimable}, reasons(got))
		})
	}
}

func TestWorkerPoll_RecordsEachPollAsTheAgentsLiveness(t *testing.T) {
	r := newRig(t, nil)
	w := r.workerWaiter(nil)
	read := func() (version string, at time.Time) {
		require.NoError(t, r.db.QueryRow(r.ctx(context.Background()),
			`SELECT version, last_polled_at FROM worker_polls WHERE agent_id = $1`, r.agent).Scan(&version, &at))
		return version, at
	}

	r.poll(t, w, &wakeup.WorkerPollRequest{Version: "1"})
	version, first := read()
	assert.Equal(t, "1", version)

	r.poll(t, w, &wakeup.WorkerPollRequest{Version: "2"})
	version, second := read()
	assert.Equal(t, "2", version, "one row per agent, overwritten")
	assert.True(t, second.After(first))
}

// Today's minimum is today's protocol, so nothing is refused yet; the refusal
// is exercised by raising the minimum.
func TestWorkerPoll_ATooOldProtocolIsToldSoReadablyAndNothingElse(t *testing.T) {
	r := newRig(t, nil)
	r.state.peek = claimable(38)

	got := r.poll(t, r.workerWaiter(&wakeup.Options{MinSupervisorProtocol: 3}), &wakeup.WorkerPollRequest{Protocol: 2})

	require.Len(t, got.Events, 1)
	assert.Equal(t, wakeup.ReasonProtocolUnsupported, got.Events[0].Reason)
	assert.Equal(t, "supervisor protocol 2 is no longer supported; run `slop-haus update`", got.Events[0].Message)
	assert.Zero(t, r.state.read(func(s *state) int { return s.peeks }), "it cannot be handed work")
}

func TestWorkerPoll_TheMinimumProtocolOrNoneIsNotRefused(t *testing.T) {
	for name, protocol := range map[string]int{"the minimum": 3, "newer": 4, "none (older than the field)": 0} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t, nil)
			r.state.peek = claimable(38)

			got := r.poll(t, r.workerWaiter(&wakeup.Options{MinSupervisorProtocol: 3}), &wakeup.WorkerPollRequest{Protocol: protocol})

			assert.Equal(t, []wakeup.Reason{wakeup.ReasonClaimable}, reasons(got))
		})
	}
}

func TestWorkerPoll_TodaysServerRefusesNoProtocol(t *testing.T) {
	r := newRig(t, nil)
	r.state.peek = claimable(38)

	got := r.poll(t, r.workerWaiter(nil), &wakeup.WorkerPollRequest{Protocol: 1})

	assert.Equal(t, []wakeup.Reason{wakeup.ReasonClaimable}, reasons(got))
}

func TestWorkerPoll_RecordsTheProtocolEvenOfAnUnsupportedSupervisor(t *testing.T) {
	r := newRig(t, nil)
	w := r.workerWaiter(&wakeup.Options{MinSupervisorProtocol: 3})
	read := func() (protocol int) {
		require.NoError(t, r.db.QueryRow(r.ctx(context.Background()),
			`SELECT protocol FROM worker_polls WHERE agent_id = $1`, r.agent).Scan(&protocol))
		return protocol
	}

	r.poll(t, w, &wakeup.WorkerPollRequest{Protocol: 2})
	assert.Equal(t, 2, read(), "the board can then say which supervisors are behind")

	r.poll(t, w, &wakeup.WorkerPollRequest{})
	assert.Equal(t, 0, read(), "a poll that says none overwrites: the row is the last poll")
}

func TestWorkerPoll_AStaleSupervisorIsStillRecordedAsAlive(t *testing.T) {
	r := newRig(t, nil)

	r.poll(t, r.workerWaiter(&wakeup.Options{SupervisorVersion: "2"}), &wakeup.WorkerPollRequest{Version: "1"})

	var n int
	require.NoError(t, r.db.QueryRow(r.ctx(context.Background()), `SELECT count(*) FROM worker_polls WHERE agent_id = $1`, r.agent).Scan(&n))
	assert.Equal(t, 1, n)
}

// What a poll found as the server closed may never reach the supervisor, so
// it is left to be found by the next one.
func TestWorkerPoll_ShutdownAnswersWithNoEvents(t *testing.T) {
	r := newRig(t, nil)
	r.state.peek = claimable(38)
	w := r.workerWaiter(nil)
	w.Close()

	got := r.poll(t, w, &wakeup.WorkerPollRequest{Wait: time.Minute})

	assert.Empty(t, got.Events)
}

func TestWorkerPoll_AFailedReadReportsNothing(t *testing.T) {
	r := newRig(t, nil)
	r.state.peekErr = context.DeadlineExceeded

	got := r.poll(t, r.workerWaiter(nil), &wakeup.WorkerPollRequest{})

	assert.Empty(t, got.Events)
}

// The lease id comes from the client. What became of another agent's ticket
// is not for it to learn by guessing ids.
func TestWorkerPoll_ALeaseOfAnotherAgentIsToldOnlyAsLost(t *testing.T) {
	r := newRig(t, nil)
	r.state.leaseEnd = &ticket.LeaseEnd{TicketID: 99, AgentID: uuid.New(), Kind: ticket.LeaseFinished, Exit: ticket.ExitReview}

	got := r.poll(t, r.workerWaiter(nil), &wakeup.WorkerPollRequest{LeaseID: 7})

	assert.Equal(t, []*wakeup.Wake{{Reason: wakeup.ReasonLeaseLost, LeaseID: 7}}, got.Events)
}

func TestWorkerPoll_TheAgentsOwnLeaseEndIsToldInFull(t *testing.T) {
	r := newRig(t, nil)
	r.state.leaseEnd = &ticket.LeaseEnd{TicketID: 99, AgentID: r.agent, Kind: ticket.LeaseFinished, Exit: ticket.ExitReview}

	got := r.poll(t, r.workerWaiter(nil), &wakeup.WorkerPollRequest{LeaseID: 7})

	assert.Equal(t, []*wakeup.Wake{{Reason: wakeup.ReasonFinished, TicketID: 99, LeaseID: 7, Ended: "review"}}, got.Events)
}

// An end is told of the lease asked about, which can be an older one than the
// agent holds now (its supervisor has moved on to a newer claim): the id is
// how the supervisor tells a stale end from news.
func TestWorkerPoll_AnEndNamesTheLeaseItIsOfNotTheOneHeldNow(t *testing.T) {
	r := newRig(t, nil)
	r.state.lease = &ticket.Lease{ID: 9, TicketID: 30}
	r.state.leaseEnd = &ticket.LeaseEnd{TicketID: 12, Kind: ticket.LeaseFinished, Exit: ticket.ExitDone}

	got := r.poll(t, r.workerWaiter(nil), &wakeup.WorkerPollRequest{LeaseID: 7})

	assert.Equal(t, []*wakeup.Wake{{Reason: wakeup.ReasonFinished, TicketID: 12, LeaseID: 7, Ended: "done"}}, got.Events)
}

func (r *rig) pollInBackground(t *testing.T, w *wakeup.Waiter, agentID uuid.UUID) <-chan *wakeup.WorkerPoll {
	t.Helper()
	done := make(chan *wakeup.WorkerPoll, 1)
	go func() {
		got, err := w.WorkerPoll(r.ctx(context.Background()), agentID, &wakeup.WorkerPollRequest{Wait: time.Hour})
		assert.NoError(t, err)
		done <- got
	}()
	return done
}

func awaitPoll(t *testing.T, done <-chan *wakeup.WorkerPoll) *wakeup.WorkerPoll {
	t.Helper()
	select {
	case got := <-done:
		return got
	case <-time.After(5 * time.Second):
		t.Fatal("the poll did not return")
		return nil
	}
}

// A supervisor that restarts, or retries a poll it gave up on, must not leave
// the old one probing the database beside the new one.
func TestWorkerPoll_ANewPollOfTheSameAgentEndsTheOldOneWithNoEvents(t *testing.T) {
	r := newRig(t, nil)
	w := r.workerWaiter(nil)
	r.state.looked = make(chan struct{}, 1)
	older := r.pollInBackground(t, w, r.agent)
	<-r.state.looked

	newer := r.pollInBackground(t, w, r.agent)

	assert.Empty(t, awaitPoll(t, older).Events)
	<-r.state.looked
	r.clock.deadline <- time.Now() // the newer poll runs out in its turn
	assert.Empty(t, awaitPoll(t, newer).Events)
}

func TestWorkerPoll_APollOfAnotherAgentIsLeftOpen(t *testing.T) {
	r := newRig(t, nil)
	w := r.workerWaiter(nil)
	r.state.looked = make(chan struct{}, 1)
	first := r.pollInBackground(t, w, r.agent)
	<-r.state.looked
	second := r.pollInBackground(t, w, r.newAgent(t))
	<-r.state.looked // the second has registered and checked: the first has had its chance to be ended

	r.state.set(func(s *state) { s.peek = claimable(5) })

	for _, done := range []<-chan *wakeup.WorkerPoll{first, second} {
		for got := (*wakeup.WorkerPoll)(nil); got == nil; {
			select {
			case got = <-done:
				assert.Equal(t, []wakeup.Reason{wakeup.ReasonClaimable}, reasons(got))
			case r.clock.ticks <- time.Now():
			case <-time.After(5 * time.Second):
				t.Fatal("the poll never woke")
			}
		}
	}
}
