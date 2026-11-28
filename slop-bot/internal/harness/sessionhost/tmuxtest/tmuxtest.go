// Package tmuxtest gives each test its own real tmux server.
package tmuxtest

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"log/slog"
	"os/exec"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-bot/internal/harness/sessionhost"
)

// New is a Tmux host on a server of its own, killed when the test ends.
func New(t *testing.T) *sessionhost.Tmux {
	t.Helper()
	// Short, so the occasional lost wakeup costs a test little.
	return Host(t, &sessionhost.TmuxOptions{ExitRecheck: 200 * time.Millisecond})
}

// Host is a Tmux host with opts; a blank Socket is a fresh server of the
// test's own. The server is killed when the test ends.
func Host(t *testing.T, opts *sessionhost.TmuxOptions) *sessionhost.Tmux {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux is not installed")
	}
	o := *opts
	if o.Socket == "" {
		b := make([]byte, 6)
		_, err := rand.Read(b)
		require.NoError(t, err)
		o.Socket = "sbt-" + hex.EncodeToString(b)
	}
	if o.Logger == nil {
		o.Logger = slog.New(slog.DiscardHandler)
	}
	host, err := sessionhost.NewTmux(&o)
	require.NoError(t, err)
	t.Cleanup(func() { _ = host.KillServer(context.Background()) })
	return host
}

// Attach joins a read-only client to session, as a person watching would,
// and returns once tmux counts it attached; detach ends it. It is a
// control-mode client, which needs no terminal, run from the host's own
// attach command.
func Attach(ctx context.Context, t *testing.T, host *sessionhost.Tmux, session string) (detach func()) {
	t.Helper()
	argv := host.AttachCommand(session)
	at := slices.Index(argv, "attach-session")
	require.Positive(t, at)
	// -C, before the command: control mode. -r: read-only.
	args := append(append(append([]string{}, argv[1:at]...), "-C"), argv[at:]...)
	args = append(args, "-r")
	cmd := exec.CommandContext(ctx, argv[0], args...) // #nosec G204 -- the test's own tmux
	stdin, err := cmd.StdinPipe()
	require.NoError(t, err)
	stdout, err := cmd.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, cmd.Start())
	// Control mode writes its first notification once attached.
	_, err = bufio.NewReader(stdout).ReadString('\n')
	require.NoError(t, err)
	done := false
	detach = func() {
		if done {
			return
		}
		done = true
		// End of input detaches a control-mode client.
		_ = stdin.Close()
		_, _ = io.Copy(io.Discard, stdout)
		_ = cmd.Wait()
	}
	t.Cleanup(detach)
	return detach
}
