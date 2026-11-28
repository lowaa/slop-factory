package service_test

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-bot/internal/daemon"
	"github.com/lowaa/slop-factory/slop-bot/internal/service"
	"github.com/lowaa/slop-factory/slop-bot/internal/service/servicetest"
	"github.com/lowaa/slop-factory/slop-bot/internal/shutdown"
)

func TestNewRefusesAnUnsupportedOS(t *testing.T) {
	t.Parallel()
	f := newFixture(t, &fixtureOpts{goos: "linux"})
	opts := *f.opts
	opts.GOOS = "windows"
	_, err := service.New(&opts)
	require.ErrorIs(t, err, service.ErrUnsupported)
}

func TestNewNeedsItsDependencies(t *testing.T) {
	t.Parallel()
	_, err := service.New(&service.Options{GOOS: "linux", Runner: servicetest.NewRunner(), Logger: slog.New(slog.DiscardHandler)})
	require.Error(t, err)
}

// The service manager's kill comes only after the daemon's worst-case stop
// (its drain, the drain's grace and the exit wait), as the installed unit
// and plist say it, so a daemon still closing its stores isn't killed.
func TestTheServiceOutwaitsTheDaemonsWholeStop(t *testing.T) {
	t.Parallel()
	assert.Greater(t, service.StopTimeout, shutdown.Worst)
	assert.Equal(t, daemon.StopTimeout, shutdown.Drain, "the daemon's drain is the budget's")
	for goos, setting := range map[string]*regexp.Regexp{
		"linux":  regexp.MustCompile(`(?m)^TimeoutStopSec=(\d+)s$`),
		"darwin": regexp.MustCompile(`<key>ExitTimeOut</key>\s*<integer>(\d+)</integer>`),
	} {
		f := newFixture(t, &fixtureOpts{goos: goos})
		_, err := f.mgr.Install(context.Background())
		require.NoError(t, err)
		m := setting.FindStringSubmatch(readFile(t, f.mgr.UnitPath()))
		require.Len(t, m, 2, "%s: the stop timeout is set", goos)
		secs, err := strconv.Atoi(m[1])
		require.NoError(t, err)
		assert.Greater(t, time.Duration(secs)*time.Second, shutdown.Worst, "%s", goos)
	}
}

func TestBinaryFinderPrefersThePATHNameForTheSameFile(t *testing.T) {
	t.Parallel()
	const cellar, link = "/opt/homebrew/Cellar/slop-bot/1.2/bin/slop-bot", "/opt/homebrew/bin/slop-bot"
	cases := []struct {
		name     string
		lookPath func(string) (string, error)
		same     bool
		want     string
	}{
		{"symlink on PATH", func(string) (string, error) { return link, nil }, true, link},
		{"a different slop-bot on PATH", func(string) (string, error) { return link, nil }, false, cellar},
		{"not on PATH", func(string) (string, error) { return "", errors.New("not found") }, false, cellar},
		{"relative PATH hit", func(string) (string, error) { return "./slop-bot", nil }, true, cellar},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := &service.BinaryFinder{
				Executable: func() (string, error) { return cellar, nil },
				LookPath:   tc.lookPath,
				SameFile:   func(string, string) bool { return tc.same },
			}
			got, err := f.Find()
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestExecRunnerReportsExitCodesAsResults(t *testing.T) {
	t.Parallel()
	res, err := service.ExecRunner{}.Run(context.Background(), "sh", "-c", "echo out; echo err >&2; exit 3")
	require.NoError(t, err)
	assert.Equal(t, &service.Result{Stdout: "out\n", Stderr: "err\n", ExitCode: 3}, res)

	_, err = service.ExecRunner{}.Run(context.Background(), "slop-bot-no-such-command")
	require.Error(t, err)
}

// A command killed by cancellation mid-run exits -1, which must not come
// back as a Result a caller would read as a state. The fifo tells the test
// the command is running before it cancels.
func TestExecRunnerReportsCancellationAsAnError(t *testing.T) {
	t.Parallel()
	fifo := filepath.Join(t.TempDir(), "started")
	require.NoError(t, syscall.Mkfifo(fifo, 0o600))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		f, err := os.Open(fifo) // #nosec G304 -- the test's own fifo
		if err == nil {
			_, _ = f.Read(make([]byte, 1))
			_ = f.Close()
		}
		cancel()
	}()
	_, err := service.ExecRunner{}.Run(ctx, "sh", "-c", `echo x > "$0"; exec sleep 1000`, fifo)
	require.ErrorIs(t, err, context.Canceled)
}
