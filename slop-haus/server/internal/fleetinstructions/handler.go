package fleetinstructions

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/httperr"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/workspace"
)

// Handler is a thin HTTP wrapper over Service, like the other handlers.
type Handler struct {
	svc      *Service
	codeHost CodeHost
	logger   *slog.Logger
}

// CodeHost is whether the tenant has a code host credential, which decides
// how the briefings say a change lands. It is true only for a credential whose
// key opens (githubapp.Service.Connected), so a damaged one gets the repo-own-tool briefing.
type CodeHost interface {
	Connected(ctx context.Context) (bool, error)
}

func NewHandler(svc *Service, codeHost CodeHost, logHandler slog.Handler) *Handler {
	return &Handler{svc: svc, codeHost: codeHost, logger: slog.New(logHandler)}
}

var errTable = httperr.Table{
	Rules: []httperr.Rule{
		{Err: ErrNotOwner, Kind: httperr.Forbidden},
		{Err: ErrInvalid, Kind: httperr.Invalid},
		{Err: ErrConflict, Kind: httperr.Conflict},
		{Err: ErrNotFound, Kind: httperr.NotFound, Message: "no such version"},
		httperr.SignIn,
	},
	Unexpected: "unexpected fleet instructions error",
}

func (h *Handler) mapError(err error) error {
	return httperr.Map(h.logger, err, &errTable)
}

type VersionBody struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	Version   int     `json:"version"`
	Kind      string  `json:"kind" enum:"fleet,steward,sensitive_words"`
	Body      string  `json:"body"`
	CreatedBy *string `json:"created_by"`
	// CreatedByDeleted is true when the author's account was deleted:
	// created_by is then null.
	CreatedByDeleted bool      `json:"created_by_deleted,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
	// Archived versions are listed only when asked for.
	Archived   bool       `json:"archived"`
	ArchivedAt *time.Time `json:"archived_at" doc:"When the version was archived; null while it is listed."`
}

func versionBody(v *Version) VersionBody {
	out := VersionBody{ID: v.ID.String(), Name: v.Name, Version: v.Version, Kind: string(v.Kind), Body: v.Body, CreatedAt: v.CreatedAt, Archived: v.ArchivedAt != nil, ArchivedAt: v.ArchivedAt, CreatedByDeleted: v.CreatedByDeleted}
	if v.CreatedBy != nil {
		id := v.CreatedBy.String()
		out.CreatedBy = &id
	}
	return out
}

type FleetInstructionsBody struct {
	Versions  []VersionBody `json:"versions" doc:"Every saved version, by name and then newest first."`
	DefaultID *string       `json:"default_id" doc:"The version agents are given; null while none is chosen, when briefings carry no fleet instructions."`
	CanEdit   bool          `json:"can_edit" doc:"Whether the signed-in user is an owner of the tenant, who alone may change them."`
	MaxBytes  int           `json:"max_body_bytes"`
}

// KindQuery is the optional kind of every operation; absent is fleet, so the
// endpoints behave as they did before there were kinds.
type KindQuery struct {
	Kind string `query:"kind" required:"false" enum:"fleet,steward,sensitive_words" doc:"Which texts: fleet (the default), steward or sensitive_words."`
}

type GetRequest struct {
	KindQuery
	IncludeArchived bool `query:"include_archived" required:"false" doc:"Also list archived versions, which are hidden by default."`
}

type GetResponse struct {
	Body FleetInstructionsBody
}

func (h *Handler) Get(ctx context.Context, in *GetRequest) (*GetResponse, error) {
	who, err := authn.UserFromContext(ctx)
	if err != nil {
		return nil, h.mapError(err)
	}
	kind, err := ParseKind(in.Kind)
	if err != nil {
		return nil, h.mapError(err)
	}
	t, err := h.svc.Load(ctx, kind, in.IncludeArchived)
	if err != nil {
		return nil, h.mapError(err)
	}
	canEdit, err := h.svc.CanEdit(ctx, who)
	if err != nil {
		return nil, h.mapError(err)
	}
	resp := &GetResponse{}
	resp.Body = FleetInstructionsBody{Versions: make([]VersionBody, 0, len(t.Versions)), CanEdit: canEdit, MaxBytes: MaxBodyBytes}
	for i := range t.Versions {
		resp.Body.Versions = append(resp.Body.Versions, versionBody(&t.Versions[i]))
	}
	if t.DefaultID != nil {
		id := t.DefaultID.String()
		resp.Body.DefaultID = &id
	}
	return resp, nil
}

// AgentNamePlaceholder stands for the agent's name in the briefings shown,
// which are the same for every agent but for that.
const AgentNamePlaceholder = "<agent-name>"

type BriefingFile struct {
	Name     string `json:"name" doc:"Which file: headless or pi."`
	Path     string `json:"path" doc:"Where the file lands in a workspace."`
	Protocol string `json:"protocol" doc:"The file up to where the fleet instructions start."`
	Fleet    string `json:"fleet" doc:"The fleet instructions section as agents read it, wrapper paragraph and quoting included; empty when none apply. The file is protocol followed by fleet."`
}

type BriefingsBody struct {
	Files []BriefingFile `json:"files"`
}

type BriefingsRequest struct {
	VersionID string `query:"version_id" format:"uuid" required:"false" doc:"The saved version to render the fleet instructions from; none are included when omitted."`
}

type BriefingsResponse struct {
	Body BriefingsBody
}

// Briefings renders what the current server would write to a workspace, by
// the same code that writes it. Nothing here is secret: the files sit in
// plain text in every checkout.
func (h *Handler) Briefings(ctx context.Context, in *BriefingsRequest) (*BriefingsResponse, error) {
	if _, err := authn.UserFromContext(ctx); err != nil {
		return nil, h.mapError(err)
	}
	fleet, err := h.fleetFor(ctx, in.VersionID)
	if err != nil {
		return nil, h.mapError(err)
	}
	changeTools := false
	if h.codeHost != nil {
		if changeTools, err = h.codeHost.Connected(ctx); err != nil {
			return nil, h.mapError(err)
		}
	}
	docs := workspace.Documents(AgentNamePlaceholder, fleet, changeTools)
	file := func(name, path string, d *workspace.Document) BriefingFile {
		return BriefingFile{Name: name, Path: path, Protocol: d.Protocol, Fleet: d.Fleet}
	}
	return &BriefingsResponse{Body: BriefingsBody{Files: []BriefingFile{
		file("headless", workspace.HeadlessBriefing, docs.Headless),
	}}}, nil
}

// fleetFor is the version asked for, or nil -- no fleet instructions -- when
// none is. The caller says which, so that showing "none" and showing the
// default are both possible.
func (h *Handler) fleetFor(ctx context.Context, versionID string) (*workspace.FleetInstructions, error) {
	if versionID == "" {
		return nil, nil
	}
	id, err := uuid.Parse(versionID)
	if err != nil {
		return nil, ErrNotFound
	}
	t, err := h.svc.Load(ctx, KindFleet, true)
	if err != nil {
		return nil, err
	}
	for i := range t.Versions {
		if v := &t.Versions[i]; v.ID == id {
			return &workspace.FleetInstructions{Name: v.Name, Version: v.Version, Body: v.Body}, nil
		}
	}
	return nil, ErrNotFound
}

type SaveVersionRequest struct {
	KindQuery
	Body struct {
		Name        string `json:"name" minLength:"1" maxLength:"256" doc:"Names the text; saving under an existing name adds its next version."`
		Body        string `json:"body" maxLength:"80000" doc:"The instructions. At most 20000 bytes after trimming, and not empty except for a sensitive_words list (422 otherwise)."`
		MakeDefault bool   `json:"make_default" doc:"Also make this version the one agents are given."`
	}
}

type SaveVersionResponse struct {
	Body VersionBody
}

func (h *Handler) SaveVersion(ctx context.Context, in *SaveVersionRequest) (*SaveVersionResponse, error) {
	who, err := authn.UserFromContext(ctx)
	if err != nil {
		return nil, h.mapError(err)
	}
	kind, err := ParseKind(in.Kind)
	if err != nil {
		return nil, h.mapError(err)
	}
	v, err := h.svc.Save(ctx, who, kind, in.Body.Name, in.Body.Body, in.Body.MakeDefault)
	if err != nil {
		return nil, h.mapError(err)
	}
	return &SaveVersionResponse{Body: versionBody(v)}, nil
}

type SetDefaultRequest struct {
	KindQuery
	Body struct {
		VersionID *string `json:"version_id" format:"uuid" required:"true" nullable:"true" doc:"The version to give agents, or null for none."`
	}
}

func (h *Handler) SetDefault(ctx context.Context, in *SetDefaultRequest) (*struct{}, error) {
	who, err := authn.UserFromContext(ctx)
	if err != nil {
		return nil, h.mapError(err)
	}
	kind, err := ParseKind(in.Kind)
	if err != nil {
		return nil, h.mapError(err)
	}
	var id *uuid.UUID
	if in.Body.VersionID != nil {
		parsed, err := uuid.Parse(*in.Body.VersionID)
		if err != nil {
			return nil, huma.Error422UnprocessableEntity("version_id must be a uuid")
		}
		id = &parsed
	}
	if err := h.svc.SetDefault(ctx, who, kind, id); err != nil {
		return nil, h.mapError(err)
	}
	return nil, nil
}

type VersionIDPath struct {
	ID string `path:"id" format:"uuid"`
}

type ArchiveVersionRequest struct{ VersionIDPath }

type ArchiveVersionResponse struct {
	Body VersionBody
}

func (h *Handler) ArchiveVersion(ctx context.Context, in *ArchiveVersionRequest) (*ArchiveVersionResponse, error) {
	return h.setVersionArchived(ctx, in.ID, true)
}

func (h *Handler) UnarchiveVersion(ctx context.Context, in *ArchiveVersionRequest) (*ArchiveVersionResponse, error) {
	return h.setVersionArchived(ctx, in.ID, false)
}

func (h *Handler) setVersionArchived(ctx context.Context, rawID string, archive bool) (*ArchiveVersionResponse, error) {
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
	return &ArchiveVersionResponse{Body: versionBody(v)}, nil
}

type ArchiveGroupRequest struct {
	KindQuery
	Body struct {
		Name string `json:"name" minLength:"1" maxLength:"256" doc:"The name whose every version is archived or restored."`
	}
}

type ArchiveGroupResponse struct {
	Body struct {
		Versions int `json:"versions" doc:"How many versions changed; 0 when the group was already in the state asked for."`
	}
}

func (h *Handler) ArchiveGroup(ctx context.Context, in *ArchiveGroupRequest) (*ArchiveGroupResponse, error) {
	return h.setGroupArchived(ctx, in, true)
}

func (h *Handler) UnarchiveGroup(ctx context.Context, in *ArchiveGroupRequest) (*ArchiveGroupResponse, error) {
	return h.setGroupArchived(ctx, in, false)
}

func (h *Handler) setGroupArchived(ctx context.Context, in *ArchiveGroupRequest, archive bool) (*ArchiveGroupResponse, error) {
	who, err := authn.UserFromContext(ctx)
	if err != nil {
		return nil, h.mapError(err)
	}
	var n int
	kind, kerr := ParseKind(in.Kind)
	if kerr != nil {
		return nil, h.mapError(kerr)
	}
	if archive {
		n, err = h.svc.ArchiveGroup(ctx, who, kind, in.Body.Name)
	} else {
		n, err = h.svc.UnarchiveGroup(ctx, who, kind, in.Body.Name)
	}
	if err != nil {
		return nil, h.mapError(err)
	}
	resp := &ArchiveGroupResponse{}
	resp.Body.Versions = n
	return resp, nil
}

// RegisterRoutes mounts the board's fleet instructions surface. Every
// operation is a signed-in user's, never an agent's: the text lands in every
// agent's prompt, so an agent able to write it could steer all the others.
// Agents get the default only inside the scripts the workspace routes render.
func RegisterRoutes(api huma.API, h *Handler) {
	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireUser(),
		OperationID: "getFleetInstructions",
		Method:      http.MethodGet,
		Path:        "/api/fleet_instructions",
		Summary:     "The tenant's fleet instructions: every saved version and which one agents are given",
	}, h.Get)
	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireUser(),
		OperationID: "getFleetBriefings",
		Method:      http.MethodGet,
		Path:        "/api/fleet_instructions/briefings",
		Summary:     "The briefing files agents receive, with a saved version's fleet instructions in them",
		Description: "What the current server would write: a workspace not yet updated may hold an older copy. " +
			"slop-haus's part only; agents also follow the repo's own CLAUDE.md and the MCP tool descriptions and notices. " +
			"The agent's name is shown as " + AgentNamePlaceholder + ".",
	}, h.Briefings)
	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireUser(),
		OperationID: "saveFleetInstructionsVersion",
		Method:      http.MethodPost,
		Path:        "/api/fleet_instructions/versions",
		Summary:     "Save the next version of a named text",
		Description: "Owners only (403 otherwise). Versions are immutable: this never changes an earlier one. " +
			"Agents pick the default up the next time their workspace is updated.",
		DefaultStatus: http.StatusCreated,
	}, h.SaveVersion)
	huma.Register(api, huma.Operation{
		Metadata:      authn.RequireUser(),
		OperationID:   "setFleetInstructionsDefault",
		Method:        http.MethodPut,
		Path:          "/api/fleet_instructions/default",
		Summary:       "Choose which version agents are given, or none",
		Description:   "Owners only (403 otherwise). 404 for a version the tenant does not have.",
		DefaultStatus: http.StatusNoContent,
	}, h.SetDefault)
	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireUser(),
		OperationID: "archiveFleetInstructionsVersion",
		Method:      http.MethodPost,
		Path:        "/api/fleet_instructions/versions/{id}/archive",
		Summary:     "Hide a version from the list",
		Description: "Owners only (403 otherwise). The text is kept. 409 for the current default; unset or change it first.",
	}, h.ArchiveVersion)
	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireUser(),
		OperationID: "unarchiveFleetInstructionsVersion",
		Method:      http.MethodPost,
		Path:        "/api/fleet_instructions/versions/{id}/unarchive",
		Summary:     "List an archived version again",
		Description: "Owners only (403 otherwise).",
	}, h.UnarchiveVersion)
	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireUser(),
		OperationID: "archiveFleetInstructionsGroup",
		Method:      http.MethodPost,
		Path:        "/api/fleet_instructions/groups/archive",
		Summary:     "Hide every version of a name",
		Description: "Owners only (403 otherwise). 404 for an unknown name. 409, archiving nothing, when the default is one of the versions.",
	}, h.ArchiveGroup)
	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireUser(),
		OperationID: "unarchiveFleetInstructionsGroup",
		Method:      http.MethodPost,
		Path:        "/api/fleet_instructions/groups/unarchive",
		Summary:     "List every version of a name again",
		Description: "Owners only (403 otherwise). 404 for an unknown name.",
	}, h.UnarchiveGroup)
}
