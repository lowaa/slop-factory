package conductor_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/conductor"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/wakeup"
)

// tickCounter is a real clock that counts its ticks, so a test can wait for
// "the poll has been waiting through this many intervals" without sleeping.
type tickCounter struct {
	ticks chan struct{}
	seen  atomic.Int64
}

func (c *tickCounter) Ticker(d time.Duration) (<-chan time.Time, func()) {
	out, stop := wakeup.RealClock{}.Ticker(d)
	fwd := make(chan time.Time)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case t := <-out:
				c.seen.Add(1)
				select {
				case c.ticks <- struct{}{}:
				default:
				}
				select {
				case fwd <- t:
				case <-done:
					return
				}
			case <-done:
				return
			}
		}
	}()
	return fwd, func() { stop(); close(done) }
}

func (c *tickCounter) After(d time.Duration) <-chan time.Time { return wakeup.RealClock{}.After(d) }

// The server's write timeout (10s in production) would cut a poll held open
// longer; the route lifts it for the wait. Here the timeout is shorter than
// the wait the poll sits through.
func TestALongPollOutlivesTheServersWriteTimeout(t *testing.T) {
	f := newFixture(t)
	owned := f.owned(t, f.owner, "c1")
	clock := &tickCounter{ticks: make(chan struct{}, 1024)}
	api := f.replica(t, &conductor.Options{Clock: clock, PollInterval: 10 * time.Millisecond}, nil)
	api.agents["host"] = owned.Agent()
	api.users["owner"] = f.owner
	// A server of its own: this is about the connection, so it is served as
	// production does, with a write timeout the poll must outlast.
	srv := httptest.NewUnstartedServer(api.mux)
	srv.Config.WriteTimeout = 100 * time.Millisecond
	srv.Start()
	t.Cleanup(srv.Close)

	type answer struct {
		body string
		err  error
	}
	got := make(chan answer, 1)
	go func() {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/conductor/poll",
			strings.NewReader(`{"host_id":"`+uuid.NewString()+`","wait_seconds":30}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer host")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			got <- answer{err: err}
			return
		}
		defer func() { _ = resp.Body.Close() }()
		raw, err := io.ReadAll(resp.Body)
		got <- answer{body: string(raw), err: err}
	}()

	// Well past the write timeout, in ticks of the poll's own wait.
	for range 30 {
		select {
		case <-clock.ticks:
		case <-time.After(10 * time.Second):
			t.Fatal("the poll is not waiting")
		}
	}
	f.start(t, owned)

	select {
	case a := <-got:
		require.NoError(t, a.err, "the connection was cut by the write timeout")
		assert.Contains(t, a.body, `"reason":"work"`)
	case <-time.After(10 * time.Second):
		t.Fatal("the poll did not return")
	}
}
