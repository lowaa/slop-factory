package cli

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-bot/internal/api"
)

// `inbox --follow` reads the inbox once on connecting and again for each
// event that says a message arrived, and for no other event.
func TestInboxFollowRereadsOnlyForEventsAboutMessages(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	require.NoError(t, os.MkdirAll(filepath.Join(h.runtime, "slop-bot"), 0o700))
	l, err := net.Listen("unix", filepath.Join(h.runtime, "slop-bot", "slop-bot.sock"))
	require.NoError(t, err)

	var reads atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc(api.PathInbox, func(w http.ResponseWriter, _ *http.Request) {
		reads.Add(1)
		_, _ = w.Write([]byte("[]"))
	})
	mux.HandleFunc(api.PathEvents, func(w http.ResponseWriter, _ *http.Request) {
		// One event of each kind that matters, among ones that don't; the
		// stream then ends, which ends the follow.
		for _, typ := range []string{"agent.spawned", "message.queued", "ticket.updated", "user.told", "agent.asked"} {
			_, _ = w.Write([]byte(`{"type":"` + typ + `"}` + "\n"))
		}
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: time.Minute}
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(func() { _ = srv.Close() })

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	cmd.SetOut(h.out)
	a := &app{deps: h.deps}

	require.NoError(t, a.followInbox(cmd, false))

	assert.EqualValues(t, 4, reads.Load(), "the first read, then one per message event, none for the others")
}
