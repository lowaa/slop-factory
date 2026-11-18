package conductor_test

import (
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/agent"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/conductor"
)

func (f *fixture) poll(t *testing.T, svc *conductor.Service, h *host, ack int64) *conductor.PollResult {
	t.Helper()
	res, err := svc.Poll(f.ctx, h.proof, &conductor.PollRequest{HostID: h.id, Ack: ack,
		Capabilities: []string{conductor.CapabilityInterrupt}, Version: "test"})
	require.NoError(t, err)
	return res
}

func (f *fixture) report(t *testing.T, h *host, c *conductor.Conversation, events ...conductor.HostEvent) int64 {
	t.Helper()
	acked, err := f.svc.Report(f.ctx, h.proof, c.ID, &conductor.ReportRequest{HostID: h.id, Events: events})
	require.NoError(t, err)
	return acked
}

func kinds(items []conductor.WorkItem) []conductor.WorkKind {
	out := make([]conductor.WorkKind, len(items))
	for i := range items {
		out[i] = items[i].Kind
	}
	return out
}

func text(body string, seq int64) conductor.HostEvent {
	return conductor.HostEvent{HostSeq: seq, Kind: conductor.EventAssistant, Body: body}
}

func TestStartQueuesAStartAndThenTheFirstMessage(t *testing.T) {
	f := newFixture(t)
	owned := f.owned(t, f.owner, "c1")
	opening := "what is in this repo?"

	c, err := f.svc.Start(f.ctx, owned, &conductor.StartRequest{Message: &opening})
	require.NoError(t, err)

	assert.Equal(t, conductor.StateStarting, c.State)
	require.NotNil(t, c.OwnerUserID)
	assert.Equal(t, f.owner.ID, *c.OwnerUserID)
	res := f.poll(t, f.svc, f.host(t, owned), 0)
	require.Len(t, res.Items, 2)
	start, msg := res.Items[0], res.Items[1]
	assert.Equal(t, conductor.WorkStart, start.Kind)
	assert.Equal(t, c.ID, start.Conversation, "the conversation's id is the harness session id")
	assert.False(t, start.Resume)
	assert.Equal(t, conductor.WorkMessage, msg.Kind)
	assert.Equal(t, opening, msg.Text)
	assert.Less(t, start.Seq, msg.Seq)
}

func TestStartRefusesAnEmptyOpeningMessage(t *testing.T) {
	f := newFixture(t)
	owned := f.owned(t, f.owner, "c1")
	empty := "   "

	_, err := f.svc.Start(f.ctx, owned, &conductor.StartRequest{Message: &empty})

	assert.ErrorIs(t, err, conductor.ErrInvalid)
	list, err := f.svc.List(f.ctx, owned, 0)
	require.NoError(t, err)
	assert.Empty(t, list, "a refused start leaves nothing behind")
	assert.Empty(t, f.poll(t, f.svc, f.host(t, owned), 0).Items)
}

func (f *fixture) live(t *testing.T, owned *agent.OwnedConductor) []conductor.Conversation {
	t.Helper()
	list, err := f.svc.List(f.ctx, owned, 0)
	require.NoError(t, err)
	var live []conductor.Conversation
	for _, c := range list {
		if c.State.Live() {
			live = append(live, c)
		}
	}
	return live
}

// The host runs one process, so opening a session replaces the live one
// instead of being refused: the stop is queued ahead of the new start, so the
// host ends the old process first.
func TestStartingASessionReplacesTheLiveOne(t *testing.T) {
	f := newFixture(t)
	owned := f.owned(t, f.owner, "c1")
	first := f.start(t, owned)

	second := f.start(t, owned)

	assert.Equal(t, []uuid.UUID{second.ID}, []uuid.UUID{f.live(t, owned)[0].ID})
	got, err := f.svc.Get(f.ctx, owned, first.ID)
	require.NoError(t, err)
	assert.Equal(t, conductor.StateStopped, got.State)
	res := f.poll(t, f.svc, f.host(t, owned), 0)
	assert.Equal(t, []conductor.WorkKind{conductor.WorkStart, conductor.WorkStop, conductor.WorkStart}, kinds(res.Items))
	assert.Equal(t, first.ID, res.Items[1].Conversation, "the old one's process is stopped")
	assert.Equal(t, second.ID, res.Items[2].Conversation)
	events := f.transcript(t, owned, first)
	require.Len(t, events, 1)
	assert.Equal(t, conductor.EventStopped, events[0].Kind)

	// Another conductor, even the same owner's, is its own machine.
	elsewhere := f.owned(t, f.owner, "c2")
	f.start(t, elsewhere)
	assert.Len(t, f.live(t, owned), 1)
}

// A message to a session with no process brings it back, resuming the Claude
// session, and replaces the live one.
func TestAMessageReconnectsASessionWithNoProcessAndReplacesTheLiveOne(t *testing.T) {
	f := newFixture(t)
	owned := f.owned(t, f.owner, "c1")
	old := f.start(t, owned)
	_, err := f.svc.Disconnect(f.ctx, owned, old.ID)
	require.NoError(t, err)
	current := f.start(t, owned)

	_, err = f.svc.Send(f.ctx, owned, old.ID, &conductor.SendRequest{Text: "back"})
	require.NoError(t, err)

	live := f.live(t, owned)
	require.Len(t, live, 1)
	assert.Equal(t, old.ID, live[0].ID)
	got, err := f.svc.Get(f.ctx, owned, current.ID)
	require.NoError(t, err)
	assert.Equal(t, conductor.StateStopped, got.State)
	res := f.poll(t, f.svc, f.host(t, owned), 0)
	require.Len(t, res.Items, 6)
	// start old, stop old, start current, then the reconnect: stop current, start old (resume), message.
	assert.Equal(t, []conductor.WorkKind{conductor.WorkStart, conductor.WorkStop, conductor.WorkStart,
		conductor.WorkStop, conductor.WorkStart, conductor.WorkMessage}, kinds(res.Items))
	assert.Equal(t, current.ID, res.Items[3].Conversation)
	assert.True(t, res.Items[4].Resume)
	assert.Equal(t, old.ID, res.Items[4].Conversation)
	assert.Equal(t, "back", res.Items[5].Text)
}

// The retry of a send whose reply was lost must not do the replacing again: the
// session the first made live may have been replaced since, and the retry only
// has to find what the first stored.
func TestASendRepeatedAfterTheSessionWasReplacedReplacesNothing(t *testing.T) {
	f := newFixture(t)
	owned := f.owned(t, f.owner, "c1")
	old := f.start(t, owned)
	_, err := f.svc.Disconnect(f.ctx, owned, old.ID)
	require.NoError(t, err)
	id := uuid.New()
	req := &conductor.SendRequest{Text: "back", ClientID: &id}
	first, err := f.svc.Send(f.ctx, owned, old.ID, req)
	require.NoError(t, err)
	current := f.start(t, owned) // replaces old

	again, err := f.svc.Send(f.ctx, owned, old.ID, req)

	require.NoError(t, err)
	assert.Equal(t, first.Seq, again.Seq)
	live := f.live(t, owned)
	require.Len(t, live, 1)
	assert.Equal(t, current.ID, live[0].ID, "the retry did not take the conductor back")
}

func TestAMessageToALiveSessionReplacesNothing(t *testing.T) {
	f := newFixture(t)
	owned := f.owned(t, f.owner, "c1")
	c := f.start(t, owned)

	_, err := f.svc.Send(f.ctx, owned, c.ID, &conductor.SendRequest{Text: "hi"})
	require.NoError(t, err)

	assert.Equal(t, []conductor.WorkKind{conductor.WorkStart, conductor.WorkMessage}, kinds(f.poll(t, f.svc, f.host(t, owned), 0).Items))
}

// raceAll runs every function at once: each is started, and all wait at a gate
// that opens only once the last has reached it, so the calls are in flight
// together and not one after another.
func raceAll(fns []func()) {
	var ready, done sync.WaitGroup
	gate := make(chan struct{})
	ready.Add(len(fns))
	done.Add(len(fns))
	for _, fn := range fns {
		go func() {
			defer done.Done()
			ready.Done()
			<-gate
			fn()
		}()
	}
	ready.Wait()
	close(gate)
	done.Wait()
}

// The rule is the database's, not a check followed by a write: the starts
// race and the loser is told to try again, but they never leave two sessions
// live.
func TestConcurrentStartsLeaveExactlyOneLiveConversation(t *testing.T) {
	f := newFixture(t)
	owned := f.owned(t, f.owner, "c1")
	const racers = 8
	var (
		mu   sync.Mutex
		errs []error
	)
	fns := make([]func(), racers)
	for i := range fns {
		fns[i] = func() {
			_, err := f.svc.Start(f.ctx, owned, &conductor.StartRequest{})
			mu.Lock()
			defer mu.Unlock()
			errs = append(errs, err)
		}
	}
	raceAll(fns)

	won := 0
	for _, err := range errs {
		if err == nil {
			won++
		} else {
			assert.ErrorIs(t, err, conductor.ErrBusy)
		}
	}
	assert.GreaterOrEqual(t, won, 1)
	assert.Len(t, f.live(t, owned), 1)
}

// Messages to several sessions that have no process each bring theirs back at
// once. Whoever commits first is the live one and the rest are told to try
// again: the one-live-conversation index decides, never two live.
func TestConcurrentReconnectsLeaveExactlyOneLiveConversation(t *testing.T) {
	f := newFixture(t)
	owned := f.owned(t, f.owner, "c1")
	var sessions []uuid.UUID
	for range 3 {
		c := f.start(t, owned)
		_, err := f.svc.Disconnect(f.ctx, owned, c.ID)
		require.NoError(t, err)
		sessions = append(sessions, c.ID)
	}
	var (
		mu   sync.Mutex
		errs []error
	)
	var fns []func()
	for range 4 {
		for _, id := range sessions {
			fns = append(fns, func() {
				_, err := f.svc.Send(f.ctx, owned, id, &conductor.SendRequest{Text: "x"})
				mu.Lock()
				defer mu.Unlock()
				errs = append(errs, err)
			})
		}
	}
	raceAll(fns)

	won := 0
	for _, err := range errs {
		if err == nil {
			won++
		} else {
			assert.ErrorIs(t, err, conductor.ErrBusy, "never a deadlock or a unique violation")
		}
	}
	assert.GreaterOrEqual(t, won, 1)
	assert.Len(t, f.live(t, owned), 1)
}

func TestWorkIsDeliveredInOrderAndAgainUntilAcknowledged(t *testing.T) {
	f := newFixture(t)
	owned := f.owned(t, f.owner, "c1")
	c := f.start(t, owned)
	for _, m := range []string{"one", "two", "three"} {
		_, err := f.svc.Send(f.ctx, owned, c.ID, &conductor.SendRequest{Text: m})
		require.NoError(t, err)
	}
	h := f.host(t, owned)

	first := f.poll(t, f.svc, h, 0)
	again := f.poll(t, f.svc, h, 0)

	require.Len(t, first.Items, 4)
	assert.Equal(t, first.Items, again.Items, "a host that died after receiving them is given them again")
	assert.Equal(t, []conductor.WorkKind{conductor.WorkStart, conductor.WorkMessage, conductor.WorkMessage, conductor.WorkMessage}, kinds(first.Items))
	assert.Equal(t, []string{"one", "two", "three"}, []string{first.Items[1].Text, first.Items[2].Text, first.Items[3].Text})

	upTo := first.Items[1].Seq
	rest := f.poll(t, f.svc, h, upTo)
	assert.Equal(t, upTo, rest.Acked)
	assert.Equal(t, first.Items[2:], rest.Items, "what was acknowledged is not delivered again")
}

func TestAHostCannotAcknowledgeWorkThatDoesNotExistYet(t *testing.T) {
	f := newFixture(t)
	owned := f.owned(t, f.owner, "c1")
	c := f.start(t, owned)
	h := f.host(t, owned)
	res := f.poll(t, f.svc, h, 1_000_000)
	assert.Equal(t, int64(1), res.Acked, "clamped to the start that exists")
	assert.Empty(t, res.Items)

	_, err := f.svc.Send(f.ctx, owned, c.ID, &conductor.SendRequest{Text: "still delivered"})
	require.NoError(t, err)

	later := f.poll(t, f.svc, h, 0)
	require.Len(t, later.Items, 1)
	assert.Equal(t, "still delivered", later.Items[0].Text)
}

// A sequence hands out numbers in one order and commits in another, so a host
// that had acknowledged 6 could later be shown a 5. Numbers come from a row
// the producers queue on, so whatever a read sees is a gapless prefix.
func TestWorkNeverShowsAGapThatFillsLater(t *testing.T) {
	f := newFixture(t)
	owned := f.owned(t, f.owner, "c1")
	c := f.start(t, owned)
	const senders, each = 6, 15
	h := f.host(t, owned)

	var (
		wg   sync.WaitGroup
		done = make(chan struct{})
	)
	for s := range senders {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range each {
				_, err := f.svc.Send(f.ctx, owned, c.ID, &conductor.SendRequest{Text: string(rune('a'+s)) + string(rune('0'+i%10))})
				assert.NoError(t, err)
			}
		}()
	}
	go func() { wg.Wait(); close(done) }()

	var seen []conductor.WorkItem
	acked := int64(0)
	// A poll returns at most MaxWorkPerPoll items, so once the senders are
	// done keep reading until a poll comes back empty rather than assuming
	// one more poll sees everything.
	for finished := false; ; {
		select {
		case <-done:
			finished = true
		default:
		}
		res := f.poll(t, f.svc, h, acked)
		for i := range res.Items {
			assert.Equal(t, acked+int64(i)+1, res.Items[i].Seq, "a read is a gapless run from the acknowledged number")
		}
		if len(res.Items) > 0 {
			seen = append(seen, res.Items...)
			acked = res.Items[len(res.Items)-1].Seq
		}
		if finished && len(res.Items) == 0 {
			break
		}
	}
	assert.Len(t, seen, 1+senders*each)
}

func TestOnlyOneHostServesAConductorUntilItGoesQuiet(t *testing.T) {
	f := newFixture(t)
	owned := f.owned(t, f.owner, "c1")
	f.start(t, owned)
	first, second := f.host(t, owned), f.host(t, owned)
	f.poll(t, f.svc, first, 0)

	_, err := f.svc.Poll(f.ctx, second.proof, &conductor.PollRequest{HostID: second.id})
	assert.ErrorIs(t, err, conductor.ErrHostBusy)
	f.poll(t, f.svc, first, 0)

	// A replica configured to give up on a host at once.
	impatient := f.newService(&conductor.Options{HostTTL: time.Nanosecond})
	t.Cleanup(impatient.Close)
	res, err := impatient.Poll(f.ctx, second.proof, &conductor.PollRequest{HostID: second.id})
	require.NoError(t, err)
	assert.Len(t, res.Items, 1, "the new host is given the work the old one had not acknowledged")
	_, err = f.svc.Poll(f.ctx, first.proof, &conductor.PollRequest{HostID: first.id})
	assert.ErrorIs(t, err, conductor.ErrHostBusy, "and the old one is now the one refused")
}

func TestAReleaseByTheCurrentHostLetsANewHostPollAtOnce(t *testing.T) {
	f := newFixture(t)
	owned := f.owned(t, f.owner, "c1")
	f.start(t, owned)
	first, second := f.host(t, owned), f.host(t, owned)
	f.poll(t, f.svc, first, 0)
	_, err := f.svc.Poll(f.ctx, second.proof, &conductor.PollRequest{HostID: second.id})
	require.ErrorIs(t, err, conductor.ErrHostBusy)

	require.NoError(t, f.svc.Release(f.ctx, first.proof, first.id))

	status, err := f.svc.HostInfo(f.ctx, owned)
	require.NoError(t, err)
	assert.False(t, status.Online, "the conductor shows no host")
	res := f.poll(t, f.svc, second, 0)
	assert.Len(t, res.Items, 1, "the new host is given the work the old one had not acknowledged")
	_, err = f.svc.Poll(f.ctx, first.proof, &conductor.PollRequest{HostID: first.id})
	assert.ErrorIs(t, err, conductor.ErrHostBusy, "and the old one is refused")
}

func TestAReleaseByAHostThatWasReplacedChangesNothing(t *testing.T) {
	f := newFixture(t)
	owned := f.owned(t, f.owner, "c1")
	first, second := f.host(t, owned), f.host(t, owned)
	f.poll(t, f.svc, first, 0)
	impatient := f.newService(&conductor.Options{HostTTL: time.Nanosecond})
	t.Cleanup(impatient.Close)
	_, err := impatient.Poll(f.ctx, second.proof, &conductor.PollRequest{HostID: second.id})
	require.NoError(t, err)

	require.NoError(t, f.svc.Release(f.ctx, first.proof, first.id))

	status, err := f.svc.HostInfo(f.ctx, owned)
	require.NoError(t, err)
	assert.True(t, status.Online, "the successor is still the host")
	_, err = f.svc.Poll(f.ctx, first.proof, &conductor.PollRequest{HostID: first.id})
	assert.ErrorIs(t, err, conductor.ErrHostBusy, "and still the one polling")
	f.poll(t, f.svc, second, 0)
}

func TestAReleaseTwiceOrBeforeAnyPollIsANoOp(t *testing.T) {
	f := newFixture(t)
	owned := f.owned(t, f.owner, "c1")
	h := f.host(t, owned)
	require.NoError(t, f.svc.Release(f.ctx, h.proof, h.id), "no queue row yet")
	f.poll(t, f.svc, h, 0)

	require.NoError(t, f.svc.Release(f.ctx, h.proof, h.id))
	require.NoError(t, f.svc.Release(f.ctx, h.proof, h.id))

	f.poll(t, f.svc, h, 0)
}

func TestReleaseNeedsAHostID(t *testing.T) {
	f := newFixture(t)
	h := f.host(t, f.owned(t, f.owner, "c1"))
	assert.ErrorIs(t, f.svc.Release(f.ctx, h.proof, uuid.Nil), conductor.ErrInvalid)
}

func TestPollNeedsAHostID(t *testing.T) {
	f := newFixture(t)
	owned := f.owned(t, f.owner, "c1")

	_, err := f.svc.Poll(f.ctx, f.host(t, owned).proof, &conductor.PollRequest{})

	assert.ErrorIs(t, err, conductor.ErrInvalid)
}

func TestARefusedMessageToASessionWithNoProcessDoesNotReconnectIt(t *testing.T) {
	f := newFixture(t)
	owned := f.owned(t, f.owner, "c1")
	c := f.start(t, owned)
	_, err := f.svc.Disconnect(f.ctx, owned, c.ID)
	require.NoError(t, err)
	f.setNextSeq(t, c, conductor.MaxEventsPerConversation)

	_, err = f.svc.Send(f.ctx, owned, c.ID, &conductor.SendRequest{Text: "too late"})

	assert.ErrorIs(t, err, conductor.ErrTranscriptFull)
	got, err := f.svc.Get(f.ctx, owned, c.ID)
	require.NoError(t, err)
	assert.Equal(t, conductor.StateStopped, got.State)
	assert.Equal(t, []conductor.WorkKind{conductor.WorkStart, conductor.WorkStop}, kinds(f.poll(t, f.svc, f.host(t, owned), 0).Items),
		"the refusal rolled back the reconnect's start")
}

func TestAnEmptyMessageIsRefusedBeforeAnythingIsQueued(t *testing.T) {
	f := newFixture(t)
	owned := f.owned(t, f.owner, "c1")
	c := f.start(t, owned)
	_, err := f.svc.Disconnect(f.ctx, owned, c.ID)
	require.NoError(t, err)

	_, err = f.svc.Send(f.ctx, owned, c.ID, &conductor.SendRequest{Text: ""})

	assert.ErrorIs(t, err, conductor.ErrInvalid)
	assert.Equal(t, []conductor.WorkKind{conductor.WorkStart, conductor.WorkStop}, kinds(f.poll(t, f.svc, f.host(t, owned), 0).Items))
}

func TestASendRepeatedWithTheSameClientIDIsStoredAndQueuedOnce(t *testing.T) {
	f := newFixture(t)
	owned := f.owned(t, f.owner, "c1")
	c := f.start(t, owned)
	id := uuid.New()

	first, err := f.svc.Send(f.ctx, owned, c.ID, &conductor.SendRequest{Text: "hi", ClientID: &id})
	require.NoError(t, err)
	again, err := f.svc.Send(f.ctx, owned, c.ID, &conductor.SendRequest{Text: "hi", ClientID: &id})
	require.NoError(t, err)

	assert.Equal(t, first.Seq, again.Seq)
	res := f.poll(t, f.svc, f.host(t, owned), 0)
	assert.Equal(t, []conductor.WorkKind{conductor.WorkStart, conductor.WorkMessage}, kinds(res.Items))
}

func TestMessageLimits(t *testing.T) {
	f := newFixture(t)
	owned := f.owned(t, f.owner, "c1")
	c := f.start(t, owned)

	for name, body := range map[string]string{
		"empty":     "",
		"too big":   string(make([]byte, conductor.MaxMessageBytes+1)),
		"has a NUL": "a\x00b",
		"not utf-8": "a\xffb",
	} {
		_, err := f.svc.Send(f.ctx, owned, c.ID, &conductor.SendRequest{Text: body})
		assert.ErrorIs(t, err, conductor.ErrInvalid, name)
	}
}

func TestTheQueueIsBoundedWhileTheHostIsAway(t *testing.T) {
	f := newFixture(t)
	owned := f.owned(t, f.owner, "c1")
	c := f.start(t, owned)

	var err error
	sent := 1
	for ; err == nil && sent <= conductor.MaxQueuedWork+5; sent++ {
		_, err = f.svc.Send(f.ctx, owned, c.ID, &conductor.SendRequest{Text: "x"})
	}

	assert.ErrorIs(t, err, conductor.ErrQueueFull)
	assert.Equal(t, conductor.MaxQueuedWork, sent-1, "the start and the sends fill it, and no more")
	res := f.poll(t, f.svc, f.host(t, owned), 0)
	assert.Len(t, res.Items, conductor.MaxWorkPerPoll)
}

// The owner can always end the conversation, whatever the host's backlog: a
// full queue must not leave the conductor stuck on it.
func TestADisconnectGoesThroughAFullQueue(t *testing.T) {
	f := newFixture(t)
	owned := f.owned(t, f.owner, "c1")
	c := f.start(t, owned)
	for {
		if _, err := f.svc.Send(f.ctx, owned, c.ID, &conductor.SendRequest{Text: "x"}); err != nil {
			require.ErrorIs(t, err, conductor.ErrQueueFull)
			break
		}
	}

	stopped, err := f.svc.Disconnect(f.ctx, owned, c.ID)

	require.NoError(t, err)
	assert.Equal(t, conductor.StateStopped, stopped.State)
}

func TestHostInfoCarriesTheBuildIdOfTheHostPackageTheServerShips(t *testing.T) {
	f := newFixture(t)
	owned := f.owned(t, f.owner, "c1")
	h := f.host(t, owned)
	_, err := f.svc.Poll(f.ctx, h.proof, &conductor.PollRequest{HostID: h.id, Version: "aaaaaaaaaaaa"})
	require.NoError(t, err)

	plain, err := f.svc.HostInfo(f.ctx, owned)
	require.NoError(t, err)
	assert.Empty(t, plain.CurrentVersion, "a server that ships no package has nothing to compare with")

	shipping := f.newService(&conductor.Options{CurrentHostVersion: "bbbbbbbbbbbb"})
	t.Cleanup(shipping.Close)
	status, err := shipping.HostInfo(f.ctx, owned)
	require.NoError(t, err)
	assert.Equal(t, "bbbbbbbbbbbb", status.CurrentVersion)
	assert.Equal(t, "aaaaaaaaaaaa", status.Version, "beside what the host reported")
}
