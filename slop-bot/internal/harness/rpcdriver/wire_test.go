package rpcdriver

import (
	"context"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-clock"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness"
)

// lineCodec sends each message's text as a line.
type lineCodec struct{ readyCodec }

func (lineCodec) Send(_ context.Context, w *Wire, msg *harness.Message, _ bool) error {
	return w.WriteMessage([]byte(msg.Text), msg)
}

// A Send never waits on the harness reading its stdin: one that did held a
// codec's lock (Codex's) across the write, so Decode stopped, stdout
// filled, and a harness blocked writing it never read stdin again. Here
// the harness never reads, and the message is far past a pipe's buffer.
func TestSendDoesNotWaitForTheHarnessToRead(t *testing.T) {
	t.Parallel()
	sh, err := exec.LookPath("sh")
	require.NoError(t, err)
	c, err := Start(context.Background(), &Options{
		Harness: "test", Driver: "test", Codec: lineCodec{},
		Spec:   &harness.LaunchSpec{Path: sh, Args: []string{"-c", `echo ready; exec sleep 1000`}, Env: []string{"PATH=" + os.Getenv("PATH")}, Dir: t.TempDir()},
		Logger: slog.New(slog.DiscardHandler), Clock: clock.Real{}, KillGrace: time.Millisecond,
	})
	require.NoError(t, err)
	t.Cleanup(func() { c.proc.Kill() })

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	sent := make(chan error, 1)
	go func() { sent <- c.Send(ctx, &harness.Message{Text: strings.Repeat("x", 1<<20)}) }()
	select {
	case err := <-sent:
		require.NoError(t, err)
	case <-ctx.Done():
		require.FailNow(t, "Send waited for the harness to read")
	}
	require.NoError(t, c.Stop(ctx, true))
}

// A message Send queued but the harness never got (it died with the line
// still waiting for its stdin) is Undelivered: the caller has it marked
// delivered, and queues it again. One written whole is not.
func TestAMessageStillQueuedAtTheExitIsUndelivered(t *testing.T) {
	t.Parallel()
	sh, err := exec.LookPath("sh")
	require.NoError(t, err)
	c, err := Start(context.Background(), &Options{
		Harness: "test", Driver: "test", Codec: lineCodec{},
		// It reads one line, then stops reading.
		Spec:   &harness.LaunchSpec{Path: sh, Args: []string{"-c", `echo ready; read first; echo got; exec sleep 1000`}, Env: []string{"PATH=" + os.Getenv("PATH")}, Dir: t.TempDir()},
		Logger: slog.New(slog.DiscardHandler), Clock: clock.Real{}, KillGrace: time.Millisecond,
	})
	require.NoError(t, err)
	t.Cleanup(func() { c.proc.Kill() })
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	read := &harness.Message{Text: "first", IDs: []int64{1}}
	require.NoError(t, c.Send(ctx, read))
	// Far past a pipe's buffer: it can't all be written to a harness that
	// has stopped reading.
	lost := &harness.Message{Text: strings.Repeat("x", 1<<20), IDs: []int64{2}}
	require.NoError(t, c.Send(ctx, lost))
	require.NoError(t, c.Stop(ctx, true))
	for range c.Events() {
	}

	var ids []int64
	for _, m := range c.Undelivered() {
		ids = append(ids, m.IDs...)
	}
	assert.Equal(t, []int64{2}, ids)
}

func TestAQueuedWireWritesInOrderThenClosesOnClose(t *testing.T) {
	t.Parallel()
	r, wc := io.Pipe()
	w := newQueuedWire(wc)
	for i := range 3 {
		require.NoError(t, w.WriteLine([]byte(strconv.Itoa(i))))
	}
	w.close()
	assert.ErrorIs(t, w.WriteLine([]byte("late")), errWireClosed)

	got, err := io.ReadAll(r)
	require.NoError(t, err, "the pipe closes once the queue is written")
	assert.Equal(t, "0\n1\n2\n", string(got))
}

func TestAWireNobodyReadsFillsThenRefuses(t *testing.T) {
	t.Parallel()
	r, wc := io.Pipe()
	w := newQueuedWire(wc)
	t.Cleanup(w.abort)
	// The writer holds one line, blocked on the pipe; the queue fills
	// behind it.
	var err error
	for i := 0; i <= maxQueued+1 && err == nil; i++ {
		err = w.WriteLine([]byte("x"))
	}
	assert.ErrorIs(t, err, errWireFull)
	_ = r.Close()
}

func TestABrokenWireReportsTheWriteError(t *testing.T) {
	t.Parallel()
	r, wc := io.Pipe()
	_ = r.CloseWithError(io.ErrClosedPipe)
	w := newQueuedWire(wc)
	require.NoError(t, w.WriteLine([]byte("lost")), "queued before the writer finds the pipe broken")
	w.mu.Lock()
	for w.err == nil {
		w.cond.Wait()
	}
	w.mu.Unlock()
	assert.ErrorIs(t, w.WriteLine([]byte("next")), io.ErrClosedPipe)
}
