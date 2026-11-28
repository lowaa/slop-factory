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

	require.NoError(t, c.kill(context.Background()))

	assert.Equal(t, 7, lastEvent(c).ExitCode, "the process ran its SIGTERM handler; it wasn't SIGKILLed at once")
}

func TestKillForcesAProcessThatIgnoresSIGTERMAfterTheGrace(t *testing.T) {
	t.Parallel()
	fake := clock.NewFake(time.Unix(0, 0))
	c := startScript(t, `trap '' TERM; echo ready; read x`, fake)

	done := make(chan struct{})
	go func() { defer close(done); _ = c.kill(context.Background()) }()
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

// A Stop whose context has ended doesn't give SIGTERM its grace: the
// caller's deadline has passed, so the group is killed at once. The fake
// clock never moves, so waiting out the grace would hang.
func TestStopWithAnEndedContextKillsWithoutTheGrace(t *testing.T) {
	t.Parallel()
	c := startScript(t, `trap '' TERM; echo ready; exec sleep 1000`, clock.NewFake(time.Unix(0, 0)))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	require.NoError(t, c.Stop(ctx, false))

	assert.Equal(t, 137, lastEvent(c).ExitCode)
}

// textCodec makes a Text event of every line after the handshake.
type textCodec struct{ readyCodec }

func (textCodec) Decode(line []byte, _ *Wire) ([]harness.Event, error) {
	return []harness.Event{{Kind: harness.Text, Text: string(line)}}, nil
}

// A consumer that isn't reading Events can't hold up a Stop: more events
// than any buffer, and nobody reading them, and Stop still returns once the
// process has gone. Then the consumer gets every event, Exited last.
func TestStopDoesNotWaitForTheConsumer(t *testing.T) {
	t.Parallel()
	const lines = 500
	sh, err := exec.LookPath("sh")
	require.NoError(t, err)
	c, err := Start(context.Background(), &Options{
		Harness: "test", Driver: "test", Codec: textCodec{},
		Spec: &harness.LaunchSpec{Path: sh, Args: []string{"-c", `echo ready; i=0; while [ $i -lt 500 ]; do echo line; i=$((i+1)); done`},
			Env: []string{"PATH=" + os.Getenv("PATH")}, Dir: t.TempDir()},
		Logger: slog.New(slog.DiscardHandler), Clock: clock.Real{},
	})
	require.NoError(t, err)
	t.Cleanup(func() { c.proc.Kill() })
	<-c.proc.Done()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	stopped := make(chan error, 1)
	go func() { stopped <- c.Stop(ctx, true) }()
	select {
	case err := <-stopped:
		require.NoError(t, err)
	case <-ctx.Done():
		require.FailNow(t, "Stop waited for a consumer that isn't reading")
	}

	var kinds []harness.EventKind
	for ev := range c.Events() {
		kinds = append(kinds, ev.Kind)
	}
	require.Len(t, kinds, lines+2)
	assert.Equal(t, harness.Ready, kinds[0])
	assert.Equal(t, harness.Exited, kinds[len(kinds)-1])
}
