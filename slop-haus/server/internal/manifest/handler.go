package manifest

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/agent"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
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
		{Err: ErrMoveBlocked, Kind: httperr.Conflict},
		{Err: ErrSyncBlocked, Kind: httperr.Conflict},
		{Err: ErrSyncUnavailable, Kind: httperr.BadGateway, Warn: "manifest: sync not possible"},
		{Err: ErrInvalid, Kind: httperr.Invalid},
		httperr.SignIn,
	},
	Unexpected: "unexpected manifest error",
}

func (h *Handler) mapError(err error) error {
	return httperr.Map(h.logger, err, &errTable)
}

// ManifestBody is the tenant's declared workspace.
type ManifestBody struct {
	Repos []Repo `json:"repos"`
	// Revision counts the tenant's saves; a console proposing a change names the one it read.
	Revision  int64     `json:"revision"`
	UpdatedAt time.Time `json:"updated_at"`
}

func manifestBody(m *Manifest) *ManifestBody {
	if m == nil {
		return nil
	}
	return &ManifestBody{Repos: m.Repos, Revision: m.Revision, UpdatedAt: m.UpdatedAt}
}

// AgentWorkspaceBody is one agent's workspace as it last described itself,
// and what that comes to against the manifest.
type AgentWorkspaceBody struct {
	AgentID    string        `json:"agent_id"`
	AgentName  string        `json:"agent_name"`
	Status     Status        `json:"status" enum:"unknown,reported,matches,mismatch" doc:"unknown: the workspace has not reported (it has not run update.sh). reported: there is no manifest to compare with. matches or mismatch otherwise."`
	ReportedAt *time.Time    `json:"reported_at"`
	Layout     *agent.Layout `json:"layout"`
	Mismatches []Mismatch    `json:"mismatches"`
}

type WorkspaceBody struct {
	Manifest    *ManifestBody        `json:"manifest" doc:"Null while the tenant has not declared one: nothing is validated or enforced."`
	CanEdit     bool                 `json:"can_edit" doc:"Whether the signed-in user is an owner of the tenant, who alone may change the manifest."`
	Proposal    *Proposal            `json:"proposal" doc:"Built from the agents' reports, only while there is no manifest. Never stored."`
	Agents      []AgentWorkspaceBody `json:"agents"`
	Divergences []Divergence         `json:"divergences" doc:"One entry per manifest repo with a base_branch other than its default branch: how far the base branch is behind and ahead of the default branch on the code host, read when the page loads."`
}

type GetWorkspaceRequest struct{}

type GetWorkspaceResponse struct {
	Body WorkspaceBody
}

// GetWorkspace is everything the Workspace page shows.
func (h *Handler) GetWorkspace(ctx context.Context, _ *GetWorkspaceRequest) (*GetWorkspaceResponse, error) {
	who, err := authn.UserFromContext(ctx)
	if err != nil {
		return nil, h.mapError(err)
	}
	m, err := h.svc.Get(ctx)
	if err != nil {
		return nil, h.mapError(err)
	}
	states, err := h.svc.States(ctx, m)
	if err != nil {
		return nil, h.mapError(err)
	}
	canEdit, err := h.svc.CanEdit(ctx, who)
	if err != nil {
		return nil, h.mapError(err)
	}

	resp := &GetWorkspaceResponse{}
	resp.Body.Manifest = manifestBody(m)
	resp.Body.CanEdit = canEdit
	if m == nil {
		resp.Body.Proposal = h.svc.Proposal(ctx, states)
	}
	resp.Body.Divergences = h.svc.Divergences(ctx, m)
	resp.Body.Agents = make([]AgentWorkspaceBody, 0, len(states))
	for _, st := range states {
		body := AgentWorkspaceBody{
			AgentID: st.Agent.ID.String(), AgentName: st.Agent.Name, Status: st.Status,
			Mismatches: append([]Mismatch{}, st.Mismatches...),
		}
		if st.Report != nil {
			at, layout := st.Report.ReportedAt, st.Report.Layout
			body.ReportedAt, body.Layout = &at, &layout
		}
		resp.Body.Agents = append(resp.Body.Agents, body)
	}
	return resp, nil
}

// SlotRepoBody is what a slot is given of one manifest repo.
type SlotRepoBody struct {
	Path   string `json:"path"`
	Remote string `json:"remote"`
	Base   string `json:"base" doc:"The branch agents cut from; empty means the repo's default branch."`
}

type SlotManifestBody struct {
	Revision int64          `json:"revision" doc:"The manifest's revision; 0 while none is declared."`
	Repos    []SlotRepoBody `json:"repos"`
}

type GetManifestForSlotResponse struct {
	Body SlotManifestBody
}

func (h *Handler) GetManifestForSlot(ctx context.Context, _ *struct{}) (*GetManifestForSlotResponse, error) {
	m, err := h.svc.Get(ctx)
	if err != nil {
		return nil, h.mapError(err)
	}
	body := SlotManifestBody{Repos: []SlotRepoBody{}}
	if m != nil {
		body.Revision = m.Revision
		for _, r := range m.Repos {
			body.Repos = append(body.Repos, SlotRepoBody{Path: r.Path, Remote: r.Remote, Base: r.BaseBranch})
		}
	}
	return &GetManifestForSlotResponse{Body: body}, nil
}

type SetManifestRequest struct {
	Body struct {
		Repos []Repo `json:"repos" minItems:"1" maxItems:"50"`
	}
}

type SetManifestResponse struct {
	Body SetManifestBody
}

// SetManifestBody is the saved manifest and what making its base branches came to.
type SetManifestBody struct {
	ManifestBody
	BaseBranches []BaseBranchResult `json:"base_branches" doc:"One entry per repo with a base_branch: whether it exists on the code host now. A failure leaves the manifest saved; save again to retry."`
}

// SetManifest adopts or replaces the manifest.
func (h *Handler) SetManifest(ctx context.Context, in *SetManifestRequest) (*SetManifestResponse, error) {
	who, err := authn.UserFromContext(ctx)
	if err != nil {
		return nil, h.mapError(err)
	}
	m, err := h.svc.Set(ctx, who, in.Body.Repos)
	if err != nil {
		return nil, h.mapError(err)
	}
	results := h.svc.EnsureBaseBranches(ctx, m)
	if results == nil {
		results = []BaseBranchResult{}
	}
	return &SetManifestResponse{Body: SetManifestBody{ManifestBody: *manifestBody(m), BaseBranches: results}}, nil
}

type ClearManifestRequest struct{}

// ClearManifest takes the manifest away.
func (h *Handler) ClearManifest(ctx context.Context, _ *ClearManifestRequest) (*struct{}, error) {
	who, err := authn.UserFromContext(ctx)
	if err != nil {
		return nil, h.mapError(err)
	}
	if err := h.svc.Clear(ctx, who); err != nil {
		return nil, h.mapError(err)
	}
	return nil, nil
}

type MoveRepoRequest struct {
	Body struct {
		FromPath string `json:"from_path" doc:"The repo's current path in the manifest."`
		ToPath   string `json:"to_path" doc:"Where it goes: one directory name."`
		DryRun   bool   `json:"dry_run,omitempty" doc:"List every change and write nothing."`
	}
}

type MoveRepoResponse struct {
	Body struct {
		Applied bool `json:"applied" doc:"False for a dry run."`
		MovePlan
	}
}

// MoveRepo moves a repo and rewrites the scopes under it.
func (h *Handler) MoveRepo(ctx context.Context, in *MoveRepoRequest) (*MoveRepoResponse, error) {
	who, err := authn.UserFromContext(ctx)
	if err != nil {
		return nil, h.mapError(err)
	}
	plan, err := h.svc.Move(ctx, who, MoveRequest{From: in.Body.FromPath, To: in.Body.ToPath}, in.Body.DryRun)
	if err != nil {
		return nil, h.mapError(err)
	}
	resp := &MoveRepoResponse{}
	resp.Body.Applied = !in.Body.DryRun
	resp.Body.MovePlan = *plan
	return resp, nil
}

type SyncRepoRequest struct {
	Body struct {
		Path string `json:"path" doc:"The manifest repo's path."`
	}
}

type SyncRepoResponse struct {
	Body SyncResult
}

// SyncRepo merges a repo's default branch into its base branch.
func (h *Handler) SyncRepo(ctx context.Context, in *SyncRepoRequest) (*SyncRepoResponse, error) {
	who, err := authn.UserFromContext(ctx)
	if err != nil {
		return nil, h.mapError(err)
	}
	res, err := h.svc.Sync(ctx, who, in.Body.Path)
	if err != nil {
		return nil, h.mapError(err)
	}
	return &SyncRepoResponse{Body: *res}, nil
}

// RegisterRoutes mounts the Workspace page's REST surface. Every operation
// is a signed-in user's, never an agent's: the manifest decides what an
// agent is told to do at a claim, so one able to write it could stand any
// other agent down. The one exception is the read for slots, below.
func RegisterRoutes(api huma.API, h *Handler) {
	huma.Register(api, huma.Operation{
		// Like the update script, which already gives a slot the repos: a
		// supervisor reconciling a slot's checkouts reads them here.
		Metadata:    authn.RequireAgentOrSupervisor(),
		OperationID: "getWorkspaceManifestForSlot",
		Method:      http.MethodGet,
		Path:        "/api/workspace/manifest/slot",
		Summary:     "The workspace manifest's revision and repos, for a slot to reconcile its checkouts against",
		Description: "Read-only. Only each repo's path, remote and base branch (empty: the repo's default branch), " +
			"nothing else the manifest holds. Empty repos at revision 0 until an owner adopts a manifest.",
	}, h.GetManifestForSlot)
	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireUser(),
		OperationID: "getWorkspace",
		Method:      http.MethodGet,
		Path:        "/api/workspace",
		Summary:     "The tenant's workspace manifest, and what each enabled agent's workspace says it looks like",
		Description: "The manifest is null until an owner adopts one, and nothing is validated or enforced while it is. " +
			"With no manifest, the proposal is what the agents' reports suggest, naming agents that disagree. " +
			"With one, each agent's status is matches, mismatch (which path, what was found there) or unknown " +
			"(it has not reported). Reports are self-asserted by the agent's machine; remotes carry no credentials.",
	}, h.GetWorkspace)
	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireUser(),
		OperationID: "setWorkspaceManifest",
		Method:      http.MethodPut,
		Path:        "/api/workspace/manifest",
		Summary:     "Adopt or replace the workspace manifest",
		Description: "Owners only (403 otherwise). Changes nothing about open tickets: their scopes and status are untouched.",
	}, h.SetManifest)
	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireUser(),
		OperationID: "moveWorkspaceRepo",
		Method:      http.MethodPost,
		Path:        "/api/workspace/manifest/move",
		Summary:     "Move a manifest repo to another path, rewriting the scopes of every open ticket and intake source under it",
		Description: "Owners only (403 otherwise). One transaction: the manifest entry, the scopes of every non-terminal ticket " +
			"(done and cancelled are left as they were) and intake sources' paths; each ticket gets a comment. " +
			"Refused with 409 naming the tickets while any ticket under the repo has a live lease or holds the integration lock. " +
			"dry_run lists every change and writes nothing.",
	}, h.MoveRepo)
	huma.Register(api, huma.Operation{
		Metadata:    authn.RequireUser(),
		OperationID: "syncWorkspaceRepo",
		Method:      http.MethodPost,
		Path:        "/api/workspace/manifest/sync",
		Summary:     "Merge a repo's default branch into its base branch on the code host",
		Description: "Owners only (403 otherwise); audited. Refused with 409 naming the holder while an agent holds the base branch's integration lock. " +
			"A clean merge reports the new head (outcome merged, or up_to_date when there was nothing to merge). " +
			"A conflict changes nothing and files a ticket in preparing, named in ticket_id (outcome conflict). " +
			"Getting the base branch into the default branch is a pull request on the code host, by a person.",
	}, h.SyncRepo)
	huma.Register(api, huma.Operation{
		Metadata:      authn.RequireUser(),
		OperationID:   "clearWorkspaceManifest",
		Method:        http.MethodDelete,
		Path:          "/api/workspace/manifest",
		Summary:       "Remove the workspace manifest, so nothing about the layout is enforced",
		Description:   "Owners only (403 otherwise).",
		DefaultStatus: http.StatusNoContent,
	}, h.ClearManifest)
}
