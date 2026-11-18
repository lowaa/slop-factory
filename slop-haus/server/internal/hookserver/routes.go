package hookserver

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
)

// The hook endpoints are agent-authenticated like any other agent route.
// Claude Code's http hook type sends the Authorization header from the
// workspace's own settings, so the same bearer token that authorises tool
// calls authorises these.

type stopRequest struct {
	Body StopInput
}

// stopResponse mirrors the documented shape exactly, and no more than it.
//
// hookSpecificOutput carries only hookEventName, decision and reason;
// systemMessage is a universal field and sits beside it, not inside it. An
// earlier version also put `continue` and `systemMessage` inside, and the
// decision was silently ignored -- Claude Code honours decision fields
// only when the response passes its own schema validation, and otherwise
// just lets the action proceed. Which is indistinguishable, from the
// outside, from a hook that was never called.
type stopResponse struct {
	Body struct {
		HookSpecificOutput *stopPayload `json:"hookSpecificOutput,omitempty"`
		SystemMessage      string       `json:"systemMessage,omitempty"`
		// Continue is what actually sends the agent back to work:
		// "tells Claude to continue the conversation after being blocked
		// from stopping". Without it the reason is merely *shown* -- the
		// agent reads why it should keep going and stops anyway, which
		// looks like the loop working right up until you check whether
		// any ticket was claimed.
		//
		// Universal field, so it sits beside hookSpecificOutput rather
		// than inside it -- putting decision fields in the wrong place is
		// what made the block get discarded before.
		Continue bool `json:"continue,omitempty"`
	}
}

type stopPayload struct {
	HookEventName string `json:"hookEventName"`
	Decision      string `json:"decision,omitempty"`
	Reason        string `json:"reason,omitempty"`
}

type userPromptSubmitRequest struct {
	Body UserPromptSubmitInput
}

type userPromptSubmitResponse struct{}

type sessionStartRequest struct {
	Body SessionStartInput
}

type sessionStartResponse struct {
	Body struct {
		HookSpecificOutput *sessionStartPayload `json:"hookSpecificOutput,omitempty"`
		SystemMessage      string               `json:"systemMessage,omitempty"`
	}
}

type sessionStartPayload struct {
	HookEventName     string `json:"hookEventName"`
	AdditionalContext string `json:"additionalContext,omitempty"`
}

func RegisterRoutes(api huma.API, h *Handler) {
	huma.Register(api, huma.Operation{
		OperationID: "stopHook",
		Metadata:    authn.RequireAgent(),
		Method:      http.MethodPost,
		Path:        "/hooks/stop",
		Summary:     "Claude Code Stop hook",
	}, func(ctx context.Context, input *stopRequest) (*stopResponse, error) {
		decision, err := h.Stop(ctx, &input.Body)
		if err != nil {
			return nil, err
		}
		resp := &stopResponse{}
		if decision.Decision != "" {
			resp.Body.HookSpecificOutput = &stopPayload{
				HookEventName: "Stop",
				Decision:      decision.Decision,
				Reason:        decision.Reason,
			}
			resp.Body.SystemMessage = decision.SystemMessage
			resp.Body.Continue = decision.Continue
		}
		return resp, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "sessionStartHook",
		Metadata:    authn.RequireAgent(),
		Method:      http.MethodPost,
		Path:        "/hooks/session-start",
		Summary:     "Claude Code SessionStart hook",
	}, func(ctx context.Context, input *sessionStartRequest) (*sessionStartResponse, error) {
		out, err := h.SessionStart(ctx, &input.Body)
		if err != nil {
			return nil, err
		}
		resp := &sessionStartResponse{}
		if out.AdditionalContext != "" || out.SystemMessage != "" {
			resp.Body.HookSpecificOutput = &sessionStartPayload{
				HookEventName:     "SessionStart",
				AdditionalContext: out.AdditionalContext,
			}
			resp.Body.SystemMessage = out.SystemMessage
		}
		return resp, nil
	})

	// Answers with an empty body: for this event Claude Code adds a command
	// hook's stdout to the prompt's context, so there is nothing to say.
	huma.Register(api, huma.Operation{
		OperationID:   "userPromptSubmitHook",
		Metadata:      authn.RequireAgent(),
		Method:        http.MethodPost,
		Path:          "/hooks/user-prompt-submit",
		Summary:       "Claude Code UserPromptSubmit hook",
		DefaultStatus: http.StatusOK,
	}, func(ctx context.Context, input *userPromptSubmitRequest) (*userPromptSubmitResponse, error) {
		if err := h.UserPromptSubmit(ctx, &input.Body); err != nil {
			return nil, err
		}
		return &userPromptSubmitResponse{}, nil
	})
}
