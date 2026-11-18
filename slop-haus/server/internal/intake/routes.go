package intake

import (
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
)

// RegisterRoutes mounts the board's intake settings. Any member may manage
// their tenant's sources, as they may file tickets; an agent may not -- a
// source's credential puts work in the queue agents claim from, and an agent
// minting one would be handing itself work. Deliveries themselves come in on
// a listener of their own (Receiver).
func RegisterRoutes(api huma.API, h *Handler) {
	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireUser(),
		OperationID: "listIntakeSources",
		Method:      http.MethodGet,
		Path:        "/api/intake/sources",
		Summary:     "List the tenant's intake sources and their recent deliveries",
	}, h.ListSources)
	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireUser(),
		OperationID: "createIntakeSource",
		Method:      http.MethodPost,
		Path:        "/api/intake/sources",
		Summary:     "Register an intake source, answering its credential once (needs a recent sign-in)",
	}, h.CreateSource)
	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireUser(),
		OperationID: "rotateIntakeSource",
		Method:      http.MethodPost,
		Path:        "/api/intake/sources/{id}/rotate",
		Summary:     "Replace an intake source's credential, answering the new one once (needs a recent sign-in)",
	}, h.RotateSource)
	huma.Register(api, huma.Operation{
		Metadata:      authn.RequireUser(),
		OperationID:   "revokeIntakeSource",
		Method:        http.MethodDelete,
		Path:          "/api/intake/sources/{id}",
		Summary:       "Revoke an intake source",
		DefaultStatus: http.StatusNoContent,
	}, h.RevokeSource)
}
