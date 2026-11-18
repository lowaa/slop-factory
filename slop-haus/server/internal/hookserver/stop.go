package hookserver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"reflect"

	"github.com/danielgtaylor/huma/v2"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/agentmsg"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/session"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/tenantstate"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// StopInput is the subset of Claude Code's Stop hook payload this needs.
//
// The payload is a contract we do not own: Claude Code sends far more than
// this -- transcript_path, permission_mode, hook_event_name,
// last_assistant_message, and more over time -- and a schema that rejects
// unknown properties rejects the whole request. That is not hypothetical.
// The first time real Claude Code called this endpoint every Stop came back
// 422 "unexpected property", so the agent loop never ran at all. See
// Schema below.
type StopInput struct {
	SessionID string `json:"session_id"`
	CWD       string `json:"cwd"`
	// AgentID is set only when the hook fires inside a *subagent*. Its
	// presence is the first thing checked: without that guard, an Explore
	// subagent finishing a search gets told to go claim tickets, which is
	// easy to miss and very confusing once it happens.
	AgentID   string `json:"agent_id,omitempty"`
	AgentType string `json:"agent_type,omitempty"`
	SelfDescription
}

// Schema makes the input permissive: take the fields we understand, ignore
// the rest.
//
// Being strict here buys nothing -- we are the receiving end of somebody
// else's event, not an API whose shape we define -- and costs everything,
// because one unrecognised field fails the request rather than the field.
func (StopInput) Schema(r huma.Registry) *huma.Schema {
	return permissiveSchema(r, reflect.TypeFor[stopInputFields]())
}

// stopInputFields mirrors StopInput without the Schema method, so the
// generated schema can be derived from it rather than recursing.
type stopInputFields struct {
	SessionID string `json:"session_id"`
	CWD       string `json:"cwd"`
	AgentID   string `json:"agent_id,omitempty"`
	AgentType string `json:"agent_type,omitempty"`
}

// StopDecision is the hook's response. An empty value lets the agent stop.
type StopDecision struct {
	Decision      string `json:"decision,omitempty"`
	Reason        string `json:"reason,omitempty"`
	Continue      bool   `json:"continue,omitempty"`
	SystemMessage string `json:"systemMessage,omitempty"`
}

func blockWith(reason, systemMessage string) StopDecision {
	return StopDecision{
		Decision:      "block",
		Reason:        reason,
		Continue:      true,
		SystemMessage: systemMessage,
	}
}

type Handler struct {
	tickets  Tickets
	messages Messages
	sessions Sessions
	layouts  Layouts
	archive  tenantstate.Archive
	logger   *slog.Logger
}

type Deps struct {
	Tickets Tickets
	// Messages may be nil, which hears nothing.
	Messages Messages
	Sessions Sessions
	// Layouts is optional: without it workspace layouts are not recorded.
	Layouts Layouts
	// Archive may be nil, which sees no tenant as archived.
	Archive tenantstate.Archive
}

func NewHandler(deps Deps, logHandler slog.Handler) *Handler {
	return &Handler{
		tickets:  deps.Tickets,
		messages: deps.Messages,
		sessions: deps.Sessions,
		layouts:  deps.Layouts,
		archive:  deps.Archive,
		logger:   slog.New(logHandler),
	}
}

// Stop decides whether the agent may stop.
//
// Order matters and each step earns its place:
//
//  1. A subagent always stops. See StopInput.AgentID.
//  2. Still holding an in_progress ticket -> block. This catches an agent
//     quietly bailing mid-ticket.
//  3. A message from the human, at any step below 1: it blocks even when
//     the agent holds nothing, and its text rides in the reason (see
//     withHumanWords).
//  4. Otherwise the agent stops. Nothing sends it to the queue or to wait
//     for work: a supervisor starts a run per ticket.
func (h *Handler) Stop(ctx context.Context, input *StopInput) (StopDecision, error) {
	caller, err := authn.FromContext(ctx)
	if err != nil {
		return StopDecision{}, err
	}
	agentID := caller.AgentID()

	if input.AgentID != "" {
		return StopDecision{}, nil
	}
	// A conductor (Remote Control) is never blocked.
	if isConductor(caller) {
		return StopDecision{}, nil
	}
	if skip, err := h.archived(ctx, "stop"); skip || err != nil {
		return StopDecision{}, err
	}

	// A superseded run keeps its terminal, its token and its Stop hook, so
	// it goes on firing callbacks. Letting it through would refresh the
	// replacement session's liveness and increment its loop counter --
	// a dead process could keep a live one looking healthy, and then trip
	// its breaker. It is told to stop, which is what it should do.
	state, err := h.sessions.TouchSession(ctx, agentID, input.SessionID)
	switch {
	case err != nil:
		// Liveness bookkeeping must never decide whether an agent may
		// stop -- a failure here is logged and the decision continues.
		h.logger.Warn("touch session", "agent_id", agentID, "err", err)
	case state == session.StateSuperseded:
		h.logger.Info("stop hook from a superseded session",
			"agent_id", agentID, "claude_session_id", input.SessionID)
		return StopDecision{}, nil
	case state == session.StateUnknown:
		// A live run whose SessionStart never landed -- a misconfigured
		// hook, or a session that predates the configuration. Bind it now
		// rather than dismissing it: without a session row the loop
		// breaker has nothing to count against, so the agent would either
		// never be blocked or never be released.
		if _, err := h.sessions.Start(ctx, agentID, input.SessionID, input.CWD); err != nil {
			h.logger.Warn("bind unregistered session", "agent_id", agentID, "err", err)
		} else {
			h.logger.Info("bound a session that never sent SessionStart",
				"agent_id", agentID, "claude_session_id", input.SessionID)
		}
	}
	// After the binding above, so a run seen here first is described too;
	// and on every Stop rather than once, so a model switched mid-run shows
	// up at the next one.
	description := session.ParseDescription(input.SlopHaus)
	h.describe(ctx, agentID, input.SessionID, description)
	h.recordLayout(ctx, agentID, input.SlopHaus)

	v := h.decide(ctx, agentID)
	// The human speaking outranks everything below the held ticket's
	// reason, including "processing is off": it is not new work, it is
	// the person steering an agent that is still here.
	unsaid := h.unsaid(ctx, agentID)
	if v.reason == "" && unsaid == 0 {
		return StopDecision{}, nil
	}
	if v.reason == "" {
		v = &stopVerdict{category: "message"}
	}

	count, tripped, err := h.sessions.RecordBlock(ctx, agentID, input.SessionID)
	if err != nil {
		h.logger.Warn("record stop block", "agent_id", agentID, "err", err)
	}
	if tripped {
		// The loop breaker. It does not release the lease -- that would be
		// exactly the ticket/lease mutation this contract forbids, and it
		// does not need to: the lease has a TTL, and in the meantime the
		// human may well want to look at the workspace before the work is
		// handed to somebody else.
		h.markStuck(ctx, agentID, input.SessionID, caller.AgentName(), v, count)
		return StopDecision{}, nil
	}

	// Taken only now that the agent is certainly being blocked: past the
	// loop breaker it is let stop, and what it was never handed stays
	// pending for its next call.
	reason := h.withHumanWords(ctx, agentID, v, unsaid)
	return blockWith(reason, "slop-haus: "+v.reason), nil
}

// unsaid is how many messages from the human the agent has not been handed.
// A failed count is none: it must not decide whether an agent may stop.
func (h *Handler) unsaid(ctx context.Context, agentID uuid.UUID) int {
	if h.messages == nil {
		return 0
	}
	n, err := h.messages.Pending(ctx, agentID)
	if err != nil {
		h.logger.Warn("count pending messages", "agent_id", agentID, "err", err)
		return 0
	}
	return n
}

// withHumanWords puts the human's messages ahead of the reason the agent
// must keep going, so they arrive with the block and cost it no further
// call. They are marked delivered by being taken, and the block is what
// carries them; the systemMessage shown in the terminal does not repeat
// them.
func (h *Handler) withHumanWords(ctx context.Context, agentID uuid.UUID, v *stopVerdict, unsaid int) string {
	if unsaid == 0 {
		return v.reason
	}
	taken, err := h.messages.Deliver(ctx, agentID)
	if err != nil {
		h.logger.Warn("deliver messages", "agent_id", agentID, "err", err)
		taken = nil
	}
	words := agentmsg.Render(taken)
	switch {
	case words == "" && v.category == "message":
		return "The human sent you a message that another call of yours has already delivered. Act on it before stopping."
	case words == "":
		return v.reason
	case v.category == "message":
		return words
	default:
		return words + "\n\nAlso: " + v.reason
	}
}

// stopVerdict is decide's answer: the reason the agent must keep going, or
// "" to let it stop, plus a short category used for logging and the
// stuck-agent notice.
type stopVerdict struct {
	reason   string
	category string
	// heldTicket is the ticket the agent holds, or 0 when it holds none or
	// that could not be read.
	heldTicket int64
}

// decide reads state and never writes any. (Handing the human's messages
// over is a write, and is done by Stop after the loop breaker, not here.)
//
// An agent holding nothing is let stop: headless runs end when their turn
// does, and a console or Remote Control session has no queue to go back to.
func (h *Handler) decide(ctx context.Context, agentID uuid.UUID) *stopVerdict {
	held, err := h.tickets.LeaseFor(ctx, agentID)
	switch {
	case err == nil:
		return &stopVerdict{reason: h.reasonForHeldTicket(ctx, held), category: "held", heldTicket: held.TicketID}
	case !errors.Is(err, ticket.ErrNoLease):
		// Fail closed. An unreachable database looks exactly like "no
		// lease", and treating it as such would let an agent holding a
		// ticket stop without finishing or releasing it -- the state most
		// expensive to lose, since the work in progress goes with the
		// session.
		//
		// Blocking is bounded rather than a spin: this counts as a
		// stop-block with no progress behind it, so after MaxStopBlocks the
		// loop breaker lets the agent stop and marks it stuck. A database
		// outage therefore costs a few retries and a visible notice, not a
		// silently idled fleet.
		//
		// The stop-block loop breaker (MaxStopBlocks) stays: consoles and
		// headless runs still stop through this hook, and without the
		// breaker a run that cannot satisfy it would be blocked forever.
		h.logger.Error("look up held lease", "agent_id", agentID, "err", err)
		return &stopVerdict{
			reason:   "Cannot reach slop-haus to confirm whether you still hold a ticket. Do not stop yet -- retry.",
			category: "unavailable",
		}
	}
	return &stopVerdict{category: "idle"}
}

func (h *Handler) reasonForHeldTicket(ctx context.Context, held *ticket.Lease) string {
	status, note := "", ""
	if t, err := h.tickets.Get(ctx, held.TicketID); err == nil {
		status = string(t.Status)
		// A held lease on a done ticket can only be a shadow's: the agent
		// must not read "done" as leave to stop.
		if t.Status == ticket.StatusDone {
			note = " " + ticket.DoneThroughOtherNote
		}
		// A parked ticket held by an agent is a replay: it finishes like
		// any attempt rather than leaving the ticket parked unfinished.
		if t.Status == ticket.StatusParked {
			note = " It is parked and you hold it for a replay: finish the replay as normal."
		}
	}
	return fmt.Sprintf("You still hold ticket #%d (%s). Finish it with ticket_finish, or post a comment explaining the blocker "+
		"and call ticket_release.", held.TicketID, status) + note
}

// markStuck records on the agent's session why the loop breaker let it
// stop, which the Agents screen shows until the agent next makes progress.
// The text names the recovery paths a person actually has: the Agents
// screen has no per-lease release, only Disable, which is heavier (it
// refuses the agent's next sign-in too), so moving the ticket comes first.
func (h *Handler) markStuck(ctx context.Context, agentID uuid.UUID, claudeSessionID, agentName string, v *stopVerdict, count int) {
	h.logger.Warn("loop breaker tripped", "agent", agentName, "blocks", count, "reason", v.category, "ticket", v.heldTicket)
	var reason string
	switch {
	case v.heldTicket != 0:
		reason = fmt.Sprintf(
			"Stuck on #%d: blocked %d times with no progress, so it was let stop. Its lease will expire on its own, "+
				"or release it now by moving #%d out of in_progress on its page (or disable the agent).",
			v.heldTicket, count, v.heldTicket)
	default:
		// Either it holds nothing, or the lease lookup failed and it may
		// hold a ticket nobody could name.
		reason = fmt.Sprintf(
			"Appears stuck: blocked %d times with no progress, so it was let stop. If it holds a ticket, its lease will expire "+
				"on its own, or move that ticket out of in_progress on its page to release it now (or disable the agent).",
			count)
	}
	if err := h.sessions.MarkStuck(ctx, agentID, claudeSessionID, reason); err != nil {
		h.logger.Warn("mark agent stuck", "agent", agentName, "err", err)
	}
}
