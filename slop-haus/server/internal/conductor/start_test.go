package conductor_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/agent"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/conductor"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
)

// A start whose reply was lost is sent again with the same client id. It must
// find the conversation it made, not start another and not be refused as busy.
func TestAStartRepeatedWithTheSameClientIDReturnsTheConversationItMade(t *testing.T) {
	f := newFixture(t)
	owned := f.owned(t, f.owner, "c1")
	id := uuid.New()
	opening := "hello"
	req := &conductor.StartRequest{Message: &opening, ClientID: &id}

	first, err := f.svc.Start(f.ctx, owned, req)
	require.NoError(t, err)
	again, err := f.svc.Start(f.ctx, owned, req)
	require.NoError(t, err)
	_, err = f.svc.Disconnect(f.ctx, owned, first.ID)
	require.NoError(t, err)
	afterStop, err := f.svc.Start(f.ctx, owned, req)
	require.NoError(t, err)

	assert.Equal(t, first.ID, again.ID)
	assert.Equal(t, first.ID, afterStop.ID, "the original, not a new conversation, even though the conductor is free again")
	res := f.poll(t, f.svc, f.host(t, owned), 0)
	assert.Equal(t, []conductor.WorkKind{conductor.WorkStart, conductor.WorkMessage, conductor.WorkStop}, kinds(res.Items), "queued once")
	list, err := f.svc.List(f.ctx, owned, 0)
	require.NoError(t, err)
	assert.Len(t, list, 1)
}

func TestConcurrentStartsWithTheSameClientIDAllGetTheSameConversation(t *testing.T) {
	f := newFixture(t)
	owned := f.owned(t, f.owner, "c1")
	id := uuid.New()
	const racers = 6
	var (
		wg      sync.WaitGroup
		release = make(chan struct{})
		mu      sync.Mutex
		got     []*conductor.Conversation
	)
	for range racers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-release
			c, err := f.svc.Start(f.ctx, owned, &conductor.StartRequest{ClientID: &id})
			assert.NoError(t, err)
			mu.Lock()
			defer mu.Unlock()
			got = append(got, c)
		}()
	}
	close(release)
	wg.Wait()

	require.Len(t, got, racers)
	for _, c := range got {
		require.NotNil(t, c)
		assert.Equal(t, got[0].ID, c.ID)
	}
	assert.Equal(t, []conductor.WorkKind{conductor.WorkStart}, kinds(f.poll(t, f.svc, f.host(t, owned), 0).Items))
}

func TestAClientIDBelongsToItsConductor(t *testing.T) {
	f := newFixture(t)
	mine, theirs := f.owned(t, f.owner, "mine"), f.owned(t, f.other, "theirs")
	id := uuid.New()

	a, err := f.svc.Start(f.ctx, mine, &conductor.StartRequest{ClientID: &id})
	require.NoError(t, err)
	b, err := f.svc.Start(f.ctx, theirs, &conductor.StartRequest{ClientID: &id})
	require.NoError(t, err)

	assert.NotEqual(t, a.ID, b.ID, "another conductor's start is never answered with this one's conversation")
	assert.Equal(t, theirs.Agent().ID, b.ConductorID)
}

// AuthorizeOwner is checked before the write, so a disable that lands between
// the two must still stop the start: the write checks again, under a lock the
// disable waits for.
func TestAStartWithAProofThatWentStaleIsRefused(t *testing.T) {
	f := newFixture(t)
	owned := f.owned(t, f.owner, "c1") // proven while enabled
	_, err := f.conductors.SetDisabled(f.ctx, f.admin, owned.Agent().ID, true)
	require.NoError(t, err)

	_, err = f.svc.Start(f.ctx, owned, &conductor.StartRequest{})

	assert.ErrorIs(t, err, agent.ErrIsDisabled)
	list, err := f.svc.List(f.ctx, owned, 0)
	require.NoError(t, err)
	assert.Empty(t, list)
}

// A message reconnects a conversation with no process, which is a write that
// must hold the same line as a start against a disable.
func TestAReconnectWithAProofThatWentStaleIsRefused(t *testing.T) {
	f := newFixture(t)
	owned := f.owned(t, f.owner, "c1")
	c := f.start(t, owned)
	_, err := f.svc.Disconnect(f.ctx, owned, c.ID)
	require.NoError(t, err)
	_, err = f.conductors.SetDisabled(f.ctx, f.admin, owned.Agent().ID, true)
	require.NoError(t, err)

	_, err = f.svc.Send(f.ctx, owned, c.ID, &conductor.SendRequest{Text: "again"})

	assert.ErrorIs(t, err, agent.ErrIsDisabled)
	got, err := f.svc.Get(f.ctx, owned, c.ID)
	require.NoError(t, err)
	assert.Equal(t, conductor.StateStopped, got.State)
}

// The race itself, not only its two ends. A start is held inside its
// transaction -- after it has taken the conductor's row, before it has made the
// conversation -- by a transaction of the test's that already holds a live
// conversation's place; the disable is then attempted. It has to wait for the
// start rather than overtake it, and when the start completes the disable ends
// the conversation it made: nothing is left live on a conductor that is off.
func TestADisableRacingAStartWaitsForItAndThenEndsWhatItMade(t *testing.T) {
	f := newFixture(t)
	owned := f.owned(t, f.owner, "c1")
	holding, release := make(chan struct{}), make(chan struct{})
	heldDone := make(chan struct{})
	go func() {
		defer close(heldDone)
		_ = f.database.InTx(f.ctx, func(q db.Querier) error {
			_, err := q.Exec(f.ctx, `
				INSERT INTO conductor_conversations (id, conductor_id, owner_user_id, state)
				VALUES ($1, $2, $3, 'starting')`, uuid.New(), owned.Agent().ID, f.owner.ID)
			close(holding)
			<-release
			if err != nil {
				return err
			}
			return errors.New("roll back: the place is free again")
		})
	}()
	<-holding

	started := make(chan error, 1)
	go func() {
		_, err := f.svc.Start(f.ctx, owned, &conductor.StartRequest{})
		started <- err
	}()
	// The start is inside its transaction, waiting for the live conversation's place.
	f.awaitBlocked(t, "INSERT INTO conductor_conversations")

	disabled := make(chan error, 1)
	go func() {
		_, err := f.conductors.SetDisabled(f.ctx, f.admin, owned.Agent().ID, true)
		disabled <- err
	}()
	// The disable is waiting on the start's hold of the conductor's row.
	f.awaitBlocked(t, "disable_agent")
	select {
	case <-disabled:
		t.Fatal("the disable overtook a start in progress")
	default:
	}

	close(release)
	require.NoError(t, <-started, "the start that began first completes")
	require.NoError(t, <-disabled)
	<-heldDone
	list, err := f.svc.List(f.ctx, owned, 0)
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Equal(t, conductor.StateStopped, list[0].State, "and the disable ended it")
}

// Whatever the host had not acknowledged is delivered after a disable and a
// re-enable, so the stop must be queued behind it: a host that replays the
// start it never handled ends it, rather than running a conversation that
// was ended while it was away.
func TestDisablingQueuesAStopBehindWhatTheHostHadNotHandled(t *testing.T) {
	f := newFixture(t)
	owned := f.owned(t, f.owner, "c1")
	c := f.start(t, owned)

	_, err := f.conductors.SetDisabled(f.ctx, f.admin, owned.Agent().ID, true)
	require.NoError(t, err)

	res := f.poll(t, f.svc, f.host(t, owned), 0)
	require.Equal(t, []conductor.WorkKind{conductor.WorkStart, conductor.WorkStop}, kinds(res.Items))
	assert.Equal(t, c.ID, res.Items[1].Conversation)
}

func TestWorkCannotNameAnotherConductorsConversationOrAMissingEvent(t *testing.T) {
	f := newFixture(t)
	mine, theirs := f.owned(t, f.owner, "mine"), f.owned(t, f.other, "theirs")
	conv := f.start(t, mine)
	f.start(t, theirs) // gives the other conductor a queue

	// What a bug in a writer would do, which the schema must refuse.
	insert := func(ctx context.Context, conductorID uuid.UUID, kind string, messageSeq any) error {
		_, err := f.database.Exec(ctx, `
			INSERT INTO conductor_work (conductor_id, seq, kind, conversation_id, message_seq)
			VALUES ($1, 99, $2, $3, $4)`, conductorID, kind, conv.ID, messageSeq)
		return err
	}
	assert.Error(t, insert(db.WithTenant(context.Background(), f.tenant), theirs.Agent().ID, "stop", nil),
		"a conversation of another conductor")
	assert.Error(t, insert(db.WithTenant(context.Background(), f.tenant), mine.Agent().ID, "message", 42),
		"a message whose transcript event does not exist")
	assert.NoError(t, insert(db.WithTenant(context.Background(), f.tenant), mine.Agent().ID, "stop", nil),
		"the same insert for its own conversation is fine")
}

func TestAReportFromAHostThatWasTakenOverIsRefused(t *testing.T) {
	f := newFixture(t)
	owned := f.owned(t, f.owner, "c1")
	c := f.start(t, owned)
	old := f.polling(t, owned)
	f.report(t, old, c, text("before", 1))
	impatient := f.newService(&conductor.Options{HostTTL: time.Nanosecond})
	t.Cleanup(impatient.Close)
	successor := f.host(t, owned)
	_, err := impatient.Poll(f.ctx, successor.proof, &conductor.PollRequest{HostID: successor.id})
	require.NoError(t, err)

	_, err = f.svc.Report(f.ctx, old.proof, c.ID, &conductor.ReportRequest{HostID: old.id, Events: []conductor.HostEvent{text("stale", 2)}})

	assert.ErrorIs(t, err, conductor.ErrHostBusy)
	acked, err := f.svc.Report(f.ctx, successor.proof, c.ID, &conductor.ReportRequest{HostID: successor.id, Events: []conductor.HostEvent{text("fresh", 2)}})
	require.NoError(t, err)
	assert.Equal(t, int64(2), acked, "the new host's own sequence is intact")
}

// A report in flight when the conductor is taken over by another host either
// finishes first, and the new host starts from it, or is refused: it never
// commits after the takeover has returned, which would put events at numbers
// the new host is about to use itself.
//
// The batch is held at its first insert until the database says the takeover
// is waiting on it, so the takeover is made mid-batch every time. Holding the conductor's host row share-locked is
// what makes the takeover wait for it: without that, the takeover returns
// mid-batch and the batch lands after it.
func TestAReportInFlightWhenAnotherHostTakesOverIsWaitedFor(t *testing.T) {
	f := newFixture(t)
	impatient := f.newService(&conductor.Options{HostTTL: time.Nanosecond})
	t.Cleanup(impatient.Close)
	chunk := strings.Repeat("x", conductor.MaxDeltaBytes/2)
	batch := make([]conductor.HostEvent, conductor.MaxEventsPerBatch)
	for i := range batch {
		batch[i] = text(chunk, int64(i+1))
	}

	for round := range 4 {
		owned := f.owned(t, f.owner, "takeover"+string(rune('a'+round)))
		c := f.start(t, owned)
		old := f.polling(t, owned)
		// The batch is held inside its transaction, on its first insert, by a
		// transaction of the test's that has the events table to itself. (Taking
		// the event number it wants would not do: the foreign key's lock on the
		// conversation would hold the batch up before it had the host row.)
		holding, release := make(chan struct{}), make(chan struct{})
		heldDone := make(chan struct{})
		go func() {
			defer close(heldDone)
			_ = f.database.InTx(f.ctx, func(q db.Querier) error {
				_, err := q.Exec(f.ctx, `LOCK TABLE conductor_conversation_events IN SHARE MODE`)
				close(holding)
				<-release
				if err != nil {
					return err
				}
				return errors.New("roll back: nothing to keep")
			})
		}()
		<-holding
		var releaseOnce sync.Once
		releaseHeld := func() { releaseOnce.Do(func() { close(release) }) }
		// So a failed assertion does not leave the held transaction, and with it
		// the test's cleanup, waiting.
		t.Cleanup(releaseHeld)
		reported := make(chan error, 1)
		go func() {
			_, err := f.svc.Report(f.ctx, old.proof, c.ID, &conductor.ReportRequest{HostID: old.id, Events: batch})
			reported <- err
		}()
		f.awaitBlocked(t, "INSERT INTO conductor_conversation_events")

		takenOver := make(chan error, 1)
		successor := f.host(t, owned)
		go func() {
			_, err := impatient.Poll(f.ctx, successor.proof, &conductor.PollRequest{HostID: successor.id})
			takenOver <- err
		}()
		// The takeover is waiting on the batch's hold of the conductor's host row.
		f.awaitBlocked(t, "INSERT INTO conductor_queues (conductor_id, host_id")
		select {
		case <-takenOver:
			t.Fatal("the takeover overtook a report in flight")
		default:
		}

		releaseHeld()
		<-heldDone
		require.NoError(t, <-takenOver)
		got, err := f.svc.Get(f.ctx, owned, c.ID)
		require.NoError(t, err)
		atTakeover := got.LastSeq
		require.NoError(t, <-reported, "the batch in flight finishes before the takeover does")
		assert.Equal(t, int64(len(batch)), atTakeover, "round %d: the takeover waited for the whole batch", round)

		after, err := f.svc.Get(f.ctx, owned, c.ID)
		require.NoError(t, err)
		assert.Equal(t, atTakeover, after.LastSeq, "round %d: the superseded host's batch committed after the takeover", round)
	}
}

// awaitBlocked waits until a statement containing text is waiting on a lock. It
// reads the database's own account of who is waiting, so it waits on state, not
// on time.
func (f *fixture) awaitBlocked(t *testing.T, text string) {
	t.Helper()
	require.Eventually(t, func() bool {
		var waiting int
		if err := f.database.Global().QueryRow(context.Background(), `
			SELECT count(*) FROM pg_stat_activity
			 WHERE wait_event_type = 'Lock' AND query LIKE '%' || $1 || '%' AND pid <> pg_backend_pid()`, text).Scan(&waiting); err != nil {
			return false
		}
		return waiting > 0
	}, 10*time.Second, 5*time.Millisecond)
}
