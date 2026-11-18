package dbtest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeProbe struct {
	snap    containerSnapshot
	logs    string
	logsErr error
	reaper  string
}

func (f fakeProbe) Reaper(context.Context) string { return f.reaper }

func (f fakeProbe) Snapshot(context.Context) containerSnapshot { return f.snap }

func (f fakeProbe) Logs(context.Context) (io.ReadCloser, error) {
	if f.logsErr != nil {
		return nil, f.logsErr
	}
	return io.NopCloser(strings.NewReader(f.logs)), nil
}

func TestDescribeContainerReportsStateAndLogs(t *testing.T) {
	out := describeContainer(fakeProbe{
		snap: containerSnapshot{Status: "exited", OOMKilled: true, ExitCode: 137, FinishedAt: "2026-01-01T00:00:00Z"},
		logs: "FATAL: terminating\n",
	})
	assert.Contains(t, out, "status=exited running=false")
	assert.Contains(t, out, "oom_killed=true")
	assert.Contains(t, out, "exit_code=137")
	assert.Contains(t, out, "FATAL: terminating")
}

func TestDescribeContainerTruncatesLogsToTail(t *testing.T) {
	out := describeContainer(fakeProbe{logs: strings.Repeat("a", diagnoseLogTail) + "END"})
	assert.Contains(t, out, "END")
	assert.NotContains(t, out, strings.Repeat("a", diagnoseLogTail))
}

func TestDescribeContainerSurvivesInspectAndLogErrors(t *testing.T) {
	out := describeContainer(fakeProbe{
		snap:    containerSnapshot{Err: "no such container"},
		logsErr: errors.New("gone"),
	})
	assert.Contains(t, out, "state unavailable: no such container")
	assert.Contains(t, out, "logs unavailable: gone")
}

func TestDescribeContainerIncludesReaperReport(t *testing.T) {
	out := describeContainer(fakeProbe{reaper: "reaper abc image=testcontainers/ryuk"})
	assert.Contains(t, out, "reaper abc image=testcontainers/ryuk")
}

// Needs the daemon, like every test in this package (TestMain starts the
// container). Asserts the report is built from the live container's labels.
func TestDescribeReaperReportsSessionFromCapturedLabels(t *testing.T) {
	require.NotNil(t, containerLabels, "labels must be captured at start")
	out := describeReaper(context.Background(), containerLabels)
	assert.Contains(t, out, fmt.Sprintf("container session=%q", containerLabels[labelSessionID]))
	if os.Getenv(reapProjectEnv) != "" {
		// CI mode runs without Ryuk (see reapOption). The daemon is shared, so
		// another package's Ryuk may be listed; only ours must be absent.
		assert.NotContains(t, out, "reaper: docker client:", "the daemon listing must succeed")
		assert.NotContains(t, out, "reaper: list:", "the daemon listing must succeed")
		assert.NotContains(t, out, fmt.Sprintf("session=%q image=", containerLabels[labelSessionID]))
		return
	}
	assert.Contains(t, out, "reaper ")
}
