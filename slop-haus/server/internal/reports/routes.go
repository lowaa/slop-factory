package reports

import (
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
)

// RegisterRoutes mounts the Reports tab's reads: for a signed-in user, over
// the tenant of their session.
func RegisterRoutes(api huma.API, h *Handler) {
	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireUser(),
		OperationID: "getReport",
		Method:      http.MethodGet,
		Path:        "/api/reports",
		Summary:     "Lead time, time in each state and throughput over a period",
		Description: "503 when the report took longer than the server allows.",
	}, h.GetReport)
	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireUser(),
		OperationID: "getReportWaiting",
		Method:      http.MethodGet,
		Path:        "/api/reports/waiting",
		Summary:     "The tickets that have been in a status longest, one page",
	}, h.GetWaiting)
	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireUser(),
		OperationID: "getLLMUsageReport",
		Method:      http.MethodGet,
		Path:        "/api/reports/llm-usage",
		Summary:     "LLM usage over a period: totals, trend, and breakdowns by agent, model and tool",
		Description: "Each figure is per billing mode; subscription cost is an API-equivalent estimate. 503 when the report took longer than the server allows.",
	}, h.GetLLMUsage)
	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireUser(),
		OperationID: "getLLMUsageTickets",
		Method:      http.MethodGet,
		Path:        "/api/reports/llm-usage/tickets",
		Summary:     "The tickets with LLM usage in a period, most tokens first, one page",
	}, h.GetLLMUsageTickets)
	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireUser(),
		OperationID: "getScopeCostReport",
		Method:      http.MethodGet,
		Path:        "/api/reports/scope-cost",
		Summary:     "What exclusive path scopes cost over a period: tickets held back, time waited, idle capacity, refused extensions",
		Description: "503 when the report took longer than the server allows.",
	}, h.GetScopeCost)
	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireUser(),
		OperationID: "getLockHistoryReport",
		Method:      http.MethodGet,
		Path:        "/api/reports/lock-history",
		Summary:     "How long the integration lock is held and queued for, per day, and what a queue could overlap",
		Description: "503 when the report took longer than the server allows.",
	}, h.GetLockHistory)
	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireUser(),
		OperationID: "getShadowDecisionsReport",
		Method:      http.MethodGet,
		Path:        "/api/reports/shadow-decisions",
		Summary:     "What the independent-apps merge rule would have decided, and whether the checks of the accepted ones later passed",
		Description: "Decisions are recorded while the rule is not enforced. 503 when the report took longer than the server allows.",
	}, h.GetShadowDecisions)
}
