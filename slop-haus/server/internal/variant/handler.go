package variant

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/harness"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/httperr"
)

// Handler is a thin HTTP wrapper over Service, like the other handlers.
type Handler struct {
	svc    *Service
	logger *slog.Logger
}

func NewHandler(svc *Service, logHandler slog.Handler) *Handler {
	return &Handler{svc: svc, logger: slog.New(logHandler)}
}

var errTable = httperr.Table{
	Rules: []httperr.Rule{
		{Err: ErrNotOwner, Kind: httperr.Forbidden},
		{Err: ErrInvalid, Kind: httperr.Invalid},
		{Err: ErrNotFound, Kind: httperr.NotFound, Message: "no such version"},
		{Err: ErrAgentNotFound, Kind: httperr.NotFound, Message: "no such agent"},
		httperr.SignIn,
	},
	Unexpected: "unexpected variants error",
}

func (h *Handler) mapError(err error) error {
	return httperr.Map(h.logger, err, &errTable)
}

// VariantOptionBody is a version a ticket can be routed to: enough to name
// it, and not its settings, which are the owners'.
type VariantOptionBody struct {
	ID      string       `json:"id"`
	Name    string       `json:"name"`
	Version int          `json:"version"`
	Harness harness.Name `json:"harness"`
}

type ListVariantOptionsResponse struct {
	Body struct {
		Options []VariantOptionBody `json:"options" doc:"By name, then newest version first."`
	}
}

func (h *Handler) ListOptions(ctx context.Context, _ *struct{}) (*ListVariantOptionsResponse, error) {
	who, err := authn.UserFromContext(ctx)
	if err != nil {
		return nil, h.mapError(err)
	}
	versions, err := h.svc.Options(ctx, who)
	if err != nil {
		return nil, h.mapError(err)
	}
	resp := &ListVariantOptionsResponse{}
	resp.Body.Options = make([]VariantOptionBody, len(versions))
	for i, v := range versions {
		resp.Body.Options[i] = VariantOptionBody{ID: v.ID.String(), Name: v.Name, Version: v.Version, Harness: harness.Name(v.Harness)}
	}
	return resp, nil
}

type VariantVersionBody struct {
	ID       string            `json:"id"`
	Name     string            `json:"name"`
	Version  int               `json:"version"`
	Harness  harness.Name      `json:"harness"`
	Settings map[string]string `json:"settings" doc:"The harness's own settings, name to value."`
	// The pointer is null for explicitly no fleet instructions.
	FleetInstructionsVersionID *string `json:"fleet_instructions_version_id" doc:"The pinned fleet instructions version; null for none, which is not the tenant's default."`
	CreatedBy                  *string `json:"created_by"`
	// CreatedByDeleted is true when the author's account was deleted:
	// created_by is then null.
	CreatedByDeleted bool       `json:"created_by_deleted,omitempty"`
	CreatedAt        time.Time  `json:"created_at"`
	Archived         bool       `json:"archived"`
	ArchivedAt       *time.Time `json:"archived_at" doc:"When the version was archived; null while it is listed."`
}

func versionBody(v *Version) VariantVersionBody {
	out := VariantVersionBody{
		ID: v.ID.String(), Name: v.Name, Version: v.Version, Harness: harness.Name(v.Harness), Settings: v.Settings,
		CreatedAt: v.CreatedAt, Archived: v.ArchivedAt != nil, ArchivedAt: v.ArchivedAt, CreatedByDeleted: v.CreatedByDeleted,
	}
	if out.Settings == nil {
		out.Settings = map[string]string{}
	}
	if v.FleetInstructionsVersionID != nil {
		id := v.FleetInstructionsVersionID.String()
		out.FleetInstructionsVersionID = &id
	}
	if v.CreatedBy != nil {
		id := v.CreatedBy.String()
		out.CreatedBy = &id
	}
	return out
}

type ListVariantsRequest struct {
	IncludeArchived bool `query:"include_archived" required:"false" doc:"Also list archived versions, which are hidden by default."`
}

type ListVariantsResponse struct {
	Body struct {
		Versions []VariantVersionBody `json:"versions" doc:"By name, then newest version first."`
	}
}

func (h *Handler) List(ctx context.Context, in *ListVariantsRequest) (*ListVariantsResponse, error) {
	who, err := authn.UserFromContext(ctx)
	if err != nil {
		return nil, h.mapError(err)
	}
	versions, err := h.svc.List(ctx, who, in.IncludeArchived)
	if err != nil {
		return nil, h.mapError(err)
	}
	resp := &ListVariantsResponse{}
	resp.Body.Versions = make([]VariantVersionBody, 0, len(versions))
	for i := range versions {
		resp.Body.Versions = append(resp.Body.Versions, versionBody(&versions[i]))
	}
	return resp, nil
}

type SaveVariantVersionRequest struct {
	Body struct {
		Name                       string            `json:"name" minLength:"1" maxLength:"256" doc:"Names the variant; saving under an existing name adds its next version."`
		Harness                    harness.Name      `json:"harness"`
		Settings                   map[string]string `json:"settings" doc:"Settings of the harness alone; 422 for an unknown setting, one of the other harness, or a value that fails its pattern."`
		FleetInstructionsVersionID *string           `json:"fleet_instructions_version_id" format:"uuid" required:"true" nullable:"true" doc:"A fleet instructions version of this tenant, or null for none. Null is not the tenant's default."`
	}
}

type SaveVariantVersionResponse struct {
	Body VariantVersionBody
}

func (h *Handler) SaveVersion(ctx context.Context, in *SaveVariantVersionRequest) (*SaveVariantVersionResponse, error) {
	who, err := authn.UserFromContext(ctx)
	if err != nil {
		return nil, h.mapError(err)
	}
	req := &SaveRequest{Name: in.Body.Name, Harness: string(in.Body.Harness), Settings: in.Body.Settings}
	if in.Body.FleetInstructionsVersionID != nil {
		id, err := uuid.Parse(*in.Body.FleetInstructionsVersionID)
		if err != nil {
			return nil, huma.Error422UnprocessableEntity("fleet_instructions_version_id must be a uuid")
		}
		req.FleetInstructionsVersionID = &id
	}
	v, err := h.svc.Save(ctx, who, req)
	if err != nil {
		return nil, h.mapError(err)
	}
	return &SaveVariantVersionResponse{Body: versionBody(v)}, nil
}

type VariantIDPath struct {
	ID string `path:"id" format:"uuid"`
}

type ArchiveVariantVersionRequest struct{ VariantIDPath }

type ArchiveVariantVersionResponse struct {
	Body VariantVersionBody
}

func (h *Handler) ArchiveVersion(ctx context.Context, in *ArchiveVariantVersionRequest) (*ArchiveVariantVersionResponse, error) {
	return h.setVersionArchived(ctx, in.ID, true)
}

func (h *Handler) UnarchiveVersion(ctx context.Context, in *ArchiveVariantVersionRequest) (*ArchiveVariantVersionResponse, error) {
	return h.setVersionArchived(ctx, in.ID, false)
}

func (h *Handler) setVersionArchived(ctx context.Context, rawID string, archive bool) (*ArchiveVariantVersionResponse, error) {
	who, err := authn.UserFromContext(ctx)
	if err != nil {
		return nil, h.mapError(err)
	}
	id, err := uuid.Parse(rawID)
	if err != nil {
		return nil, huma.Error422UnprocessableEntity("id must be a uuid")
	}
	var v *Version
	if archive {
		v, err = h.svc.ArchiveVersion(ctx, who, id)
	} else {
		v, err = h.svc.UnarchiveVersion(ctx, who, id)
	}
	if err != nil {
		return nil, h.mapError(err)
	}
	return &ArchiveVariantVersionResponse{Body: versionBody(v)}, nil
}

type ArchiveVariantGroupRequest struct {
	Body struct {
		Name string `json:"name" minLength:"1" maxLength:"256" doc:"The name whose every version is archived or restored."`
	}
}

type ArchiveVariantGroupResponse struct {
	Body struct {
		Versions int `json:"versions" doc:"How many versions changed; 0 when the group was already in the state asked for."`
	}
}

func (h *Handler) ArchiveGroup(ctx context.Context, in *ArchiveVariantGroupRequest) (*ArchiveVariantGroupResponse, error) {
	return h.setGroupArchived(ctx, in, true)
}

func (h *Handler) UnarchiveGroup(ctx context.Context, in *ArchiveVariantGroupRequest) (*ArchiveVariantGroupResponse, error) {
	return h.setGroupArchived(ctx, in, false)
}

func (h *Handler) setGroupArchived(ctx context.Context, in *ArchiveVariantGroupRequest, archive bool) (*ArchiveVariantGroupResponse, error) {
	who, err := authn.UserFromContext(ctx)
	if err != nil {
		return nil, h.mapError(err)
	}
	var n int
	if archive {
		n, err = h.svc.ArchiveGroup(ctx, who, in.Body.Name)
	} else {
		n, err = h.svc.UnarchiveGroup(ctx, who, in.Body.Name)
	}
	if err != nil {
		return nil, h.mapError(err)
	}
	resp := &ArchiveVariantGroupResponse{}
	resp.Body.Versions = n
	return resp, nil
}

type SetAgentVariantRequest struct {
	ID   string `path:"id" format:"uuid"`
	Body struct {
		VersionID *string `json:"version_id" format:"uuid" required:"true" nullable:"true" doc:"The variant version to run the agent on, or null to take it off its variant."`
	}
}

type SetAgentVariantResponse struct{}

func (h *Handler) SetAgentVariant(ctx context.Context, in *SetAgentVariantRequest) (*SetAgentVariantResponse, error) {
	who, err := authn.UserFromContext(ctx)
	if err != nil {
		return nil, h.mapError(err)
	}
	agentID, err := uuid.Parse(in.ID)
	if err != nil {
		return nil, huma.Error422UnprocessableEntity("agent id must be a uuid")
	}
	var versionID *uuid.UUID
	if in.Body.VersionID != nil {
		id, err := uuid.Parse(*in.Body.VersionID)
		if err != nil {
			return nil, huma.Error422UnprocessableEntity("version_id must be a uuid")
		}
		versionID = &id
	}
	if err := h.svc.SetAgentVariant(ctx, who, agentID, versionID); err != nil {
		return nil, h.mapError(err)
	}
	return &SetAgentVariantResponse{}, nil
}

// RegisterRoutes mounts the board's variants surface. There is no update or
// delete route: a saved version never changes. Every operation is a
// signed-in user's, never an agent's, and the service lets only owners
// through.
func RegisterRoutes(api huma.API, h *Handler) {
	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireUser(),
		OperationID: "listVariantOptions",
		Method:      http.MethodGet,
		Path:        "/api/variants/options",
		Summary:     "The variant versions a ticket can be routed to",
		Description: "Any signed-in member's. Unarchived versions only, named and without settings.",
	}, h.ListOptions)
	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireUser(),
		OperationID: "listVariants",
		Method:      http.MethodGet,
		Path:        "/api/variants",
		Summary:     "The tenant's variant versions",
		Description: "Owners only (403 otherwise). Archived versions are hidden unless asked for.",
	}, h.List)
	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireUser(),
		OperationID: "saveVariantVersion",
		Method:      http.MethodPost,
		Path:        "/api/variants/versions",
		Summary:     "Save the next version of a named variant",
		Description: "Owners only (403 otherwise). Versions are immutable: this never changes an earlier one. " +
			"422 for a harness that is not registered, a setting that is not a valid setting of that harness, " +
			"or a fleet instructions version the tenant does not have.",
		DefaultStatus: http.StatusCreated,
	}, h.SaveVersion)
	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireUser(),
		OperationID: "archiveVariantVersion",
		Method:      http.MethodPost,
		Path:        "/api/variants/versions/{id}/archive",
		Summary:     "Hide a version from the list",
		Description: "Owners only (403 otherwise). The version is kept as it was.",
	}, h.ArchiveVersion)
	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireUser(),
		OperationID: "unarchiveVariantVersion",
		Method:      http.MethodPost,
		Path:        "/api/variants/versions/{id}/unarchive",
		Summary:     "List an archived version again",
		Description: "Owners only (403 otherwise).",
	}, h.UnarchiveVersion)
	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireUser(),
		OperationID: "archiveVariantGroup",
		Method:      http.MethodPost,
		Path:        "/api/variants/groups/archive",
		Summary:     "Hide every version of a name",
		Description: "Owners only (403 otherwise). 404 for an unknown name.",
	}, h.ArchiveGroup)
	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireUser(),
		OperationID: "unarchiveVariantGroup",
		Method:      http.MethodPost,
		Path:        "/api/variants/groups/unarchive",
		Summary:     "List every version of a name again",
		Description: "Owners only (403 otherwise). 404 for an unknown name.",
	}, h.UnarchiveGroup)
	huma.Register(api, huma.Operation{
		Metadata:      authn.RequireUser(),
		OperationID:   "setAgentVariant",
		Method:        http.MethodPut,
		Path:          "/api/agents/{id}/variant",
		DefaultStatus: http.StatusNoContent,
		Summary:       "Put an agent on a variant version, or take it off one",
		Description: "Owners only (403 otherwise), checked on every call; never an agent's, whatever its token. " +
			"The agent then runs on the version's settings alone and on its pinned fleet instructions, and a session " +
			"started before the change reads as stale. 422 for an archived version, which cannot be newly assigned; " +
			"404 for an agent or version the tenant does not have. A null version_id takes the agent off its variant.",
	}, h.SetAgentVariant)
}
