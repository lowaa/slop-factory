package rpcdriver

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-clock"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness"
)

// resendCodec writes held, a message it was holding, when the harness's
// "resend" line says it may, as pi resends what it refused at a settle.
type resendCodec struct {
	lineCodec
	held *harness.Message
	// resent closes once the resend is queued.
	resent chan struct{}
}

func (c *resendCodec) Decode(line []byte, w *Wire) ([]harness.Event, error) {
	if string(line) != "resend" {
		return nil, nil
	}
	defer close(c.resent)
	return nil, w.WriteMessage([]byte(c.held.Text), c.held)
}

// A message the codec writes of its own accord (a resend), still queued
// when the harness exits, is Undelivered too: the codec let go of it once
// queued, so only the Wire knows it never went.
func TestAMessageACodecResentIsUndeliveredUntilWritten(t *testing.T) {
	t.Parallel()
	sh, err := exec.LookPath("sh")
	require.NoError(t, err)
	held := &harness.Message{Text: strings.Repeat("x", 1<<20), IDs: []int64{7}}
	codec := &resendCodec{held: held, resent: make(chan struct{})}
	c, err := Start(context.Background(), &Options{
		Harness: "test", Driver: "test", Codec: codec,
		// It asks for the resend, then never reads.
		Spec:   &harness.LaunchSpec{Path: sh, Args: []string{"-c", `echo ready; echo resend; exec sleep 1000`}, Env: []string{"PATH=" + os.Getenv("PATH")}, Dir: t.TempDir()},
		Logger: slog.New(slog.DiscardHandler), Clock: clock.Real{}, KillGrace: time.Millisecond,
	})
	require.NoError(t, err)
	t.Cleanup(func() { c.proc.Kill() })
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	select {
	case <-codec.resent:
	case <-ctx.Done():
		require.FailNow(t, "the harness never asked for the resend")
	}
	require.NoError(t, c.Stop(ctx, true))
	for range c.Events() {
	}

	var ids []int64
	for _, m := range c.Undelivered() {
		ids = append(ids, m.IDs...)
	}
	assert.Equal(t, []int64{7}, ids)
}
