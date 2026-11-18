package ticket_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-auth/authn/authntest"

	kclock "github.com/lowaa/slop-factory/go-clock"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

// The board's change stream over real HTTP, against a revision the test
// moves by hand. That the revision moves on every change the board shows is
// board_revisions_test.go's, against Postgres.

// fakeRevisions is the board revision as the test sets it, recording the
// tenant each read was scoped to.
type fakeRevisions struct {
	mu       sync.Mutex
	revision int64
	tenants  []uuid.UUID
	delta    *ticket.BoardDelta
	deltaErr error
	// asked records each (since, upTo) the stream asked a delta for.
	asked [][2]int64
}

func (r *fakeRevisions) BoardDelta(_ context.Context, since, upTo int64) (*ticket.BoardDelta, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.asked = append(r.asked, [2]int64{since, upTo})
	if r.deltaErr != nil {
		return nil, r.deltaErr
	}
	if r.delta == nil {
		return &ticket.BoardDelta{}, nil
	}
	return r.delta, nil
}

func (r *fakeRevisions) setDelta(delta *ticket.BoardDelta, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.delta, r.deltaErr = delta, err
}

func newFakeRevisions(revision int64) *fakeRevisions {
	return &fakeRevisions{revision: revision}
}

func (r *fakeRevisions) BoardRevision(ctx context.Context) (int64, error) {
	tenant, err := db.TenantFrom(ctx)
	if err != nil {
		return 0, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tenants = append(r.tenants, tenant)
	return r.revision, nil
}

func (r *fakeRevisions) set(revision int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.revision = revision
}

// boardStreamServer serves the stream behind the real user middleware. The
// verifier signs every request in as the user named by its bearer token.
//
// The keepalive is sent on every poll unless a test says otherwise: the
// stream writes in order, so a keepalive is the point by which an event
// from the same poll would already have arrived, and "nothing sent" can be
// checked without waiting for a time.
func boardStreamServer(t *testing.T, revisions ticket.BoardRevisions, opts *ticket.BoardStreamOptions) *httptest.Server {
	t.Helper()
	if opts == nil {
		opts = &ticket.BoardStreamOptions{}
	}
	if opts.PollInterval == 0 {
		opts.PollInterval = time.Millisecond
	}
	if opts.KeepAlive == 0 {
		opts.KeepAlive = time.Nanosecond
	}
	mux := http.NewServeMux()
	api := humago.New(mux, huma.DefaultConfig("test", "0.0.0"))
	api.UseMiddleware(authn.UserMiddleware(api, func(_ context.Context, token string) (*authn.User, error) {
		id, err := uuid.Parse(token)
		if err != nil {
			return nil, errors.New("unknown token")
		}
		return &authn.User{ID: id, TenantID: testTenantID, IssuedAt: kclock.Or(opts.Clock).Now()}, nil
	}, authntest.Guard(), slog.New(slog.DiscardHandler)))
	ticket.RegisterBoardStream(api, ticket.NewBoardStream(revisions, opts, slog.DiscardHandler))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// sseEvent is one event as the browser would parse it.
type sseEvent struct {
	id, data, retry string
	comments        []string
}

type boardStreamClient struct {
	resp   *http.Response
	cancel context.CancelFunc
	events chan sseEvent
	// ended is closed once the server has ended the stream.
	ended chan struct{}
}

func openBoardStream(t *testing.T, srv *httptest.Server, userID uuid.UUID, query string, header http.Header) *boardStreamClient {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/tickets/stream"+query, nil)
	require.NoError(t, err)
	for name, values := range header {
		req.Header[name] = values
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Authorization", "Bearer "+userID.String())
	resp, err := srv.Client().Do(req)
	require.NoError(t, err)
	c := &boardStreamClient{resp: resp, cancel: cancel, events: make(chan sseEvent, 64), ended: make(chan struct{})}
	t.Cleanup(c.close)
	if resp.StatusCode != http.StatusOK {
		close(c.ended)
		return c
	}
	go c.read()
	return c
}

func (c *boardStreamClient) read() {
	defer close(c.ended)
	scanner := bufio.NewScanner(c.resp.Body)
	var ev sseEvent
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case line == "":
			c.events <- ev
			ev = sseEvent{}
		case strings.HasPrefix(line, ": "):
			ev.comments = append(ev.comments, strings.TrimPrefix(line, ": "))
		case strings.HasPrefix(line, "id: "):
			ev.id = strings.TrimPrefix(line, "id: ")
		case strings.HasPrefix(line, "data: "):
			ev.data = strings.TrimPrefix(line, "data: ")
		case strings.HasPrefix(line, "retry: "):
			ev.retry = strings.TrimPrefix(line, "retry: ")
		}
	}
}

func (c *boardStreamClient) close() {
	c.cancel()
	_ = c.resp.Body.Close()
}

// nextData is the next event carrying data, past any keepalives.
func (c *boardStreamClient) nextData(t *testing.T) sseEvent {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if ev := c.next(t); ev.data != "" {
			return ev
		}
		require.True(t, time.Now().Before(deadline), "only keepalives from the board stream")
	}
}

func (c *boardStreamClient) next(t *testing.T) sseEvent {
	t.Helper()
	select {
	case ev := <-c.events:
		return ev
	case <-time.After(10 * time.Second):
		t.Fatal("no event from the board stream")
		return sseEvent{}
	}
}

// noEventYet fails unless the next thing the stream sends is a keepalive.
func (c *boardStreamClient) noEventYet(t *testing.T) {
	t.Helper()
	ev := c.next(t)
	require.Empty(t, ev.data, "an event where only a keepalive was due")
	require.Equal(t, []string{"keepalive"}, ev.comments)
}

func (c *boardStreamClient) awaitEnd(t *testing.T) {
	t.Helper()
	select {
	case <-c.ended:
	case <-time.After(10 * time.Second):
		t.Fatal("the board stream did not end")
	}
}

func TestBoardStream_NoCredentialIs401(t *testing.T) {
	t.Parallel()
	srv := boardStreamServer(t, newFakeRevisions(0), nil)

	resp, err := srv.Client().Get(srv.URL + "/api/tickets/stream")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

// A client with no revision is sent the current one at once, so it has a
// cursor to reconnect with; after that, only changes are sent.
func TestBoardStream_SendsTheRevisionThenEachChange(t *testing.T) {
	t.Parallel()
	revisions := newFakeRevisions(7)
	stream := openBoardStream(t, boardStreamServer(t, revisions, nil), uuid.New(), "", nil)

	first := stream.nextData(t)
	assert.Equal(t, `{"revision":7,"reload":true}`, first.data, "with no cursor the client has nothing to apply changes to")
	assert.Equal(t, "7", first.id, "the id is the revision, which the browser resends as Last-Event-ID")
	stream.noEventYet(t)

	revisions.set(9)
	assert.Equal(t, `{"revision":9}`, stream.nextData(t).data)
	stream.noEventYet(t)
}

// A change carries the tickets that changed, as the list returns them, and
// the delta is asked for from the revision the client has to the new one.
func TestBoardStream_SendsTheChangedTickets(t *testing.T) {
	t.Parallel()
	revisions := newFakeRevisions(7)
	revisions.setDelta(&ticket.BoardDelta{Tickets: []ticket.Ticket{{ID: 12, Title: "Moved", Status: ticket.StatusInProgress, Priority: model.PriorityNormal}}}, nil)
	stream := openBoardStream(t, boardStreamServer(t, revisions, nil), uuid.New(), "?since=7", nil)
	stream.noEventYet(t)

	revisions.set(9)
	ev := stream.nextData(t)

	var body struct {
		Revision int64 `json:"revision"`
		Reload   bool  `json:"reload"`
		Tickets  []struct {
			ID     int64  `json:"id"`
			Title  string `json:"title"`
			Status string `json:"status"`
		} `json:"tickets"`
	}
	require.NoError(t, json.Unmarshal([]byte(ev.data), &body))
	assert.Equal(t, int64(9), body.Revision)
	assert.False(t, body.Reload)
	require.Len(t, body.Tickets, 1)
	assert.Equal(t, int64(12), body.Tickets[0].ID)
	assert.Equal(t, "Moved", body.Tickets[0].Title)
	revisions.mu.Lock()
	defer revisions.mu.Unlock()
	assert.Equal(t, [][2]int64{{7, 9}}, revisions.asked)
}

// A change the store cannot describe is passed on as a reload.
func TestBoardStream_PassesOnAReload(t *testing.T) {
	t.Parallel()
	revisions := newFakeRevisions(7)
	revisions.setDelta(&ticket.BoardDelta{Reload: true}, nil)
	stream := openBoardStream(t, boardStreamServer(t, revisions, nil), uuid.New(), "?since=7", nil)
	stream.noEventYet(t)

	revisions.set(8)

	assert.Equal(t, `{"revision":8,"reload":true}`, stream.nextData(t).data)
}

// A delta that fails sends nothing and is asked for again, from the same
// revision, so no change is skipped.
func TestBoardStream_RetriesAFailedDelta(t *testing.T) {
	t.Parallel()
	revisions := newFakeRevisions(7)
	revisions.setDelta(nil, errors.New("database down"))
	stream := openBoardStream(t, boardStreamServer(t, revisions, nil), uuid.New(), "?since=7", nil)
	stream.noEventYet(t)

	revisions.set(8)
	stream.noEventYet(t)
	revisions.setDelta(nil, nil)

	assert.Equal(t, `{"revision":8}`, stream.nextData(t).data)
}

// A client that already has the current revision -- reopening after a
// refresh, or the browser reconnecting by itself -- is sent nothing until
// it moves, so reconnecting doesn't make every board refetch.
func TestBoardStream_ResumesFromTheClientsRevision(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		query  string
		header http.Header
	}{
		"?since":        {query: "?since=7"},
		"Last-Event-ID": {header: http.Header{"Last-Event-ID": {"7"}}},
		// The browser keeps the URL it was opened with and adds the header
		// on its own reconnect: the newer of the two is what it has.
		"both, the header newer": {query: "?since=3", header: http.Header{"Last-Event-ID": {"7"}}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			revisions := newFakeRevisions(7)
			stream := openBoardStream(t, boardStreamServer(t, revisions, nil), uuid.New(), c.query, c.header)

			stream.noEventYet(t)

			revisions.set(8)
			assert.Equal(t, `{"revision":8}`, stream.nextData(t).data)
		})
	}
}

func TestBoardStream_ReadsAsTheUsersTenant(t *testing.T) {
	t.Parallel()
	revisions := newFakeRevisions(1)
	stream := openBoardStream(t, boardStreamServer(t, revisions, nil), uuid.New(), "", nil)
	stream.nextData(t)

	revisions.mu.Lock()
	defer revisions.mu.Unlock()
	require.NotEmpty(t, revisions.tenants)
	for _, tenant := range revisions.tenants {
		assert.Equal(t, testTenantID, tenant)
	}
}

// A quiet stream still writes, inside huma's per-write deadline -- and
// only a stream that has been quiet: one sending changes needs no more.
func TestBoardStream_KeepsAQuietStreamAlive(t *testing.T) {
	t.Parallel()
	revisions := newFakeRevisions(3)
	srv := boardStreamServer(t, revisions, &ticket.BoardStreamOptions{KeepAlive: time.Hour})
	stream := openBoardStream(t, srv, uuid.New(), "", nil)
	assert.Empty(t, stream.next(t).comments, "the first event is the revision, not a keepalive")

	quiet := boardStreamServer(t, revisions, &ticket.BoardStreamOptions{KeepAlive: time.Nanosecond})
	ev := openBoardStream(t, quiet, uuid.New(), "?since=3", nil).next(t)

	assert.Equal(t, []string{"keepalive"}, ev.comments)
	assert.Empty(t, ev.data)
}

// One user's streams are capped: the one over it is told to come back
// later and ended, and a slot comes free once a stream closes.
func TestBoardStream_CapsAUsersStreams(t *testing.T) {
	t.Parallel()
	revisions := newFakeRevisions(1)
	srv := boardStreamServer(t, revisions, &ticket.BoardStreamOptions{PerUser: 1, BusyRetry: time.Minute})
	user := uuid.New()
	first := openBoardStream(t, srv, user, "", nil)
	first.nextData(t)

	refused := openBoardStream(t, srv, user, "", nil)
	ev := refused.next(t)
	assert.Equal(t, "60000", ev.retry)
	assert.Empty(t, ev.data)
	refused.awaitEnd(t)

	other := openBoardStream(t, srv, uuid.New(), "", nil)
	assert.Equal(t, `{"revision":1,"reload":true}`, other.next(t).data, "another user's streams count separately")

	// The client going away ends its stream -- the same request context
	// the server's shutdown cancels -- and gives its slot back. The server
	// notices the close on its own schedule, so a reopen may still be
	// refused for a moment; it has to be let in once it has.
	first.close()
	deadline := time.Now().Add(10 * time.Second)
	for {
		again := openBoardStream(t, srv, user, "", nil)
		ev := again.next(t)
		again.close()
		if ev.data != "" {
			break
		}
		require.True(t, time.Now().Before(deadline), "the closed stream's slot never came free")
	}
}

// The stream ends when the token that opened it expires, giving its slot
// back: the browser's reconnect is authenticated afresh, and refused once
// the session is over.
func TestBoardStream_EndsWhenTheTokenExpires(t *testing.T) {
	t.Parallel()
	revisions := newFakeRevisions(1)
	fake := kclock.NewFake(time.Now())
	srv := boardStreamServer(t, revisions, &ticket.BoardStreamOptions{MaxLifetime: time.Minute, PerUser: 1, Clock: fake})
	user := uuid.New()

	first := openBoardStream(t, srv, user, "", nil)
	// The poll ticker and the lifetime deadline are both waiting.
	fake.BlockUntil(2)
	fake.Advance(time.Minute)
	first.awaitEnd(t)

	// Its slot came back: another stream for the same user is let in, not
	// told to retry.
	again := openBoardStream(t, srv, user, "", nil)
	assert.Empty(t, again.next(t).retry)
}

func TestBoardStream_CapsAllStreams(t *testing.T) {
	t.Parallel()
	revisions := newFakeRevisions(1)
	srv := boardStreamServer(t, revisions, &ticket.BoardStreamOptions{Total: 1})
	openBoardStream(t, srv, uuid.New(), "", nil).nextData(t)

	refused := openBoardStream(t, srv, uuid.New(), "", nil)

	assert.NotEmpty(t, refused.next(t).retry)
	refused.awaitEnd(t)
}
