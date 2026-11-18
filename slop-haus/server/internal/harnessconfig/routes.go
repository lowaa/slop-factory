package harnessconfig

import (
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
)

// RegisterRoutes mounts the harness config surface. Reading and saving the
// tenant's config is a signed-in person's, never an agent's: whoever sets
// it chooses what runs in every agent's process. An agent reads only its
// own effective config, through the one route its token opens.
func RegisterRoutes(api huma.API, h *Handler) {
	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireUser(),
		OperationID: "getHarnessConfig",
		Method:      http.MethodGet,
		Path:        "/api/harness_config",
		Summary:     "Read the tenant's harness config: the settings there are, the defaults, and every agent's override",
	}, h.GetHarnessConfig)
	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireUser(),
		OperationID: "saveHarnessConfigDefaults",
		Method:      http.MethodPut,
		Path:        "/api/harness_config",
		Summary:     "Replace the tenant's default harness config",
		Description: "The body is the whole set: a setting left out is unset. 422 for an unknown setting or a value " +
			"that fails its pattern. Applies to each agent the next time its harness starts.",
	}, h.SaveDefaults)
	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireUser(),
		OperationID: "saveAgentHarnessConfig",
		Method:      http.MethodPut,
		Path:        "/api/agents/{id}/harness_config",
		Summary:     "Replace one agent's harness config override",
		Description: "Laid over the defaults one setting at a time; a setting left out takes the default. 404 for an " +
			"agent the tenant does not have. A conductor's is its owner's alone (403 for anyone else) and may be saved " +
			"while it is disabled.",
	}, h.SaveOverride)
	huma.Register(api, huma.Operation{
		Metadata:      authn.RequireUser(),
		OperationID:   "removeAgentHarnessConfig",
		Method:        http.MethodDelete,
		Path:          "/api/agents/{id}/harness_config",
		DefaultStatus: http.StatusNoContent,
		Summary:       "Remove one agent's harness config override, so it follows the tenant's defaults again",
		Description:   "Same authorization as saving an override. Removing one the agent does not have is a no-op.",
	}, h.RemoveOverride)
	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireAgent(),
		OperationID: "getWorkspaceHarnessConfig",
		Method:      http.MethodGet,
		Path:        "/api/workspace/harness_config",
		Summary:     "The calling agent's effective harness config, which .slop-haus/run_remote_control.sh fetches when it starts a harness",
		Description: "204 while the tenant has never saved any config.",
	}, h.GetWorkspaceHarnessConfig)
}
