package agentmsg

import (
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
)

// RegisterRoutes mounts the board's REST surface for messages to agents.
// Every route is authn.RequireUser(): a message is the human steering an
// agent, so no agent credential may send one -- not to itself, not to a
// peer -- and the agent's own way of receiving them is the notice on its
// MCP calls, never a route. Writes through RequireUser are also behind the
// browser guard's CSRF check, which is what keeps another site from
// speaking as the signed-in person.
func RegisterRoutes(api huma.API, h *Handler) {
	huma.Register(api, huma.Operation{
		Metadata:      authn.RequireUser(),
		OperationID:   "sendAgentMessage",
		Method:        http.MethodPost,
		Path:          "/api/agents/{id}/messages",
		Summary:       "Send a message to an agent",
		Description:   "Held until the agent's next call to slop-haus, which carries it. 409 for a disabled agent or one with too many undelivered messages; 422 for an empty or over-long body.",
		DefaultStatus: http.StatusCreated,
	}, h.Send)
	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireUser(),
		OperationID: "listAgentMessages",
		Method:      http.MethodGet,
		Path:        "/api/agent_messages",
		Summary:     "List recent messages to agents and whether each was delivered",
	}, h.List)
}
