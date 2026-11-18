package conductor_test

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-clock"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/agent"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/conductor"
)

// sseEvent is one event read off a stream.
type sseEvent struct {
	ID   int64
	Kind string
	Body string
}

// sseClient reads a conversation's stream the way a browser's EventSource
// does, over a real connection. Every read is bounded so a stream that never
// delivers fails the test instead of hanging it; the bound is only that.
type sseClient struct {
	events   chan sseEvent
	comments chan string
	// named is the name of each event sent under one: a browser's onmessage,
	// which the board's own stream relies on, only hears the unnamed.
	named  chan string
	done   chan struct{}
	status int
	cancel context.CancelFunc
}

func (r *replica) stream(t *testing.T, token, path, lastEventID string) *sseClient {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.server.URL+path, nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+token)
	if lastEventID != "" {
		req.Header.Set("Last-Event-ID", lastEventID)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	c := &sseClient{events: make(chan sseEvent, 1024), comments: make(chan string, 16), named: make(chan string, 16), done: make(chan struct{}), status: resp.StatusCode, cancel: cancel}
	t.Cleanup(func() { cancel(); _ = resp.Body.Close() })
	go func() {
		defer close(c.done)
		defer func() { _ = resp.Body.Close() }()
		var cur sseEvent
		var name string
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			line := sc.Text()
			switch {
			case strings.HasPrefix(line, ":"):
				select {
				case c.comments <- strings.TrimSpace(line[1:]):
				default:
				}
			case strings.HasPrefix(line, "event:"):
				name = strings.TrimSpace(line[len("event:"):])
			case strings.HasPrefix(line, "id:"):
				cur.ID, _ = strconv.ParseInt(strings.TrimSpace(line[3:]), 10, 64)
			case strings.HasPrefix(line, "data:"):
				var body struct {
					Seq        int64
					Kind, Body string
				}
				_ = json.Unmarshal([]byte(strings.TrimSpace(line[5:])), &body)
				cur.Kind, cur.Body = body.Kind, body.Body
			case line == "" && cur.ID != 0:
				if name != "" {
					c.named <- name
				}
				c.events <- cur
				cur, name = sseEvent{}, ""
			}
		}
	}()
	return c
}

func (c *sseClient) next(t *testing.T) sseEvent {
	t.Helper()
	select {
	case e := <-c.events:
		return e
	case <-time.After(10 * time.Second):
		t.Fatal("the stream delivered no event")
		return sseEvent{}
	}
}

// closed waits for the server to end the stream.
func (c *sseClient) closed(t *testing.T) {
	t.Helper()
	select {
	case <-c.done:
	case <-time.After(10 * time.Second):
		t.Fatal("the stream did not end")
	}
}

func fastStream() *conductor.StreamOptions {
	return &conductor.StreamOptions{PollInterval: 5 * time.Millisecond, RecheckEvery: 1}
}

// holdsSlot (for a stream opened with a short KeepAlive) waits until the server is running the stream's handler, which is
// where a cap slot is taken. The response headers go out before the handler
// starts, so a 200 alone does not mean the slot is held, and a second stream
// opened on it could win the slot instead of being refused. The handler's
// first keepalive is written only after the slot is taken.
func (c *sseClient) holdsSlot(t *testing.T) {
	t.Helper()
	require.Equal(t, http.StatusOK, c.status)
	c.comment(t, "keepalive")
}

type streamRig struct {
	*rest
	streams *replica
	path    string
	conv    conversationJSON
}

// newStreamRig is a conversation with its stream served by a replica other
// than the one the events are reported through.
func newStreamRig(t *testing.T, streamOpts *conductor.StreamOptions) *streamRig {
	t.Helper()
	r := newRest(t)
	streamSide := r.fixture.replica(t, nil, streamOpts)
	streamSide.users["owner"], streamSide.users["other"] = r.owner, r.other
	conv := r.start(t, map[string]any{})
	return &streamRig{rest: r, streams: streamSide, conv: conv,
		path: r.base + "/" + conv.ID + "/stream"}
}

// report posts host events through the other replica.
func (s *streamRig) report(t *testing.T, hostID string, events ...map[string]any) {
	t.Helper()
	rec := s.api.do(s.hostToken, http.MethodPost, "/api/conductor/conversations/"+s.conv.ID+"/events",
		map[string]any{"host_id": hostID, "events": events})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
}

func (s *streamRig) pollAsHost(hostID string) {
	s.api.do(s.hostToken, http.MethodPost, "/api/conductor/poll", map[string]any{"host_id": hostID})
}

func TestTheStreamSendsEventsInOrderAsAnotherReplicaStoresThem(t *testing.T) {
	s := newStreamRig(t, fastStream())
	hostID := newHostID()
	s.pollAsHost(hostID)
	stream := s.streams.stream(t, "owner", s.path, "")
	require.Equal(t, http.StatusOK, stream.status)

	s.report(t, hostID,
		map[string]any{"host_seq": 1, "kind": "ready", "work_seq": 1},
		map[string]any{"host_seq": 2, "kind": "assistant", "turn": 1, "text": "one"})
	first, second := stream.next(t), stream.next(t)
	s.report(t, hostID, map[string]any{"host_seq": 3, "kind": "assistant", "turn": 1, "text": "two"})
	third := stream.next(t)

	assert.Equal(t, sseEvent{ID: 1, Kind: "ready", Body: ""}, first)
	assert.Equal(t, sseEvent{ID: 2, Kind: "assistant", Body: "one"}, second)
	assert.Equal(t, sseEvent{ID: 3, Kind: "assistant", Body: "two"}, third)
	assert.Empty(t, stream.named, "a browser's EventSource.onmessage does not hear a named event")
}

func TestTheStreamStartsWithWhatIsAlreadyStoredAndResumesAfterTheLastIDSeen(t *testing.T) {
	s := newStreamRig(t, fastStream())
	hostID := newHostID()
	s.pollAsHost(hostID)
	s.report(t, hostID,
		map[string]any{"host_seq": 1, "kind": "ready", "work_seq": 1},
		map[string]any{"host_seq": 2, "kind": "assistant", "text": "a"},
		map[string]any{"host_seq": 3, "kind": "assistant", "text": "b"})

	whole := s.streams.stream(t, "owner", s.path, "")
	assert.Equal(t, int64(1), whole.next(t).ID)
	assert.Equal(t, int64(2), whole.next(t).ID)
	assert.Equal(t, int64(3), whole.next(t).ID)
	whole.cancel()

	// What the browser sends when its connection drops after event 2.
	resumed := s.streams.stream(t, "owner", s.path, "2")
	assert.Equal(t, int64(3), resumed.next(t).ID, "nothing seen twice")
	s.report(t, hostID, map[string]any{"host_seq": 4, "kind": "assistant", "text": "c"})
	assert.Equal(t, int64(4), resumed.next(t).ID, "and nothing missed")

	byQuery := s.streams.stream(t, "owner", s.path+"?after=3", "")
	assert.Equal(t, int64(4), byQuery.next(t).ID)
}

func TestTheStreamIsRefusedBeforeItStartsForAnyoneButTheOwner(t *testing.T) {
	s := newStreamRig(t, fastStream())
	s.streams.users["admin"] = s.admin

	assert.Equal(t, http.StatusForbidden, s.streams.stream(t, "other", s.path, "").status)
	assert.Equal(t, http.StatusForbidden, s.streams.stream(t, "admin", s.path, "").status)
	assert.Equal(t, http.StatusNotFound, s.streams.stream(t, "owner", s.base+"/"+newHostID()+"/stream", "").status)
}

func TestDisablingTheConductorEndsItsStreamAfterTheStoppedEvent(t *testing.T) {
	s := newStreamRig(t, fastStream())
	stream := s.streams.stream(t, "owner", s.path, "")
	require.Equal(t, http.StatusOK, stream.status)

	_, err := s.conductors.SetDisabled(s.ctx, s.admin, s.mine.Agent().ID, true)
	require.NoError(t, err)

	assert.Equal(t, "stopped", stream.next(t).Kind)
	stream.closed(t)
}

func TestTheOwnerLeavingTheTenantEndsTheirStream(t *testing.T) {
	s := newStreamRig(t, fastStream())
	stream := s.streams.stream(t, "owner", s.path, "")
	require.Equal(t, http.StatusOK, stream.status)

	_, err := s.database.Global().Exec(context.Background(),
		`DELETE FROM global_tenant_memberships WHERE tenant = $1 AND user_id = $2`, s.tenant, s.owner.ID)
	require.NoError(t, err)

	stream.closed(t)
	select {
	case e := <-stream.events:
		t.Fatalf("a person who is no longer a member was sent %+v after losing access", e)
	default:
	}
}

// leavingOwners removes the owner from the tenant right after the first
// ownership check of an armed stream passes: the moment between a stream's
// recheck and its read of the events, which no test can hit by timing.
type leavingOwners struct {
	conductor.Owners
	armed atomic.Bool
	leave func()
	once  sync.Once
}

func (o *leavingOwners) AuthorizeOwner(ctx context.Context, who *authn.User, id uuid.UUID) (*agent.OwnedConductor, error) {
	owned, err := o.Owners.AuthorizeOwner(ctx, who, id)
	if err == nil && o.armed.Load() {
		o.once.Do(o.leave)
	}
	return owned, err
}

func TestAnOwnerRemovedAfterTheRecheckIsNotSentWhatTheRemovalCommitted(t *testing.T) {
	r := newRest(t)
	owners := &leavingOwners{Owners: r.fixture.conductors}
	owners.leave = func() {
		_, err := r.database.Global().Exec(context.Background(),
			`DELETE FROM global_tenant_memberships WHERE tenant = $1 AND user_id = $2`, r.tenant, r.owner.ID)
		assert.NoError(t, err)
	}
	side := r.fixture.bareReplicaWithOwners(t, owners, fastStream())
	side.users["owner"] = r.owner
	conv := r.start(t, map[string]any{})
	stream := side.stream(t, "owner", r.base+"/"+conv.ID+"/stream", "")
	require.Equal(t, http.StatusOK, stream.status)

	owners.armed.Store(true)

	stream.closed(t)
	select {
	case e := <-stream.events:
		t.Fatalf("a person who is no longer a member was sent %+v after losing access", e)
	default:
	}
}

func TestShuttingDownEndsOpenStreams(t *testing.T) {
	s := newStreamRig(t, fastStream())
	stream := s.streams.stream(t, "owner", s.path, "")
	require.Equal(t, http.StatusOK, stream.status)

	s.streams.svc.Close()

	stream.closed(t)
}

func TestStreamsAreCappedPerUser(t *testing.T) {
	opts := fastStream()
	opts.PerUser = 1
	opts.KeepAlive = time.Millisecond
	s := newStreamRig(t, opts)
	first := s.streams.stream(t, "owner", s.path, "")
	first.holdsSlot(t)

	second := s.streams.stream(t, "owner", s.path, "")

	second.closed(t)
	select {
	case c := <-second.comments:
		assert.Contains(t, c, "too many")
	default:
		t.Fatal("the refused stream said nothing")
	}
	// The first is untouched, and a slot freed is reusable.
	first.cancel()
	first.closed(t)
}

func (c *sseClient) comment(t *testing.T, want string) {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case got := <-c.comments:
			if strings.Contains(got, want) {
				return
			}
		case <-deadline:
			t.Fatalf("the stream never sent a %q comment", want)
		}
	}
}

// A stream with nothing to say still writes, or the server's write deadline
// would break it on its next event and a dead client would go unnoticed.
func TestAQuietStreamKeepsItselfAlive(t *testing.T) {
	opts := fastStream()
	opts.KeepAlive = 10 * time.Millisecond
	s := newStreamRig(t, opts)

	stream := s.streams.stream(t, "owner", s.path, "")
	require.Equal(t, http.StatusOK, stream.status)

	stream.comment(t, "keepalive")
}

// A stream ends with the access token that opened it, so a session signed
// out elsewhere stops getting events; here the token's lifetime is short.
func TestAStreamEndsWhenItsLifetimeIsUp(t *testing.T) {
	opts := fastStream()
	opts.MaxLifetime = time.Minute
	fake := clock.NewFake(time.Now())
	opts.Clock = fake
	s := newStreamRig(t, opts)

	stream := s.streams.stream(t, "owner", s.path, "")
	require.Equal(t, http.StatusOK, stream.status)
	// The poll ticker and the lifetime deadline are both waiting.
	fake.BlockUntil(2)
	fake.Advance(time.Minute)
	stream.closed(t)
}

func TestStreamsAreCappedForTheWholeProcess(t *testing.T) {
	opts := fastStream()
	opts.Total = 1
	opts.KeepAlive = time.Millisecond
	s := newStreamRig(t, opts)
	// Another person, with a conductor and conversation of their own: the
	// per-user cap is not what refuses them.
	theirs := s.owned(t, s.other, "theirs")
	conv, err := s.svc.Start(s.ctx, theirs, &conductor.StartRequest{})
	require.NoError(t, err)
	s.streams.users["other"] = s.other
	first := s.streams.stream(t, "owner", s.path, "")
	first.holdsSlot(t)

	second := s.streams.stream(t, "other", "/api/conductors/"+theirs.Agent().ID.String()+"/conversations/"+conv.ID.String()+"/stream", "")

	second.comment(t, "too many")
	second.closed(t)
	first.cancel()
	first.closed(t)
}

// More events than one read returns: the stream pages through them, in order,
// both from the start and from a point part way.
func TestAStreamPagesThroughATranscriptLongerThanOneRead(t *testing.T) {
	s := newStreamRig(t, fastStream())
	total := int64(conductor.MaxEventsPerRead*2 + 37)
	_, err := s.database.Exec(s.ctx, `
		INSERT INTO conductor_conversation_events (conversation_id, seq, kind, body)
		SELECT $1, n, 'assistant', 'e' || n FROM generate_series(1, $2::bigint) AS n`, uuid.MustParse(s.conv.ID), total)
	require.NoError(t, err)
	_, err = s.database.Exec(s.ctx, `UPDATE conductor_conversations SET next_seq = $2 WHERE id = $1`, uuid.MustParse(s.conv.ID), total)
	require.NoError(t, err)

	whole := s.streams.stream(t, "owner", s.path, "")
	for want := int64(1); want <= total; want++ {
		require.Equal(t, want, whole.next(t).ID)
	}
	whole.cancel()
	partway := s.streams.stream(t, "owner", s.path, strconv.Itoa(conductor.MaxEventsPerRead+5))
	for want := int64(conductor.MaxEventsPerRead + 6); want <= total; want++ {
		require.Equal(t, want, partway.next(t).ID)
	}
}

// A closed stream gives its slot back: the cap is on streams open, not on
// streams ever opened.
func TestAClosedStreamsSlotCanBeUsedAgain(t *testing.T) {
	opts := fastStream()
	opts.PerUser, opts.Total = 1, 1
	opts.KeepAlive = time.Millisecond
	s := newStreamRig(t, opts)
	first := s.streams.stream(t, "owner", s.path, "")
	first.holdsSlot(t)
	first.cancel()
	first.closed(t)

	// The client seeing its stream end does not mean the server's handler has
	// returned and released the slot yet, so a reopen can still be refused
	// (with a 200 and a comment). Retry until the slot is given back.
	var again *sseClient
	require.Eventually(t, func() bool {
		c := s.streams.stream(t, "owner", s.path, "")
		for {
			select {
			case got := <-c.comments:
				if strings.Contains(got, "too many") {
					c.cancel()
					return false
				}
				if strings.Contains(got, "keepalive") {
					again = c
					return true
				}
			case <-time.After(10 * time.Second):
				c.cancel()
				return false
			}
		}
	}, 10*time.Second, 10*time.Millisecond, "the slot was never given back")

	hostID := newHostID()
	s.pollAsHost(hostID)
	s.report(t, hostID, map[string]any{"host_seq": 1, "kind": "assistant", "text": "still here"})
	assert.Equal(t, "still here", again.next(t).Body, "a live stream, not a refusal")
}
