package hookserver

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/agentmsg"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/session"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// SessionStartInput is the subset of Claude Code's SessionStart payload
// this needs. Like StopInput it accepts whatever else the sender includes
// -- see permissiveSchema.
type SessionStartInput struct {
	SessionID string `json:"session_id"`
	CWD       string `json:"cwd"`
	// Source is why the session started: "startup", or for one that
	// replaced another in the same process, "clear" (Claude Code) or "new"
	// (pi). Other values exist; only these two are acted on.
	Source string `json:"source,omitempty"`
	SelfDescription
}

func (SessionStartInput) Schema(r huma.Registry) *huma.Schema {
	return permissiveSchema(r, reflect.TypeFor[sessionStartInputFields]())
}

type sessionStartInputFields struct {
	SessionID string `json:"session_id"`
	CWD       string `json:"cwd"`
	Source    string `json:"source,omitempty"`
}

// SessionStartOutput is injected into the agent's context before its first
// turn.
type SessionStartOutput struct {
	AdditionalContext string `json:"additionalContext,omitempty"`
	SystemMessage     string `json:"systemMessage,omitempty"`
}

// SessionStart binds the run to its agent and tells the agent who it is.
//
// This is what removes the "tell them to check in" step: starting an agent
// becomes cd + claude, with no prose typed by the human, and the
// instructions live on the server so they cannot go stale across several
// workspaces.
//
// It injects identity and conventions only -- it never claims work. The
// human keeps control of when an agent starts working.
func (h *Handler) SessionStart(ctx context.Context, input *SessionStartInput) (SessionStartOutput, error) {
	caller, err := authn.FromContext(ctx)
	if err != nil {
		return SessionStartOutput{}, err
	}
	agentID, agentName := caller.AgentID(), caller.AgentName()
	// A conductor's session is the host's, not a worker's to bind or brief.
	if isConductor(caller) {
		return SessionStartOutput{}, nil
	}
	if skip, err := h.archived(ctx, "session start"); skip || err != nil {
		return SessionStartOutput{}, err
	}

	result, err := h.sessions.Start(ctx, agentID, input.SessionID, input.CWD)
	if err != nil {
		return SessionStartOutput{}, fmt.Errorf("bind session: %w", err)
	}
	h.describe(ctx, agentID, input.SessionID, session.ParseDescription(input.SlopHaus))
	// Not for a session a newer one has superseded: a late retry from it
	// must not overwrite the workspace the current run just reported.
	if result.Arrival != session.ArrivalStale {
		h.recordLayout(ctx, agentID, input.SlopHaus)
	}

	out := SessionStartOutput{AdditionalContext: h.briefing(ctx, agentName, agentID)}
	// A session that replaced the last one in the same process -- a /clear,
	// or pi's fresh session per ticket -- finds its predecessor still
	// looking live, since that was seen moments ago, but there is only
	// one terminal. Warning there would ask the human to close it.
	if result.ConcurrentRun && !replacedInProcess(input.Source) {
		out.SystemMessage = "slop-haus: another agent run looks live in this workspace -- close one of the terminals."
	}
	if result.Arrival == session.ArrivalStale {
		out.SystemMessage = "slop-haus: this session was superseded by a newer one; it has not been rebound."
	}
	return out, nil
}

func replacedInProcess(source string) bool {
	return source == "clear" || source == "new"
}

// briefing is the text injected into the agent's context: only what changes
// from session to session. The working agreement reaches each harness as a
// file the workspace setup writes (CLAUDE.md's import, pi's system prompt),
// so repeating it here would only give a session two copies to drift apart.
func (h *Handler) briefing(ctx context.Context, agentName string, agentID uuid.UUID) string {
	var b strings.Builder

	fmt.Fprintf(&b, "You are agent %q in slop-haus, which coordinates several agents working on separate checkouts of one repo.\n", agentName)

	if held, err := h.tickets.LeaseFor(ctx, agentID); err == nil {
		fmt.Fprintf(&b, "\nYou are already holding ticket #%d from a previous run -- it is still yours. Pick it back up.\n", held.TicketID)
	} else if !errors.Is(err, ticket.ErrNoLease) {
		h.logger.Warn("look up held lease for briefing", "agent_id", agentID, "err", err)
	}

	// What the human said while the agent was away. Taken here, so a session
	// that starts finds it in its briefing and not again on its first call.
	if h.messages != nil {
		if taken, err := h.messages.Deliver(ctx, agentID); err != nil {
			h.logger.Warn("deliver messages for briefing", "agent_id", agentID, "err", err)
		} else if len(taken) > 0 {
			b.WriteString("\n" + agentmsg.Render(taken) + "\n")
		}
	}

	return b.String()
}
