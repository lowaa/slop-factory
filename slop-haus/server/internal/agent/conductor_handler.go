package agent

import (
	"context"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
)

// ConductorBody is the wire representation of a conductor. There is no
// credential in it: like a worker's, a conductor's is minted by the device
// sign-in its owner approves.
type ConductorBody struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	CreatedAt  time.Time  `json:"created_at"`
	DisabledAt *time.Time `json:"disabled_at"`
	ArchivedAt *time.Time `json:"archived_at"`
	// ReplacedBy is the agent that took over from it when it was
	// re-authorized (a conductor is never itself a replacement).
	ReplacedBy *AgentRefBody `json:"replaced_by"`
	OwnerID    *string       `json:"owner_user_id" doc:"null when the owner's account was deleted"`
	// OwnerDeleted is true when the owner's account was deleted: the
	// conductor is then nobody's to use, and owner_user_id is null.
	OwnerDeleted bool `json:"owner_user_id_deleted,omitempty"`
	// OwnedByMe says whether the caller may use it. Everyone else who sees
	// it (a tenant owner) may only disable it.
	OwnedByMe bool `json:"owned_by_me"`
	// RunBySupervisorID is the supervisor that runs it, for a Remote Control
	// added for a supervisor to run; removing one is removing its entry
	// there, not archiving it.
	RunBySupervisorID *string `json:"run_by_supervisor_id,omitempty" doc:"the supervisor that runs it; absent for one run by hand"`
}

func conductorBody(a *Agent, viewer *authn.User) ConductorBody {
	body := ConductorBody{ID: a.ID.String(), Name: a.Name, CreatedAt: a.CreatedAt, DisabledAt: a.DisabledAt, ArchivedAt: a.ArchivedAt, ReplacedBy: refToBody(a.ReplacedBy), OwnerDeleted: a.OwnerUserDeleted}
	if a.OwnerUserID != nil {
		id := a.OwnerUserID.String()
		body.OwnerID = &id
		body.OwnedByMe = a.OwnedBy(viewer.ID)
	}
	if a.RunBySupervisorID != nil {
		id := a.RunBySupervisorID.String()
		body.RunBySupervisorID = &id
	}
	return body
}

func conductorBodies(agents []Agent, viewer *authn.User) []ConductorBody {
	bodies := make([]ConductorBody, len(agents))
	for i := range agents {
		bodies[i] = conductorBody(&agents[i], viewer)
	}
	return bodies
}

// -- Register --

type RegisterConductorRequest struct {
	Body struct {
		Name string `json:"name" doc:"Conductor name, unique across the fleet"`
	}
}

// RegisterConductor registers a conductor owned by the caller and returns
// its setup script, as registering a worker does.
func (h *Handler) RegisterConductor(ctx context.Context, input *RegisterConductorRequest) (*SetupScriptResponse, error) {
	who := authn.PersonFromContext(ctx)
	server, err := BaseURLFrom(ctx)
	if err != nil {
		return nil, huma.Error422UnprocessableEntity(err.Error())
	}
	inputs, err := h.briefingInputs(ctx, nil)
	if err != nil {
		return nil, err
	}
	created, err := h.conductors.Register(ctx, who, input.Body.Name)
	if err != nil {
		return nil, h.mapError(err)
	}
	return h.registeredScript(ctx, created, server, inputs)
}

// -- List --

type ListConductorsResponse struct {
	Body struct {
		// Conductors are the caller's own.
		Conductors []ConductorBody `json:"conductors"`
		// Others are everyone else's, listed for the tenant's owner to
		// disable and empty for anyone else.
		Others []ConductorBody `json:"others"`
	}
}

type ListConductorsRequest struct {
	IncludeArchived bool `query:"include_archived" doc:"Also list archived conductors."`
}

func (h *Handler) ListConductors(ctx context.Context, input *ListConductorsRequest) (*ListConductorsResponse, error) {
	who := authn.PersonFromContext(ctx)
	own, err := h.conductors.ListOwned(ctx, who, input.IncludeArchived)
	if err != nil {
		return nil, h.mapError(err)
	}
	others, err := h.conductors.ListOthers(ctx, who, input.IncludeArchived)
	if err != nil {
		return nil, h.mapError(err)
	}
	resp := &ListConductorsResponse{}
	resp.Body.Conductors = conductorBodies(own, who)
	resp.Body.Others = conductorBodies(others, who)
	return resp, nil
}

// -- Directory --

// ConductorDirectoryEntryBody is what any member may know about a conductor.
// It is deliberately narrow: nothing about the conversations on it beyond
// whether one is live.
type ConductorDirectoryEntryBody struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	OwnerID    *string    `json:"owner_user_id" doc:"null when the owner's account was deleted"`
	OwnerName  *string    `json:"owner_display_name" doc:"null when the owner's account was deleted"`
	DisabledAt *time.Time `json:"disabled_at"`
	ArchivedAt *time.Time `json:"archived_at"`
	// HostSeenAt is when its host last connected; null if it never has.
	HostSeenAt        *time.Time `json:"host_seen_at"`
	HasLiveSession    bool       `json:"has_live_session" doc:"A conversation is starting or ready on it"`
	RunBySupervisorID *string    `json:"run_by_supervisor_id,omitempty" doc:"the supervisor that runs it; absent for one run by hand. Removing it is removing it from that supervisor."`
}

type ConductorDirectoryResponse struct {
	Body struct {
		Conductors []ConductorDirectoryEntryBody `json:"conductors"`
	}
}

func (h *Handler) ConductorDirectory(ctx context.Context, input *ListConductorsRequest) (*ConductorDirectoryResponse, error) {
	who := authn.PersonFromContext(ctx)
	entries, err := h.conductors.ListDirectory(ctx, who, input.IncludeArchived)
	if err != nil {
		return nil, h.mapError(err)
	}
	resp := &ConductorDirectoryResponse{}
	resp.Body.Conductors = make([]ConductorDirectoryEntryBody, len(entries))
	for i := range entries {
		a := &entries[i].Agent
		body := ConductorDirectoryEntryBody{ID: a.ID.String(), Name: a.Name, DisabledAt: a.DisabledAt, ArchivedAt: a.ArchivedAt,
			HostSeenAt: entries[i].HostSeenAt, HasLiveSession: entries[i].HasLiveSession}
		if a.OwnerUserID != nil {
			id := a.OwnerUserID.String()
			body.OwnerID = &id
			body.OwnerName = entries[i].OwnerName
		}
		if a.RunBySupervisorID != nil {
			id := a.RunBySupervisorID.String()
			body.RunBySupervisorID = &id
		}
		resp.Body.Conductors[i] = body
	}
	return resp, nil
}

// -- Set disabled --

type SetConductorDisabledRequest struct {
	ID   string `path:"id"`
	Body SetAgentDisabledBody
}

type ConductorResponse struct {
	Body ConductorBody
}

func (h *Handler) SetConductorDisabled(ctx context.Context, input *SetConductorDisabledRequest) (*ConductorResponse, error) {
	who := authn.PersonFromContext(ctx)
	id, err := parseAgentPathID(input.ID)
	if err != nil {
		return nil, err
	}
	updated, err := h.conductors.SetDisabled(ctx, who, id, input.Body.Disabled)
	if err != nil {
		return nil, h.mapError(err)
	}
	return &ConductorResponse{Body: conductorBody(updated, who)}, nil
}

// -- Archive --

func (h *Handler) ArchiveConductor(ctx context.Context, input *SetAgentArchivedRequest) (*ConductorResponse, error) {
	return h.setConductorArchived(ctx, input, true)
}

func (h *Handler) UnarchiveConductor(ctx context.Context, input *SetAgentArchivedRequest) (*ConductorResponse, error) {
	return h.setConductorArchived(ctx, input, false)
}

func (h *Handler) setConductorArchived(ctx context.Context, input *SetAgentArchivedRequest, archived bool) (*ConductorResponse, error) {
	who := authn.PersonFromContext(ctx)
	id, err := parseAgentPathID(input.ID)
	if err != nil {
		return nil, err
	}
	updated, err := h.conductors.SetArchived(ctx, who, id, archived)
	if err != nil {
		return nil, h.mapError(err)
	}
	return &ConductorResponse{Body: conductorBody(updated, who)}, nil
}

// -- Rotate --

// RotateConductorSetupScript signs out every workspace acting as the
// conductor and returns a new setup script for it. Only its owner: the
// credential it issues is the conductor.
func (h *Handler) RotateConductorSetupScript(ctx context.Context, input *RotateSetupScriptRequest) (*SetupScriptResponse, error) {
	who := authn.PersonFromContext(ctx)
	server, err := BaseURLFrom(ctx)
	if err != nil {
		return nil, huma.Error422UnprocessableEntity(err.Error())
	}
	id, err := parseAgentPathID(input.ID)
	if err != nil {
		return nil, err
	}
	owned, err := h.conductors.AuthorizeOwner(ctx, who, id)
	if err != nil {
		return nil, h.mapError(err)
	}
	inputs, err := h.briefingInputs(ctx, &id)
	if err != nil {
		return nil, err
	}
	// Rendered before anything is revoked, as for a worker.
	script, err := h.setupScript(ctx, owned.Agent(), server, inputs)
	if err != nil {
		return nil, err
	}
	if _, err := h.conductors.SignOutWorkspaces(ctx, owned); err != nil {
		return nil, h.mapError(err)
	}
	return scriptResponse(owned.Agent().Name, script), nil
}

func registerConductorRoutes(api huma.API, h *Handler) {
	huma.Register(api, huma.Operation{
		OperationID: "registerConductor",
		Metadata:    authn.RequireUser(),
		Method:      http.MethodPost,
		Path:        "/api/conductors",
		Summary:     "Register a conductor owned by the signed-in user and return a shell script that configures a workspace for it",
		Description: "The caller becomes the conductor's owner, for good: only they may use it. Needs a recent sign-in " +
			"(403 reauth_required otherwise). The script carries no credential; its device sign-in is approved by the owner.",
		DefaultStatus: http.StatusCreated,
		Responses:     shellScriptResponses(http.StatusCreated),
		Middlewares:   huma.Middlewares{BaseURLMiddleware},
	}, h.RegisterConductor)
	huma.Register(api, huma.Operation{
		OperationID: "listConductors",
		Metadata:    authn.RequireUser(),
		Method:      http.MethodGet,
		Path:        "/api/conductors",
		Summary:     "List the caller's own conductors",
		Description: "Also, for the tenant's owner, the conductors other people registered: they may disable those but not use them.",
	}, h.ListConductors)
	huma.Register(api, huma.Operation{
		OperationID: "conductorDirectory",
		Metadata:    authn.RequireUser(),
		Method:      http.MethodGet,
		Path:        "/api/conductors/directory",
		Summary:     "List every conductor in the tenant, for any member",
		Description: "Read-only: each conductor's owner, when its host last connected and whether a conversation is live on it. " +
			"Never anything about the conversations themselves. Archived ones only with include_archived=true.",
	}, h.ConductorDirectory)
	huma.Register(api, huma.Operation{
		OperationID: "setConductorDisabled",
		Metadata:    authn.RequireUser(),
		Method:      http.MethodPatch,
		Path:        "/api/conductors/{id}",
		Summary:     "Enable or disable a conductor",
		Description: "Its owner or the tenant's owner may. Anyone else gets 403.",
	}, h.SetConductorDisabled)
	huma.Register(api, huma.Operation{
		OperationID: "archiveConductor",
		Metadata:    authn.RequireUser(),
		Method:      http.MethodPost,
		Path:        "/api/conductors/{id}/archive",
		Summary:     "Archive a disabled conductor",
		Description: "Its owner or the tenant's owner may. It drops out of the conductor list, which returns it with " +
			"include_archived=true. 409 for a conductor that is still enabled.",
	}, h.ArchiveConductor)
	huma.Register(api, huma.Operation{
		OperationID: "unarchiveConductor",
		Metadata:    authn.RequireUser(),
		Method:      http.MethodPost,
		Path:        "/api/conductors/{id}/unarchive",
		Summary:     "Unarchive a conductor",
		Description: "Its owner or the tenant's owner may. It stays disabled.",
	}, h.UnarchiveConductor)
	huma.Register(api, huma.Operation{
		OperationID: "rotateConductorSetupScript",
		Metadata:    authn.RequireUser(),
		Method:      http.MethodPost,
		Path:        "/api/conductors/{id}/setup_script",
		Summary:     "Sign out a conductor's workspaces and return a new setup script for it",
		Description: "Its owner only. 409 for a disabled conductor.",
		Responses:   shellScriptResponses(http.StatusOK),
		Middlewares: huma.Middlewares{BaseURLMiddleware},
	}, h.RotateConductorSetupScript)
}
