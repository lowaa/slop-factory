package llmusage

import (
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
)

// RegisterRoutes mounts the usage hook. Agent-authenticated like the other
// hooks: the tenant and agent a report is stored under are the token's.
func RegisterRoutes(api huma.API, h *Handler) {
	huma.Register(api, huma.Operation{
		Metadata:     authn.RequireAgent(),
		OperationID:  "usageHook",
		Method:       http.MethodPost,
		Path:         "/hooks/usage",
		Summary:      "Report an agent transcript's LLM usage: full totals per model, which replace the last report's",
		Description:  "422 for an invalid record or command, 429 past the per-agent rate limit. A client must never wait on this or fail because of it.",
		MaxBodyBytes: MaxBodyBytes,
	}, h.IngestUsage)
}
