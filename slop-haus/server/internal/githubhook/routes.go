package githubhook

import (
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
)

// RegisterRoutes mounts the board's view of the webhook deliveries.
// Deliveries themselves come in on a listener of their own (Receiver).
func RegisterRoutes(api huma.API, h *Handler) {
	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireUser(),
		OperationID: "listGitHubWebhooks",
		Method:      http.MethodGet,
		Path:        "/api/github/webhooks",
		Summary:     "List the tenant's recent GitHub webhook deliveries",
	}, h.ListWebhooks)
}
