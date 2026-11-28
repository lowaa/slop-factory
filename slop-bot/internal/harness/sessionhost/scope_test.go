package sessionhost_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-bot/internal/harness/sessionhost"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/sessionhost/tmuxtest"
)

// The test binary, re-run with serviceEnv set, is a stand-in daemon: it
// starts a session as the daemon's host does, says so on the fifo, and
// waits for its service manager's SIGTERM.
const (
	serviceEnv = "SLOP_BOT_SCOPE_SERVICE"
	socketEnv  = "SLOP_BOT_SCOPE_SOCKET"
	fifoEnv    = "SLOP_BOT_SCOPE_FIFO"
	scopedEnv  = "SLOP_BOT_SCOPE_SCOPED"
)

func TestMain(m *testing.M) {
	if os.Getenv(serviceEnv) == "1" {
		os.Exit(standInDaemon())
	}
	os.Exit(m.Run())
}

func standInDaemon() int {
	term := make(chan os.Signal, 1)
	signal.Notify(term, syscall.SIGTERM)
	socket := os.Getenv(socketEnv)
	opts := &sessionhost.TmuxOptions{Socket: socket}
	if os.Getenv(scopedEnv) == "1" {
		opts.Scope = sessionhost.SystemdScope(socket)
		if opts.Scope == nil {
			fmt.Fprintln(os.Stderr, "not under a systemd user manager")
			return 1
		}
	}
	host, err := sessionhost.NewTmux(opts)
	if err == nil {
		err = host.Start(context.Background(), &sessionhost.StartSpec{Name: "s1", Argv: []string{"sleep", "600"}})
	}
	fifo, ferr := os.OpenFile(os.Getenv(fifoEnv), os.O_WRONLY, 0)
	if ferr != nil {
		return 1
	}
	if err != nil {
		_, _ = fmt.Fprintln(fifo, "error:", err)
		return 1
	}
	_, _ = fmt.Fprintln(fifo, "ready")
	_ = fifo.Close()
	<-term
	return 0
}

// TestSessionsOutliveTheDaemonsService runs a stand-in daemon as a real
// systemd user service with the unit's KillMode=mixed, stops the service,
// and checks the session it started is still there: only with the scope.
func TestSessionsOutliveTheDaemonsService(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux is not installed")
	}
	if err := exec.Command("systemctl", "--user", "show-environment").Run(); err != nil {
		t.Skip("no systemd user manager here")
	}
	for name, scoped := range map[string]bool{"scoped": true, "unscoped": false} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx := ctxFor(t)
			b := make([]byte, 6)
			_, err := rand.Read(b)
			require.NoError(t, err)
			id := hex.EncodeToString(b)
			socket, unit := "sbt-"+id, "sbt-scope-"+id+".service"
			// The server is this test's, by its socket name, whatever the
			// service did.
			host := tmuxtest.Host(t, &sessionhost.TmuxOptions{Socket: socket})
			fifo := filepath.Join(t.TempDir(), "ready")
			require.NoError(t, syscall.Mkfifo(fifo, 0o600))
			self, err := os.Executable()
			require.NoError(t, err)
			tmux, err := exec.LookPath("tmux")
			require.NoError(t, err)
			flag := "0"
			if scoped {
				flag = "1"
			}
			out, err := exec.CommandContext(ctx, "systemd-run", "--user", "--quiet", "--collect", "--unit="+unit, // #nosec G204 -- the test's own binary and unit
				"--property=KillMode=mixed",
				"--setenv="+serviceEnv+"=1", "--setenv="+socketEnv+"="+socket, "--setenv="+fifoEnv+"="+fifo,
				"--setenv="+scopedEnv+"="+flag, "--setenv=PATH="+filepath.Dir(tmux)+":/usr/bin:/bin",
				self).CombinedOutput()
			require.NoError(t, err, string(out))
			t.Cleanup(func() { _ = exec.Command("systemctl", "--user", "stop", unit).Run() }) // #nosec G204 -- the test's own unit

			ready := make(chan string, 1)
			go func() {
				f, err := os.Open(fifo) // #nosec G304 -- the test's own fifo
				if err != nil {
					ready <- err.Error()
					return
				}
				b, _ := io.ReadAll(f)
				_ = f.Close()
				ready <- string(b)
			}()
			select {
			case got := <-ready:
				require.Equal(t, "ready\n", got)
			case <-ctx.Done():
				require.FailNow(t, "the stand-in daemon never started", "journalctl --user -u %s", unit)
			}

			// What `systemctl --user stop slop-bot` does to the daemon.
			out, err = exec.CommandContext(ctx, "systemctl", "--user", "stop", unit).CombinedOutput() // #nosec G204 -- the test's own unit
			require.NoError(t, err, string(out))
			names, err := host.List(ctx)
			require.NoError(t, err)
			if scoped {
				assert.Equal(t, []string{"s1"}, names, "the session outlives the service")
			} else {
				assert.Empty(t, names, "without the scope, the service's stop ends the server")
			}
		})
	}
}
