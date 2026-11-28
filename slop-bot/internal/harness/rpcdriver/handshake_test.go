package rpcdriver

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/go-clock"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness"
)

// failAfterOneLineCodec's handshake reads the first line and then fails. It
// keeps next, for the test to read what is left after Start.
type failAfterOneLineCodec struct {
	readyCodec
	next func(context.Context) ([]byte, error)
}

func (f *failAfterOneLineCodec) Open(ctx context.Context, _ *Wire, next func(context.Context) ([]byte, error)) (string, error) {
	f.next = next
	if _, err := next(ctx); err != nil {
		return "", err
	}
	return "", errors.New("no thanks")
}

// A failed handshake must drain what the harness had already written so the
// reader goroutine can finish, and report the handshake failure.
func TestStartAFailedHandshakeDrainsTheLinesStillComing(t *testing.T) {
	t.Parallel()
	sh, err := exec.LookPath("sh")
	require.NoError(t, err)
	// One write, so the second line is in the pipe before the first is seen.
	codec := &failAfterOneLineCodec{}
	c, err := Start(context.Background(), &Options{
		Harness: "test", Driver: "test", Codec: codec,
		Spec:   &harness.LaunchSpec{Path: sh, Args: []string{"-c", `printf 'a\nb\n'; read x`}, Env: []string{"PATH=" + os.Getenv("PATH")}, Dir: t.TempDir()},
		Logger: slog.New(slog.DiscardHandler), Clock: clock.NewFake(time.Unix(0, 0)), KillGrace: time.Minute,
	})
	require.Nil(t, c)
	require.ErrorContains(t, err, "test handshake: no thanks")
	// The second line was drained, and the reader finished: nothing is left.
	line, err := codec.next(context.Background())
	assert.Nil(t, line)
	assert.EqualError(t, err, "the harness exited during the handshake")
}

func TestNextLineGivesUpWhenTheContextIsDone(t *testing.T) {
	t.Parallel()
	c := &Conversation{lines: make(chan []byte)}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	line, err := c.nextLine(ctx)
	assert.Nil(t, line)
	require.ErrorIs(t, err, context.Canceled)
}
