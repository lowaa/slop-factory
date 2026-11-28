package agents

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-bot/internal/config"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness"
	"github.com/lowaa/slop-factory/slop-bot/internal/ident"
	"github.com/lowaa/slop-factory/slop-bot/internal/state"
	"github.com/lowaa/slop-factory/slop-bot/internal/tickets"
)

// These test the scheduling rules over the manager's own bookkeeping, set up
// directly: the choices are made before anything touches a store or a
// process, so none is wanted.

func bareManager(maxLive int) *Manager {
	return &Manager{
		opts:     &Options{Config: &config.Config{Concurrency: config.Concurrency{MaxLive: maxLive}}},
		runners:  map[ident.AgentID]*runner{},
		starting: map[ident.TicketID]state.Pool{},
		streams:  map[ident.AgentID]int{},
	}
}

// idleConv is a conversation that exists, which is all parking asks of it.
type idleConv struct{ harness.Conversation }

func idleRunner(id ident.AgentID, since time.Time) *runner {
	return &runner{id: id, ticket: ident.TicketID("t-" + string(id)), conv: idleConv{}, st: state.StateIdle, since: since}
}

func TestOnlyPreparingAndOpenTicketsWantAnAgent(t *testing.T) {
	t.Parallel()
	m := bareManager(1)
	for _, st := range []tickets.Status{tickets.StatusInProgress, tickets.StatusDone, tickets.StatusCancelled, tickets.StatusDiscussion, tickets.StatusMisconfigured} {
		assert.False(t, m.wantsAgent(context.Background(), &tickets.Ticket{ID: "1", Status: st}), "%s", st)
	}
}

func TestAStartForATicketThatAlreadyHasItsRunnerTakesNoSecondPlace(t *testing.T) {
	t.Parallel()
	m := bareManager(2)
	r := idleRunner("a", time.Time{})
	r.st = state.StateWorking // not parkable: a miscount would find no room
	m.runners[r.id] = r
	m.starting[r.ticket] = state.PoolNormal

	ok, victim := m.roomLocked("")

	assert.True(t, ok, "the one process is counted once, so the second place is free")
	assert.Nil(t, victim)
	assert.True(t, m.hasRunnerLocked(r.ticket))
	assert.False(t, m.hasRunnerLocked("other"))
}

func TestAnIdleTieGoesToTheLowerAgentId(t *testing.T) {
	t.Parallel()
	m := bareManager(1)
	since := time.Date(2026, 11, 28, 9, 0, 0, 0, time.UTC)
	// Enough of them that map order alone would pick the wrong one.
	for i := 9; i >= 0; i-- {
		r := idleRunner(ident.AgentID(fmt.Sprintf("agent-%d", i)), since)
		m.runners[r.id] = r
	}

	victim := m.victimLocked("")

	require.NotNil(t, victim)
	assert.Equal(t, ident.AgentID("agent-0"), victim.id)
}

func TestNothingStartsOnceTheManagerIsStopping(t *testing.T) {
	t.Parallel()
	m := bareManager(1)
	m.stopping = true
	var ran atomic.Bool

	started := m.goStart("1", state.PoolNormal, false, func(context.Context) { ran.Store(true) })

	assert.False(t, started)
	assert.Empty(t, m.starting, "no place was taken")
	m.starts.Wait()
	assert.False(t, ran.Load())
}

// lastingConv is a session that outlives the daemon, and counts being let go.
type lastingConv struct {
	idleConv
	closed atomic.Int32
}

func (l *lastingConv) Close() error             { l.closed.Add(1); return nil }
func (l *lastingConv) Held() []*harness.Message { return nil }

// A session whose process already exited has nothing to let go of: the
// daemon's stop leaves it be rather than closing it again and queueing what
// it held.
func TestStoppingAnAlreadyExitedLastingSessionLetsGoOfNothing(t *testing.T) {
	t.Parallel()
	conv := &lastingConv{}
	r := idleRunner("a", time.Time{})
	r.conv = conv
	r.exited = make(chan struct{})
	close(r.exited)

	require.NoError(t, r.shutdown(context.Background()))

	assert.Zero(t, conv.closed.Load())
	assert.True(t, r.stopping)
}
