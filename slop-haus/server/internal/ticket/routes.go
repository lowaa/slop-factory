package ticket

import (
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
)

// RegisterRoutes mounts slop-haus's ticket REST surface directly on the
// API. The board routes are authn.RequireUser(): the signed-in user's id is
// what CreateTicket and CreateTicketComment attribute a ticket or comment
// to, taken from the request context rather than trusted from the body --
// a body field there used to let any caller impersonate an agent's or
// another tenant's authorship. The claim lifecycle is authn.RequireAgent().
// OperationID becomes the generated TS client's method name -- see
// cmd/generate_openapi_spec.
func RegisterRoutes(api huma.API, h *Handler) {
	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireUser(),
		OperationID: "listTickets",
		Method:      http.MethodGet,
		Path:        "/api/tickets",
		Summary:     "List a page of tickets: filtered, sorted and keyset-paged",
	}, h.ListTickets)
	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireUser(),
		OperationID: "getBoard",
		Method:      http.MethodGet,
		Path:        "/api/board",
		Summary:     "Get the first page and the count of each board column",
	}, h.GetBoard)
	huma.Register(api, huma.Operation{
		Metadata:      authn.RequireUser(),
		OperationID:   "createTicket",
		Method:        http.MethodPost,
		Path:          "/api/tickets",
		Summary:       "File a new ticket",
		DefaultStatus: http.StatusCreated,
	}, h.CreateTicket)
	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireUser(),
		OperationID: "getQueueBlockage",
		Method:      http.MethodGet,
		Path:        "/api/queue/blockage",
		Summary:     "Say whether the queue is waiting on path scopes held elsewhere",
	}, h.GetQueueBlockage)
	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireUser(),
		OperationID: "getTicket",
		Method:      http.MethodGet,
		Path:        "/api/tickets/{id}",
		Summary:     "Get a ticket",
	}, h.GetTicket)
	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireUser(),
		OperationID: "updateTicket",
		Method:      http.MethodPatch,
		Path:        "/api/tickets/{id}",
		Summary:     "Partially update a ticket",
	}, h.UpdateTicket)
	// A person may edit any ticket's title or body. The agent route to the
	// same edit is the MCP ticket_edit tool, and only for the ticket the
	// agent holds.
	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireUser(),
		OperationID: "createTicketRevision",
		Method:      http.MethodPost,
		Path:        "/api/tickets/{id}/revisions",
		Summary:     "Edit a ticket's title and/or body, recording the change in its history",
	}, h.CreateTicketRevision)
	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireUser(),
		OperationID: "listTicketRevisions",
		Method:      http.MethodGet,
		Path:        "/api/tickets/{id}/revisions",
		Summary:     "List the edits to a ticket's title and body, oldest first",
	}, h.ListTicketRevisions)
	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireUser(),
		OperationID: "listTicketStatusHistory",
		Method:      http.MethodGet,
		Path:        "/api/tickets/{id}/status_history",
		Summary:     "List every status change of a ticket, oldest first, with who made it and why",
	}, h.ListTicketStatusHistory)
	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireUser(),
		OperationID: "listTicketExternal",
		Method:      http.MethodGet,
		Path:        "/api/tickets/{id}/external",
		Summary:     "List what an intake source sent about a ticket, oldest first: text from outside, for a person to read as such",
	}, h.ListTicketExternal)
	// Ready is the only way a Preparing ticket enters the queue, and it is
	// a person's call: RequireUser, with no agent route to it at all.
	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireUser(),
		OperationID: "markTicketReady",
		Method:      http.MethodPost,
		Path:        "/api/tickets/{id}/ready",
		Summary:     "Move a Preparing ticket into the queue: open, or blocked on unfinished dependencies",
	}, h.MarkTicketReady)
	// Only a person may change the roles a ticket requires: it can loosen
	// a restriction. Agents add requirements only when filing, through
	// ticket_create.
	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireUser(),
		OperationID: "setTicketRoles",
		Method:      http.MethodPut,
		Path:        "/api/tickets/{id}/roles",
		Summary:     "Replace the roles an agent needs to claim a ticket",
		Description: "Sets the ticket's required roles to exactly role_ids (empty clears them) and role_match " +
			"(any, the default, or all). 422 if any id is not one of the tenant's roles. Held leases are unaffected.",
	}, h.SetTicketRoles)
	// Any member may route a ticket to a variant, as any member may add an
	// attempt; owners control which variants exist. Never an agent's: workers
	// do not choose what claims their ticket.
	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireUser(),
		OperationID: "setTicketVariant",
		Method:      http.MethodPut,
		Path:        "/api/tickets/{id}/variant",
		Summary:     "Route a ticket's attempt to a variant version, or clear it",
		Description: "Any member's. Only agents running exactly that version claim the attempt; with none set, only " +
			"agents in no variant do. 409 once the attempt has had a lease; 422 for an archived version; 404 for a " +
			"ticket or version the tenant does not have. Returns the ticket as getTicket does.",
	}, h.SetTicketVariant)
	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireUser(),
		OperationID: "setTicketAttemptVariant",
		Method:      http.MethodPut,
		Path:        "/api/tickets/{id}/attempts/{n}/variant",
		Summary:     "Route one of a ticket's attempts to a variant version, or clear it",
		Description: "Any member's. As setTicketVariant, for the numbered attempt: 409 once that attempt has had a " +
			"lease; 422 for an archived version; 404 for a ticket, attempt or version the tenant does not have. " +
			"Returns the ticket as getTicket does.",
	}, h.SetTicketAttemptVariant)
	huma.Register(api, huma.Operation{
		Metadata:      authn.RequireUser(),
		OperationID:   "addTicketAttempt",
		Method:        http.MethodPost,
		Path:          "/api/tickets/{id}/attempts",
		Summary:       "Add an attempt to a ticket",
		DefaultStatus: http.StatusCreated,
		Description: "Any member's, never an agent's. Optionally names a variant version for the attempt, or takes attempts: one to five rows added " +
			"together, all or none (422 for none or more than five, or with version_id too). 404 when the " +
			"server does not serve it (ATTEMPTS_ADD_ENABLED is off; code feature_disabled) and for a ticket or version the tenant does not " +
			"have; with replay: true a done ticket takes replays (404 feature_disabled when the server does not serve them; 409 when the ticket is not done or has no base commit) and no ordinary attempt; 409 when the ticket is done (without replay) or cancelled or already has five live attempts (queued, in_progress or " +
			"discussion), counting the batch; 422 for an archived version. The attempts are shadows if the promoted attempt " +
			"has started; otherwise the promoted attempt loses its promotion, once. Returns the ticket as getTicket does.",
	}, h.AddTicketAttempt)
	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireUser(),
		OperationID: "closeTicketAttempt",
		Method:      http.MethodPost,
		Path:        "/api/tickets/{id}/attempts/{n}/close",
		Summary:     "Close one of a ticket's attempts",
		Description: "Any member's, never an agent's. Ends the attempt's agent's lease, which is told to stop, and closes the " +
			"attempt's open change on the code host (a failure there leaves the attempt closed). 404 when the server does " +
			"not serve it (ATTEMPTS_ADD_ENABLED is off; code feature_disabled) and for a ticket or attempt the tenant does not have; 409 for an " +
			"attempt that is closed or done already; 422 for the promoted attempt. Returns the ticket as getTicket does.",
	}, h.CloseTicketAttempt)
	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireUser(),
		OperationID: "promoteTicketAttempt",
		Method:      http.MethodPost,
		Path:        "/api/tickets/{id}/attempts/{n}/promote",
		Summary:     "Promote one of a ticket's attempts",
		Description: "Any member's, never an agent's. Makes the attempt the one whose change may reach the base branch: " +
			"it takes the ticket's scope, the previous promoted attempt (if any) is demoted, an attempt awaiting this choice " +
			"goes back to its agent to merge, and the changes on the code host are retargeted and retitled (a failure there " +
			"leaves the promotion made). 404 when the server does not serve it (ATTEMPTS_PROMOTE_ENABLED is off; code feature_disabled) and for a " +
			"ticket or attempt the tenant does not have; 409 for a done or cancelled ticket, an attempt that is closed or done " +
			"a promoted attempt that holds the integration lock or is merging, a scope another ticket " +
			"holds, and a change whose branch took in the base branch; 503 when the code host could not be asked. Returns the ticket " +
			"as getTicket does. Promoting the promoted attempt writes nothing and carries the changes again, which is how a failure on the code host is retried.",
	}, h.PromoteTicketAttempt)
	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireUser(),
		OperationID: "listTicketComments",
		Method:      http.MethodGet,
		Path:        "/api/tickets/{id}/comments",
		Summary:     "List a ticket's comments",
	}, h.ListTicketComments)
	huma.Register(api, huma.Operation{
		Metadata:      authn.RequireUser(),
		OperationID:   "createTicketComment",
		Method:        http.MethodPost,
		Path:          "/api/tickets/{id}/comments",
		Summary:       "Add a comment to a ticket",
		DefaultStatus: http.StatusCreated,
	}, h.CreateTicketComment)
	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireUser(),
		OperationID: "listTicketAgentContexts",
		Method:      http.MethodGet,
		Path:        "/api/tickets/{id}/contexts",
		Summary:     "List the handoffs agents have left on a ticket",
	}, h.ListAgentContexts)

	// -- Claims (the lease lifecycle) --
	huma.Register(api, huma.Operation{
		OperationID: "claimNext",
		Metadata:    authn.RequireAgent(),
		Method:      http.MethodPost,
		Path:        "/api/claims",
		Summary:     "Claim the next claimable ticket, or a specific one",
	}, h.ClaimNext)
	huma.Register(api, huma.Operation{
		OperationID: "heartbeat",
		Metadata:    authn.RequireAgent(),
		Method:      http.MethodPost,
		Path:        "/api/claims/heartbeat",
		Summary:     "Extend the caller's active lease",
	}, h.Heartbeat)
	huma.Register(api, huma.Operation{
		OperationID: "recordClaimSession",
		Metadata:    authn.RequireAgent(),
		Method:      http.MethodPost,
		Path:        "/api/claims/session",
		Summary:     "Record the harness and session of the run on the attempt the caller holds",
	}, h.RecordSession)
	huma.Register(api, huma.Operation{
		OperationID: "escalateClaim",
		Metadata:    authn.RequireAgent(),
		Method:      http.MethodPost,
		Path:        "/api/claims/escalate",
		Summary:     "Send the ticket the caller holds to discussion with a supervisor's note",
	}, h.EscalateClaim)
	huma.Register(api, huma.Operation{
		OperationID: "releaseClaim",
		Metadata:    authn.RequireAgent(),
		Method:      http.MethodPost,
		Path:        "/api/claims/release",
		Summary:     "Release the ticket the caller holds",
	}, h.ReleaseClaim)
	huma.Register(api, huma.Operation{
		OperationID: "lockIntegration",
		Metadata:    authn.RequireAgent(),
		Method:      http.MethodPost,
		Path:        "/api/integration_lock",
		Summary:     "Take the integration lock",
	}, h.LockIntegration)
	huma.Register(api, huma.Operation{
		OperationID: "unlockIntegration",
		Metadata:    authn.RequireAgent(),
		Method:      http.MethodDelete,
		Path:        "/api/integration_lock",
		Summary:     "Release the integration lock",
	}, h.UnlockIntegration)

	huma.Register(api, huma.Operation{
		OperationID: "getIntegrationLock",
		Metadata:    authn.RequireUser(),
		Method:      http.MethodGet,
		Path:        "/api/integration_lock",
		Summary:     "Who holds the integration lock, and who is queued for it",
		Errors:      []int{http.StatusNotFound},
	}, h.GetIntegrationLock)
	huma.Register(api, huma.Operation{
		OperationID: "getIntegrationLocks",
		Metadata:    authn.RequireUser(),
		Method:      http.MethodGet,
		Path:        "/api/integration_locks",
		Summary:     "Every integration lock the tenant has, each with who holds it and who is queued",
	}, h.GetIntegrationLocks)
	huma.Register(api, huma.Operation{
		OperationID: "forceUnlockIntegration",
		Metadata:    authn.RequireUser(),
		Method:      http.MethodDelete,
		Path:        "/api/integration_lock/holder",
		Summary:     "Release the integration lock whoever holds it",
		Description: "For a holder that is dead but whose lease the lock keeps from expiring. 404 when it is not held.",
		Errors:      []int{http.StatusNotFound},
	}, h.ForceUnlockIntegration)
	huma.Register(api, huma.Operation{
		OperationID: "dropIntegrationLockWaiter",
		Metadata:    authn.RequireUser(),
		Method:      http.MethodDelete,
		Path:        "/api/integration_lock/queue/{agent_id}",
		Summary:     "Remove an agent from the integration lock queue",
		Errors:      []int{http.StatusNotFound},
	}, h.DropLockWaiter)

	// -- Bulk actions on the board --
	registerBulkRoutes(api, h)
	registerReplayBatchRoutes(api, h)

	// -- PR review --
	registerReviewRoutes(api, h)
	registerCloseMergedRoutes(api, h)

	// -- Blocking (the graph over ticket_deps) --
	RegisterGraphRoutes(api, h)
}
