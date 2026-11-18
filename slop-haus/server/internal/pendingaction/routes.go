package pendingaction

import (
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
)

// RegisterRoutes mounts the approvals' REST surface. Every route is
// authn.RequireUser(): a proposal exists because an agent's credential is
// not enough to take a human action, so no agent route may list or decide
// one, and the console's own view of its proposals is the MCP tool
// list_proposals. Approving takes no request body: what is applied is what
// the proposal stored.
func RegisterRoutes(api huma.API, h *Handler) {
	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireUser(),
		OperationID: "listProposals",
		Method:      http.MethodGet,
		Path:        "/api/proposals",
		Summary:     "List console proposals",
	}, h.ListProposals)
	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireUser(),
		OperationID: "getProposal",
		Method:      http.MethodGet,
		Path:        "/api/proposals/{id}",
		Summary:     "Get a console proposal",
	}, h.GetProposal)
	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireUser(),
		OperationID: "approveProposal",
		Method:      http.MethodPost,
		Path:        "/api/proposals/{id}/approve",
		Summary:     "Approve a proposal, applying it as the signed-in person",
		Description: "Applies exactly what the proposal stored. 409 when it is no longer pending, or when the ticket has " +
			"changed since (the proposal is then settled as stale), its proposer is no longer an enabled console " +
			"(voided), or the ticket's rules refuse it (failed); nothing on the ticket changes.",
	}, h.ApproveProposal)
	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireUser(),
		OperationID: "rejectProposal",
		Method:      http.MethodPost,
		Path:        "/api/proposals/{id}/reject",
		Summary:     "Reject a proposal",
		Description: "The reason, if any, is what the proposing console reads back.",
	}, h.RejectProposal)
	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireUser(),
		OperationID: "approveAllProposals",
		Method:      http.MethodPost,
		Path:        "/api/proposals/approve_all",
		Summary:     "Approve every pending proposal of one kind: ready or discussion answers only",
		Description: "Each is approved on its own, and each result is reported. Review decisions and status changes " +
			"are never batched.",
	}, h.ApproveAll)
	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireUser(),
		OperationID: "rejectAllProposals",
		Method:      http.MethodPost,
		Path:        "/api/proposals/reject_all",
		Summary:     "Reject every pending proposal",
	}, h.RejectAll)
	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireUser(),
		OperationID: "vetoStewardDecision",
		Method:      http.MethodPost,
		Path:        "/api/proposals/{id}/steward/veto",
		Summary:     "Veto the steward's pending decision on a proposal",
		Description: "Owner only. Cancels a decision the steward has not applied yet; the proposal stays pending for the " +
			"owner. 409 when the steward already applied it (undo it instead), or it is voided, vetoed or decided.",
	}, h.VetoSteward)
	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireUser(),
		OperationID: "undoStewardDecision",
		Method:      http.MethodPost,
		Path:        "/api/proposals/{id}/steward/undo",
		Summary:     "Undo what the steward applied for a proposal",
		Description: "Owner only. Reverts a discussion answer (the ticket returns to discussion), a ready (to preparing) or an " +
			"edit (to what it replaced). 409 when nothing was applied, or the ticket has moved on since.",
	}, h.UndoSteward)
	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireUser(),
		OperationID: "applyStewardDraft",
		Method:      http.MethodPost,
		Path:        "/api/proposals/{id}/steward/apply",
		Summary:     "Apply the steward's draft of a proposal as the signed-in owner",
		Description: "Owner only; only while the tenant's steward level is Assist. Approves the proposal after checking it again " +
			"against what the server reads now. 409 when the draft is out of date (the ticket's revision or head moved, or a check " +
			"no longer holds), was already applied, or the proposal is no longer pending; 422 when the draft is notes only.",
	}, h.ApplyStewardDraft)
}
