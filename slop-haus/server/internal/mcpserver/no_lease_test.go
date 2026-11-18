package mcpserver_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// wrappedNoLease is ErrNoLease under a message that doesn't repeat it:
// a missing lease is recognised by identity, not by what the error says.
var wrappedNoLease error = &renamedError{msg: "ticket: lease lookup came back empty", err: ticket.ErrNoLease}

type renamedError struct {
	msg string
	err error
}

func (e *renamedError) Error() string { return e.msg }
func (e *renamedError) Unwrap() error { return e.err }

// logBuffer collects log output written on the server's goroutines.
type logBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *logBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *logBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func newLoggedRig(t *testing.T) (*rig, *logBuffer) {
	t.Helper()
	logs := &logBuffer{}
	return newRigWith(t, &rigOptions{logs: slog.NewTextHandler(logs, nil)}), logs
}

func callCheckin(t *testing.T, r *rig) {
	t.Helper()
	res, err := r.session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "checkin",
	})
	require.NoError(t, err)
	require.False(t, res.IsError, resultText(t, res))
}

// An idle agent's heartbeat finds no lease on every call; that is nothing
// to revoke and nothing to warn about.
func TestHeartbeat_NoLeaseIsNotLogged(t *testing.T) {
	r, logs := newLoggedRig(t)
	r.tickets.heartbeatErr = wrappedNoLease

	callCheckin(t, r)

	assert.NotContains(t, logs.String(), "heartbeat lease")
}

func TestHeartbeat_AnyOtherFailureIsLogged(t *testing.T) {
	r, logs := newLoggedRig(t)
	r.tickets.heartbeatErr = errors.New("database unreachable")

	callCheckin(t, r)

	assert.Contains(t, logs.String(), "heartbeat lease")
	assert.Contains(t, logs.String(), "database unreachable")
}

func TestLeaseLost_AWrappedNoLeaseStillLooksForTheNotice(t *testing.T) {
	r := newRig(t)
	r.tickets.lease = &ticket.Lease{TicketID: 12}
	r.tickets.heartbeatErrs = []error{nil, &ticket.LeaseRevokedError{TicketID: 12, Status: ticket.StatusCancelled}}
	r.tickets.doneErr = wrappedNoLease

	res, err := r.session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "ticket_finish", Arguments: map[string]any{"pr_url": testPR, "handoff": "finished"},
	})
	require.NoError(t, err)
	assert.True(t, res.IsError)
	assert.Contains(t, resultText(t, res), "#12 was cancelled by a human")
}

// Only a missing lease can be explained by a revocation. Any other failure
// is the agent's to see as it is, and must not be swapped for a notice.
func TestLeaseLost_AnyOtherFailureIsReportedAsItself(t *testing.T) {
	r := newRig(t)
	r.tickets.lease = &ticket.Lease{TicketID: 12}
	r.tickets.heartbeatErrs = []error{nil, &ticket.LeaseRevokedError{TicketID: 12, Status: ticket.StatusCancelled}}
	r.tickets.doneErr = ticket.ErrInvalidPRURL

	res, err := r.session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "ticket_finish", Arguments: map[string]any{"handoff": "finished", "pr_url": "not a url"},
	})
	require.NoError(t, err)
	assert.True(t, res.IsError)
	assert.Contains(t, resultText(t, res), "pr_url must be an https URL", "the package prefix is dropped")
	assert.NotContains(t, resultText(t, res), "ticket:")
	assert.NotContains(t, resultText(t, res), "cancelled by a human")
	assert.Equal(t, 1, r.tickets.heartbeats, "only caller's heartbeat; the failure was not a lost lease")
}
