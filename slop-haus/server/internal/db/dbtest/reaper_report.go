package dbtest

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/client"
	"github.com/testcontainers/testcontainers-go"
)

const (
	labelSessionID = "org.testcontainers.sessionId"
	labelRyuk      = "org.testcontainers.ryuk"
)

// describeReaper reports the container's session ID and the state and logs
// of the Ryuk containers on the daemon, which may already have exited after
// doing their removals. The labels are captured at start-up (see run):
// by the time this is called the container may be gone, and inspecting it
// would fail. Best-effort: it is only called while explaining a failure, so
// every error becomes text.
func describeReaper(ctx context.Context, labels map[string]string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "container session=%q labels=%v", labels[labelSessionID], labels)

	cli, err := testcontainers.NewDockerClientWithOpts(ctx)
	if err != nil {
		fmt.Fprintf(&b, "\nreaper: docker client: %v", err)
		return b.String()
	}
	defer func() { _ = cli.Close() }()

	// Not filtered by session: only the session in the label above tells
	// which Ryuk was ours, and a mismatch is itself evidence.
	list, err := cli.ContainerList(ctx, client.ContainerListOptions{
		All:     true,
		Filters: make(client.Filters).Add("label", labelRyuk+"=true"),
	})
	if err != nil {
		fmt.Fprintf(&b, "\nreaper: list: %v", err)
		return b.String()
	}
	if len(list.Items) == 0 {
		b.WriteString("\nreaper: no Ryuk container on the daemon")
		return b.String()
	}
	for _, r := range list.Items {
		fmt.Fprintf(&b, "\nreaper %s session=%q image=%s state=%s status=%q",
			r.ID[:12], r.Labels[labelSessionID], r.Image, r.State, r.Status)
		fmt.Fprintf(&b, "\nreaper log tail:\n%s", reaperLogs(ctx, cli, r.ID))
	}
	return b.String()
}

func reaperLogs(ctx context.Context, cli *testcontainers.DockerClient, id string) string {
	rc, err := cli.ContainerLogs(ctx, id, client.ContainerLogsOptions{ShowStdout: true, ShowStderr: true})
	if err != nil {
		return fmt.Sprintf("unavailable: %v", err)
	}
	defer func() { _ = rc.Close() }()
	var out bytes.Buffer
	// Ryuk's container has no TTY, so the stream is multiplexed.
	_, err = stdcopy.StdCopy(&out, &out, io.LimitReader(rc, diagnoseLogRead))
	logs := out.Bytes()
	if err != nil && len(logs) == 0 {
		return fmt.Sprintf("unreadable: %v", err)
	}
	if len(logs) > diagnoseLogTail {
		logs = logs[len(logs)-diagnoseLogTail:]
	}
	return strings.TrimRight(string(logs), "\n")
}
