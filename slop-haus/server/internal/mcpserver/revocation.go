package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/agentmsg"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/usagemetrics"
)

// leaseNeed is what a tool needs of the ticket the agent holds. It decides
// how the tool answers the call that delivers a revocation notice -- a
// human having taken that ticket away (ticket.LeaseRevokedError).
//
// Every tool is registered with one (see addTool), and the zero value is
// refused, so a new tool cannot end up in neither camp by omission.
type leaseNeed int

const (
	_ leaseNeed = iota
	// leaseHeld tools act on the held ticket or its lease. After a
	// revocation there is nothing for them to act on, so the notice is
	// their answer: the call fails with it and does nothing else.
	leaseHeld
	// leaseNamed tools act on the ticket their input names (namedTicketer).
	// They fail with the notice only when that is the ticket taken away --
	// a comment on the withdrawn ticket is the agent still working it.
	// This routes the notice; it is not an authorization check. These tools
	// never needed a lease (an agent may comment on any ticket), so once the
	// notice has been delivered, a later call on that ticket goes through.
	leaseNamed
	// leaseFree tools need no held ticket. They run as asked and the notice
	// rides on their result, so the agent loses no call to it and can't
	// mistake it for slop-haus being broken.
	leaseFree
)

// namedTicketer is implemented by the input of every leaseNamed tool.
type namedTicketer interface {
	namedTicket() int64
}

// toolFunc is a tool's own work, after the shared preamble in addTool has
// resolved the caller.
type toolFunc[In, Out any] func(ctx context.Context, agentID uuid.UUID, in In) (*mcp.CallToolResult, Out, error)

// addTool registers a tool behind the preamble every tool shares: resolve
// the caller, heartbeat, hand over what the human has said to the agent, and
// handle a revocation notice according to need.
func addTool[In, Out any](server *mcp.Server, h *Handler, tool *mcp.Tool, need leaseNeed, run toolFunc[In, Out]) {
	switch need {
	case leaseHeld, leaseFree:
	case leaseNamed:
		var in In
		if _, ok := any(in).(namedTicketer); !ok {
			panic(fmt.Sprintf("mcpserver: tool %q is leaseNamed but its input names no ticket", tool.Name))
		}
	default:
		panic(fmt.Sprintf("mcpserver: tool %q has no lease need", tool.Name))
	}

	var probe Out
	if _, ok := any(&probe).(noteCarrier); !ok {
		panic(fmt.Sprintf("mcpserver: tool %q has an output that does not embed toolNotes", tool.Name))
	}

	if h.toolUsage != nil {
		h.toolUsage.Known(tool.Name)
	}

	mcp.AddTool(server, tool, func(ctx context.Context, _ *mcp.CallToolRequest, in In) (*mcp.CallToolResult, Out, error) {
		var zero Out
		// Error until the call ends well; see toolUsageGuard.
		setOutcome(ctx, usagemetrics.OutcomeError)
		agentID, notice, err := h.caller(ctx)
		if err != nil {
			return nil, zero, err
		}
		if err := h.refuseSecrets(ctx, agentID, tool.Name, in); err != nil {
			setOutcome(ctx, usagemetrics.OutcomeRefused)
			return nil, zero, err
		}
		heard := h.humanWords(ctx, agentID)
		ctx = context.WithValue(ctx, noticeKey{}, notice != nil)
		if notice == nil && heard == "" {
			res, out, err := run(ctx, agentID, in)
			if err != nil {
				return nil, zero, h.failureOf(ctx, agentID, err)
			}
			setOutcome(ctx, usagemetrics.OutcomeOK)
			return res, out, nil
		}
		if notice != nil && need.failsOn(in, notice) {
			if heard == "" {
				return nil, zero, notice
			}
			return nil, zero, errors.New(joinNotices(heard, notice))
		}

		res, out, err := run(ctx, agentID, in)
		if err != nil {
			// The notices are already marked delivered, so they have to reach
			// the agent on this failure too. Flattened into the message
			// rather than wrapped: the SDK sends some wrapped error types as
			// protocol errors, which a client may not show the model at all.
			return nil, zero, fmt.Errorf("%s\n\nSeparately, this call failed: %s", joinNotices(heard, notice), h.failure(ctx, agentID, err))
		}
		noticeText := ""
		if notice != nil {
			noticeText = notice.Error()
		}
		any(&out).(noteCarrier).carry(heard, noticeText)
		setOutcome(ctx, usagemetrics.OutcomeOK)
		return withNotices(res, heard, notice), out, nil
	})
}

// joinNotices is what the agent reads in place of a result: the human's
// words first, then the revocation notice, either of which may be absent.
func joinNotices(heard string, notice *ticket.LeaseRevokedError) string {
	var parts []string
	if heard != "" {
		parts = append(parts, heard)
	}
	if notice != nil {
		parts = append(parts, notice.Error())
	}
	return strings.Join(parts, "\n\n")
}

func (n leaseNeed) failsOn(in any, notice *ticket.LeaseRevokedError) bool {
	switch n {
	case leaseHeld:
		return true
	case leaseNamed:
		return in.(namedTicketer).namedTicket() == notice.TicketID
	default:
		return false
	}
}

// withNotices puts what the agent must be told ahead of a successful
// result. First, and as text: a line after the result is one the model may
// skim past while acting on the result. They are also fields of the
// structured output (toolNotes), which is what Claude Code shows in place of
// the text. Each is a block of its own,
// so the human's words are never run into a tool's output or the notice.
func withNotices(res *mcp.CallToolResult, heard string, notice *ticket.LeaseRevokedError) *mcp.CallToolResult {
	if res == nil {
		res = &mcp.CallToolResult{}
	}
	var lead []mcp.Content
	if heard != "" {
		lead = append(lead, &mcp.TextContent{Text: heard +
			"\n\nThe call you made still went through; its result follows."})
	}
	if notice != nil {
		lead = append(lead, &mcp.TextContent{Text: "NOTICE: " + notice.Error() +
			"\n\nThe call you made still went through; its result follows."})
	}
	res.Content = append(lead, res.Content...)
	return res
}

// humanWords takes what the human has said to the agent since its last call
// and renders it, or returns "" for nothing to say.
//
// It never fails a call: the messages stay undelivered when they can't be
// taken, so the next call tries again. What it takes is marked delivered, so
// addTool must put it in front of the agent however the call goes. A
// read-only caller is observing, not working, and takes nothing: the
// messages are for the run that acts on them.
func (h *Handler) humanWords(ctx context.Context, agentID uuid.UUID) string {
	if h.messages == nil {
		return ""
	}
	if found, err := authenticatedAgent(ctx); err != nil || found.ReadOnly() {
		return ""
	}
	taken, err := h.messages.Deliver(ctx, agentID)
	if err != nil {
		h.logger.Warn("deliver messages", "agent_id", agentID, "err", err)
		return ""
	}
	return agentmsg.Render(taken)
}

// caller resolves the authenticated agent and records liveness.
//
// Every tool starts here, which is what makes "every call is a heartbeat"
// true rather than aspirational. The lease extension is best-effort: an
// agent with no lease is just idle, not failing.
//
// The one heartbeat outcome passed back is a human having taken the
// agent's ticket away. The server hands that notice out once, so whichever
// call receives it must get it to the agent -- addTool decides how.
func (h *Handler) caller(ctx context.Context) (uuid.UUID, *ticket.LeaseRevokedError, error) {
	found, err := authenticatedAgent(ctx)
	if err != nil {
		return uuid.UUID{}, nil, err
	}
	agentID := found.AgentID()
	// A read-only caller is observing, not working: it must not show the
	// agent as live on the board, nor keep a lease the agent holds alive.
	if found.ReadOnly() {
		return agentID, nil, nil
	}

	if err := h.sessions.Touch(ctx, agentID); err != nil {
		h.logger.Warn("touch session", "agent_id", agentID, "err", err)
	}
	err = h.tickets.Heartbeat(ctx, agentID)
	var revoked *ticket.LeaseRevokedError
	if errors.As(err, &revoked) {
		return agentID, revoked, nil
	}
	if err != nil && !errors.Is(err, ticket.ErrNoLease) {
		h.logger.Warn("heartbeat lease", "agent_id", agentID, "err", err)
	}
	return agentID, nil, nil
}

// leaseLost is how a leaseHeld tool reports ErrNoLease. caller's heartbeat
// delivers a revocation before the tool runs, but a human can revoke
// between that heartbeat and the tool's own lease check, which then fails
// with a bare "no active lease" -- leaving the agent to find out why only
// on its next call. Asking for the notice here delivers it on this one.
func (h *Handler) leaseLost(ctx context.Context, agentID uuid.UUID, err error) error {
	if !errors.Is(err, ticket.ErrNoLease) {
		return err
	}
	if revoked := h.tickets.Heartbeat(ctx, agentID); errors.Is(revoked, ticket.ErrLeaseRevoked) {
		return revoked
	}
	return err
}
