package rpcdriver

import (
	"context"
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

// readyCodec's handshake is the first stdout line, which the test scripts
// print once their signal handling is installed.
type readyCodec struct{}

func (readyCodec) Open(ctx context.Context, _ *Wire, next func(context.Context) ([]byte, error)) (string, error) {
	_, err := next(ctx)
	return "id", err
}
func (readyCodec) Send(context.Context, *Wire, *harness.Message, bool) error { return nil }
func (readyCodec) Interrupt(context.Context, *Wire) error                    { return nil }
func (readyCodec) Decode([]byte, *Wire) ([]harness.Event, error)             { return nil, nil }

func startScript(t *testing.T, script string, fake *clock.Fake) *Conversation {
	t.Helper()
	sh, err := exec.LookPath("sh")
	require.NoError(t, err)
	c, err := Start(context.Background(), &Options{
		Harness: "test", Driver: "test", Codec: readyCodec{},
		Spec:   &harness.LaunchSpec{Path: sh, Args: []string{"-c", script}, Env: []string{"PATH=" + os.Getenv("PATH")}, Dir: t.TempDir()},
		Logger: slog.New(slog.DiscardHandler), Clock: fake, KillGrace: time.Minute,
	})
	require.NoError(t, err)
	t.Cleanup(func() { c.proc.Kill() })
	return c
}

func lastEvent(c *Conversation) harness.Event {
	var last harness.Event
	for ev := range c.Events() {
		last = ev
	}
	return last
}

func TestKillLetsAProcessExitOnSIGTERMBeforeForcing(t *testing.T) {
	t.Parallel()
	c := startScript(t, `trap 'exit 7' TERM; echo ready; read x`, clock.NewFake(time.Unix(0, 0)))

	require.NoError(t, c.kill())

	assert.Equal(t, 7, lastEvent(c).ExitCode, "the process ran its SIGTERM handler; it wasn't SIGKILLed at once")
}

func TestKillForcesAProcessThatIgnoresSIGTERMAfterTheGrace(t *testing.T) {
	t.Parallel()
	fake := clock.NewFake(time.Unix(0, 0))
	c := startScript(t, `trap '' TERM; echo ready; read x`, fake)

	done := make(chan struct{})
	go func() { defer close(done); _ = c.kill() }()
	fake.BlockUntil(1)
	select {
	case <-done:
		t.Fatal("killed before the grace period passed")
	default:
	}
	fake.Advance(time.Minute)
	<-done

	assert.Equal(t, 137, lastEvent(c).ExitCode)
}
