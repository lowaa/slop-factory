package agent

import (
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
)

// RegisterRoutes mounts slop-haus's agent REST surface.
//
// The board's operations -- listing, registering and disabling agents --
// belong to a signed-in user, and act within that user's tenant: an agent
// minted here can only ever touch that tenant's tickets. The update script
// is the exception, fetched by the agent itself with its own token.
//
// OperationID becomes the generated client's method name, as elsewhere.
func RegisterRoutes(api huma.API, h *Handler) {
	registerSupervisorRoutes(api, h)
	registerHostLeaseRoutes(api, h)
	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireUser(),
		OperationID: "listAgents",
		Method:      http.MethodGet,
		Path:        "/api/agents",
		Summary:     "List agents",
	}, h.ListAgents)
	huma.Register(api, huma.Operation{
		OperationID:   "createAgent",
		Metadata:      authn.RequireUser(),
		Method:        http.MethodPost,
		Path:          "/api/agents",
		Summary:       "Register a new agent",
		Description:   "Registers an agent with no credential of its own: a client is authorized to act as it through OAuth (the consent or device page).",
		DefaultStatus: http.StatusCreated,
	}, h.CreateAgent)
	huma.Register(api, huma.Operation{
		OperationID: "createAgentSetupScript",
		Metadata:    authn.RequireUser(),
		Method:      http.MethodPost,
		Path:        "/api/agents/setup_script",
		Summary:     "Register an agent and return a shell script that configures a workspace for it",
		Description: "Registers the agent and returns a script that writes its workspace and then runs the device " +
			"sign-in, which the user approves on the board. The script carries no credential.",
		DefaultStatus: http.StatusCreated,
		Responses:     shellScriptResponses(http.StatusCreated),
		Middlewares:   huma.Middlewares{BaseURLMiddleware},
	}, h.SetupScript)
	huma.Register(api, huma.Operation{
		OperationID: "rotateAgentSetupScript",
		Metadata:    authn.RequireUser(),
		Method:      http.MethodPost,
		Path:        "/api/agents/{id}/setup_script",
		Summary:     "Sign out an existing agent's workspaces and return a new setup script for it",
		Description: "Revokes every OAuth grant acting as the agent, so any workspace signed in as it stops working, " +
			"and returns the same script as registering it did. Its device sign-in issues a fresh grant. " +
			"409 for a disabled agent.",
		Responses:   shellScriptResponses(http.StatusOK),
		Middlewares: huma.Middlewares{BaseURLMiddleware},
	}, h.RotateSetupScript)
	huma.Register(api, huma.Operation{
		OperationID: "getWorkspaceUpdateScript",
		// The agent's own access token: an update mints nothing and carries
		// no secret, and the machine running the agent has the agent's
		// credentials and nothing else. A supervisor's login installs token.sh
		// into its directory from this script, so it is let in; the script
		// only ever renders for the token's own agent.
		Metadata: authn.RequireAgentOrSupervisor(),
		Method:   http.MethodGet,
		Path:     "/api/workspace/update_script",
		Summary:  "Return a shell script that brings the calling agent's workspace up to date",
		Description: "What .slop-haus/update.sh fetches and runs. Rewrites the files slop-haus manages " +
			"(hooks, settings, briefing, the updater itself); never touches the stored credentials, " +
			"and only creates files the workspace owns if they are missing.",
		Responses:   shellScriptResponses(http.StatusOK),
		Middlewares: huma.Middlewares{BaseURLMiddleware},
	}, h.UpdateScript)
	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireUser(),
		OperationID: "getAgent",
		Method:      http.MethodGet,
		Path:        "/api/agents/{id}",
		Summary:     "Get an agent",
	}, h.GetAgent)
	huma.Register(api, huma.Operation{
		OperationID: "setAgentDisabled",
		Metadata:    authn.RequireUser(),
		Method:      http.MethodPatch,
		Path:        "/api/agents/{id}",
		Summary:     "Enable or disable an agent",
	}, h.SetAgentDisabled)
	huma.Register(api, huma.Operation{
		OperationID: "archiveAgent",
		Metadata:    authn.RequireUser(),
		Method:      http.MethodPost,
		Path:        "/api/agents/{id}/archive",
		Summary:     "Archive a disabled agent",
		Description: "Drops it out of the agent lists, which still return it with include_archived=true. Its history keeps naming it. " +
			"409 for an agent that is still enabled; archiving an archived agent changes nothing.",
	}, h.ArchiveAgent)
	huma.Register(api, huma.Operation{
		OperationID: "unarchiveAgent",
		Metadata:    authn.RequireUser(),
		Method:      http.MethodPost,
		Path:        "/api/agents/{id}/unarchive",
		Summary:     "Unarchive an agent",
		Description: "Brings it back into the lists. It stays disabled. An archived agent cannot be enabled before this.",
	}, h.UnarchiveAgent)
	registerRoleRoutes(api, h)
	registerConductorRoutes(api, h)
	registerProvisionRoutes(api, h)
	registerOwnRemoteControlRoutes(api, h)
}

// registerRoleRoutes mounts role management. Every route is a signed-in
// user's, never an agent's: a role decides which tickets an agent may
// claim, so an agent able to reach any of these could grant itself work.
// The handlers refuse a caller with no user as well (requireSignedInUser).
func registerRoleRoutes(api huma.API, h *Handler) {
	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireUser(),
		OperationID: "listAgentRoles",
		Method:      http.MethodGet,
		Path:        "/api/agent-roles",
		Summary:     "List the tenant's agent roles",
	}, h.ListAgentRoles)
	huma.Register(api, huma.Operation{
		Metadata:      authn.RequireUser(),
		OperationID:   "createAgentRole",
		Method:        http.MethodPost,
		Path:          "/api/agent-roles",
		Summary:       "Create an agent role",
		DefaultStatus: http.StatusCreated,
	}, h.CreateAgentRole)
	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireUser(),
		OperationID: "updateAgentRole",
		Method:      http.MethodPatch,
		Path:        "/api/agent-roles/{id}",
		Summary:     "Rename an agent role or change its description",
	}, h.UpdateAgentRole)
	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireUser(),
		OperationID: "deleteAgentRole",
		Method:      http.MethodDelete,
		Path:        "/api/agent-roles/{id}",
		Summary:     "Delete an agent role, taking it away from every agent that has it",
		Description: "409, listing the tickets, while tickets require the role -- unless clear_tickets=true, " +
			"which removes it from those tickets too. A ticket it was the only role of is then claimable by any agent.",
		DefaultStatus: http.StatusNoContent,
	}, h.DeleteAgentRole)
	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireUser(),
		OperationID: "setAgentRoles",
		Method:      http.MethodPut,
		Path:        "/api/agents/{id}/roles",
		Summary:     "Replace an agent's roles",
		Description: "Sets the agent's roles to exactly role_ids. 422 if any of them is not one of the tenant's roles; " +
			"the agent's roles are then left as they were.",
	}, h.SetAgentRoles)
}
