package settings

import (
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
)

// RegisterRoutes mounts slop-haus's settings surface. See
// ticket.RegisterRoutes's doc comment for what OperationID feeds.
//
// Every operation is authn.RequireUser(), reads included: a setting is one
// tenant's data, so answering "what is it set to" needs to know which
// tenant is asking, same as a write.
func RegisterRoutes(api huma.API, h *Handler) {
	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireUser(),
		OperationID: "listSettings",
		Method:      http.MethodGet,
		Path:        "/api/settings",
		Summary:     "List every server setting, stored or defaulted",
	}, h.ListSettings)
	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireUser(),
		OperationID: "getSetting",
		Method:      http.MethodGet,
		Path:        "/api/settings/{name}",
		Summary:     "Read one server setting",
	}, h.GetSetting)
	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireUser(),
		OperationID: "getSettingHistory",
		Method:      http.MethodGet,
		Path:        "/api/settings/{name}/history",
		Summary:     "Who changed one server setting, when, and from what to what",
	}, h.SettingHistory)
	huma.Register(api, huma.Operation{
		OperationID: "updateSetting",
		Metadata:    authn.RequireUser(),
		Method:      http.MethodPatch,
		Path:        "/api/settings/{name}",
		Summary:     "Change one server setting",
	}, h.UpdateSetting)
}
