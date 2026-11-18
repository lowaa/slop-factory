package hookserver

import (
	"context"
	"reflect"

	"github.com/danielgtaylor/huma/v2"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/session"
)

// UserPromptSubmitInput is the subset of Claude Code's UserPromptSubmit
// payload this needs. The prompt text is deliberately not read: it is the
// human's, and nothing here has a use for it.
type UserPromptSubmitInput struct {
	SessionID string `json:"session_id"`
	CWD       string `json:"cwd"`
}

func (UserPromptSubmitInput) Schema(r huma.Registry) *huma.Schema {
	return permissiveSchema(r, reflect.TypeFor[userPromptSubmitInputFields]())
}

type userPromptSubmitInputFields struct {
	SessionID string `json:"session_id"`
	CWD       string `json:"cwd"`
}

// UserPromptSubmit gives a run that a human has just spoken to a fresh
// stop-block budget.
//
// The loop breaker lets a run stop once it has been blocked MaxStopBlocks
// times with no progress, and a stopped session runs nothing -- so the
// next Stop comes from a new turn, which in practice is someone nudging
// the agent. Without this, that Stop would find the count still at the
// limit and trip at once, handing the agent no block at all: it would
// never be told to wait or to see its ticket through, and a nudge could
// not un-stick anything.
//
// It resets the count and nothing else. The stuck notice stays until the
// agent makes progress, so a nudge that does not help still shows as stuck.
//
// It never decides anything about the prompt, and any failure here is only
// logged: bookkeeping must not stand between a human and their agent.
func (h *Handler) UserPromptSubmit(ctx context.Context, input *UserPromptSubmitInput) error {
	caller, err := authn.FromContext(ctx)
	if err != nil {
		return err
	}
	agentID := caller.AgentID()
	if isConductor(caller) {
		return nil
	}
	if skip, err := h.archived(ctx, "user prompt submit"); skip || err != nil {
		return err
	}

	// The same scoping as Stop's: a superseded run keeps its terminal and
	// its hooks, and must not hand its replacement more blocks. A run the
	// server has never seen has no count to reset.
	state, err := h.sessions.TouchSession(ctx, agentID, input.SessionID)
	if err != nil {
		h.logger.Warn("touch session", "agent_id", agentID, "err", err)
		return nil
	}
	if state != session.StateLive {
		return nil
	}
	if err := h.sessions.ResetBlocks(ctx, agentID, input.SessionID); err != nil {
		h.logger.Warn("reset stop blocks", "agent_id", agentID, "err", err)
	}
	return nil
}
