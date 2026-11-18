package dbtest

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"
)

const (
	diagnoseTimeout = 15 * time.Second
	// Enough to show why Postgres went away (OOM, shutdown request, crash)
	// without flooding the test output.
	diagnoseLogTail = 4096
	diagnoseLogRead = 1 << 20
)

// containerSnapshot is what we ask Docker about a container that has stopped
// answering. Kept free of testcontainers/docker types so the report can be
// tested without a daemon.
type containerSnapshot struct {
	Status     string
	Running    bool
	OOMKilled  bool
	ExitCode   int
	FinishedAt string
	Err        string
}

// containerProbe reads a container's state and log tail; the real one wraps
// the postgres container, tests supply a fake.
type containerProbe interface {
	Snapshot(ctx context.Context) containerSnapshot
	Logs(ctx context.Context) (io.ReadCloser, error)
	// Reaper reports the container's testcontainers session and what that
	// session's Ryuk reaper did: Ryuk force-removes a session's containers
	// when its connection to the test process stalls, which looks exactly
	// like an exit_code=137 kill.
	Reaper(ctx context.Context) string
}

// describeContainer explains why a refused connection might have happened.
// It is used on failure paths only (CI flake: the container vanished
// mid-run), so it is best-effort and never fails.
func describeContainer(p containerProbe) string {
	ctx, cancel := context.WithTimeout(context.Background(), diagnoseTimeout)
	defer cancel()

	var b strings.Builder
	s := p.Snapshot(ctx)
	if s.Err != "" {
		fmt.Fprintf(&b, "container state unavailable: %s", s.Err)
	} else {
		fmt.Fprintf(&b, "container state: status=%s running=%t oom_killed=%t exit_code=%d finished_at=%s",
			s.Status, s.Running, s.OOMKilled, s.ExitCode, s.FinishedAt)
	}

	fmt.Fprintf(&b, "\n%s", p.Reaper(ctx))

	rc, err := p.Logs(ctx)
	if err != nil {
		fmt.Fprintf(&b, "\ncontainer logs unavailable: %v", err)
		return b.String()
	}
	defer func() { _ = rc.Close() }()
	// Bounded: the tail is what matters, and a runaway log must not stall a failing test.
	logs, err := io.ReadAll(io.LimitReader(rc, diagnoseLogRead))
	if err != nil && len(logs) == 0 {
		fmt.Fprintf(&b, "\ncontainer logs unreadable: %v", err)
		return b.String()
	}
	if len(logs) > diagnoseLogTail {
		logs = logs[len(logs)-diagnoseLogTail:]
	}
	fmt.Fprintf(&b, "\ncontainer log tail:\n%s", strings.TrimRight(string(logs), "\n"))
	return b.String()
}

// pgProbe adapts the shared postgres container to containerProbe.
type pgProbe struct {
	logs func(context.Context) (io.ReadCloser, error)
	snap func(context.Context) containerSnapshot
	reap func(context.Context) string
}

func (p pgProbe) Snapshot(ctx context.Context) containerSnapshot { return p.snap(ctx) }

func (p pgProbe) Logs(ctx context.Context) (io.ReadCloser, error) { return p.logs(ctx) }

func (p pgProbe) Reaper(ctx context.Context) string { return p.reap(ctx) }

func sharedContainerProbe() containerProbe {
	c := container
	return pgProbe{
		logs: c.Logs,
		reap: func(ctx context.Context) string { return describeReaper(ctx, containerLabels) },
		snap: func(ctx context.Context) containerSnapshot {
			st, err := c.State(ctx)
			if err != nil {
				return containerSnapshot{Err: err.Error()}
			}
			return containerSnapshot{
				Status:     fmt.Sprint(st.Status),
				Running:    st.Running,
				OOMKilled:  st.OOMKilled,
				ExitCode:   st.ExitCode,
				FinishedAt: st.FinishedAt,
			}
		},
	}
}
