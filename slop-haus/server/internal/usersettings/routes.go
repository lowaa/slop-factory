package usersettings

import (
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
)

// RegisterRoutes mounts the signed-in user's own settings. RequireUser turns
// away agent credentials, and the user id always comes from the session,
// never from the path or body, so there is no way to name another user.
func RegisterRoutes(api huma.API, h *Handler) {
	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireUser(),
		OperationID: "listMySettings",
		Method:      http.MethodGet,
		Path:        "/api/me/settings",
		Summary:     "List the signed-in user's settings, stored or defaulted",
	}, h.List)
	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireUser(),
		OperationID: "setMySetting",
		Method:      http.MethodPut,
		Path:        "/api/me/settings/{name}",
		Summary:     "Set one of the signed-in user's settings",
	}, h.Set)
}

// RegisterTenantRoutes mounts the signed-in user's own settings within the
// tenant they are signed into. As with RegisterRoutes, the user always comes
// from the session; the tenant comes from it too, through the context.
func RegisterTenantRoutes(api huma.API, h *TenantHandler) {
	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireUser(),
		OperationID: "listMyTenantSettings",
		Method:      http.MethodGet,
		Path:        "/api/me/tenant-settings",
		Summary:     "List the signed-in user's settings in this tenant, stored or defaulted",
	}, h.List)
	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireUser(),
		OperationID: "getMyTenantSetting",
		Method:      http.MethodGet,
		Path:        "/api/me/tenant-settings/{name}",
		Summary:     "Get one of the signed-in user's settings in this tenant",
	}, h.Get)
	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireUser(),
		OperationID: "setMyTenantSetting",
		Method:      http.MethodPut,
		Path:        "/api/me/tenant-settings/{name}",
		Summary:     "Set one of the signed-in user's settings in this tenant",
	}, h.Set)
	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireUser(),
		OperationID: "listMyTenantSettingHistory",
		Method:      http.MethodGet,
		Path:        "/api/me/tenant-settings/{name}/history",
		Summary:     "The signed-in user's changes to a setting in this tenant, newest first",
	}, h.History)
}
