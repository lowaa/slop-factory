package agent

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
)

// DesiredAgentBody is one agent of a supervisor's desired-state document.
// It carries a name and a kind, never a path or a command: the supervisor
// derives where an agent lives from its name.
type DesiredAgentBody struct {
	ID               string `json:"id,omitempty" doc:"Absent when adding an agent; the server assigns it."`
	Kind             string `json:"kind" enum:"slot,steward,remote_control"`
	Name             string `json:"name" maxLength:"100" doc:"Letters, digits, '.', '_' or '-', starting with a letter or digit."`
	Harness          string `json:"harness,omitempty" maxLength:"32" doc:"Applies to new runs only. Defaults to claude."`
	State            string `json:"state" enum:"running,paused,removed" doc:"paused is the owner's hold; removed retires the agent and is final."`
	Generation       int64  `json:"generation,omitempty" readOnly:"true" doc:"Moves when state, harness or resume_generation change."`
	ResumeGeneration int64  `json:"resume_generation" minimum:"0" doc:"Raise it to clear the supervisor's own pause on the agent; it never goes down."`
	Launch           string `json:"launch,omitempty" enum:"supervisor,manual," doc:"remote_control agents only: who runs the host. supervisor means the machine's supervisor does; manual means a person does, with run_remote_control.sh. Left out on a write, a new agent is supervisor and an existing one keeps its value. Always manual for agents imported before the field existed."`
}

// ObservedAgentBody is the supervisor's latest report on an agent.
type ObservedAgentBody struct {
	Generation int64  `json:"generation" doc:"The last generation of the agent the supervisor has acted on."`
	Phase      string `json:"phase,omitempty" enum:"provisioning,running,paused,failed,retired"`
	Reason     string `json:"reason,omitempty"`
	Harness    string `json:"harness,omitempty" doc:"The harness the supervisor reports the agent running; absent until it reports one."`
}

type DesiredAgentView struct {
	DesiredAgentBody
	Observed ObservedAgentBody `json:"observed"`
}

type DesiredStateBody struct {
	Revision int64              `json:"revision" doc:"Send it back with a write; a write on a stale revision is refused."`
	Agents   []DesiredAgentView `json:"agents"`
}

func desiredStateBody(state *DesiredState) DesiredStateBody {
	out := DesiredStateBody{Revision: state.Revision, Agents: make([]DesiredAgentView, len(state.Agents))}
	for i := range state.Agents {
		out.Agents[i] = desiredAgentView(&state.Agents[i])
	}
	return out
}

func desiredAgentView(a *DesiredAgent) DesiredAgentView {
	return DesiredAgentView{
		DesiredAgentBody: DesiredAgentBody{
			ID: a.ID.String(), Kind: string(a.Kind), Name: a.Name, Harness: a.Harness, State: string(a.State),
			Generation: a.Generation, ResumeGeneration: a.ResumeGeneration, Launch: launchOnWire(a),
		},
		Observed: ObservedAgentBody{Generation: a.Observed.Generation, Phase: string(a.Observed.Phase), Reason: a.Observed.Reason, Harness: a.Observed.Harness},
	}
}

// launchOnWire is the launch of an entry that has the choice, and nothing for
// the kinds that do not.
func launchOnWire(a *DesiredAgent) string {
	if a.Kind != ProvisionRemoteControl {
		return ""
	}
	return string(a.Launch)
}

type DesiredStateResponse struct {
	Body DesiredStateBody
}

// SupervisorDesiredStateBody adds whether the board provisions, which an empty document cannot tell from
// "on, with no agents yet". Absent from an older server, which a client reads as off.
type SupervisorDesiredStateBody struct {
	DesiredStateBody
	ProvisionFromBoard bool `json:"provision_from_board,omitempty" doc:"Whether the tenant provisions agents from the board. When true the host must not add, unpause or remove agents itself."`
	WorkspaceReconcile bool `json:"workspace_reconcile,omitempty" doc:"Whether the supervisor reconciles its slots' checkouts against the workspace manifest. Absent reads as off."`
}

type SupervisorDesiredStateResponse struct {
	Body SupervisorDesiredStateBody
}

func (h *Handler) provisioning() (*ProvisionService, error) {
	if h.provision == nil {
		return nil, huma.Error503ServiceUnavailable("provisioning from the board is not available")
	}
	return h.provision, nil
}

// -- Owner --

type SupervisorDesiredStateRequest struct {
	ID string `path:"id"`
}

func (h *Handler) GetDesiredState(ctx context.Context, in *SupervisorDesiredStateRequest) (*DesiredStateResponse, error) {
	who := authn.PersonFromContext(ctx)
	svc, err := h.provisioning()
	if err != nil {
		return nil, err
	}
	id, err := parseAgentPathID(in.ID)
	if err != nil {
		return nil, err
	}
	state, err := svc.OwnerView(ctx, who, id)
	if err != nil {
		return nil, h.mapProvisionError(err)
	}
	return &DesiredStateResponse{Body: desiredStateBody(state)}, nil
}

type PutDesiredStateRequest struct {
	ID   string `path:"id"`
	Body struct {
		Revision int64              `json:"revision" minimum:"0" doc:"The revision this write is based on."`
		Agents   []DesiredAgentBody `json:"agents" maxItems:"64" doc:"Every agent of the document, including removed ones the supervisor has not reported retired."`
	}
}

func (h *Handler) PutDesiredState(ctx context.Context, in *PutDesiredStateRequest) (*DesiredStateResponse, error) {
	who := authn.PersonFromContext(ctx)
	svc, err := h.provisioning()
	if err != nil {
		return nil, err
	}
	id, err := parseAgentPathID(in.ID)
	if err != nil {
		return nil, err
	}
	edits := make([]DesiredAgentEdit, len(in.Body.Agents))
	for i, a := range in.Body.Agents {
		edits[i] = DesiredAgentEdit{
			Kind: ProvisionKind(a.Kind), Name: a.Name, Harness: a.Harness, State: ProvisionState(a.State), Launch: ProvisionLaunch(a.Launch), ResumeGeneration: a.ResumeGeneration,
		}
		if a.ID != "" {
			agentID, err := uuid.Parse(a.ID)
			if err != nil {
				return nil, huma.Error422UnprocessableEntity("agent id must be a valid uuid")
			}
			edits[i].ID = &agentID
		}
	}
	state, err := svc.Edit(ctx, who, id, in.Body.Revision, edits)
	if err != nil {
		return nil, h.mapProvisionError(err)
	}
	return &DesiredStateResponse{Body: desiredStateBody(state)}, nil
}

type UnmanagedSlotBody struct {
	Name    string `json:"name"`
	Kind    string `json:"kind" enum:"slot"`
	Phase   string `json:"phase" doc:"What the slot last reported."`
	Offline bool   `json:"offline" doc:"The slot's report is stale: its supervisor is gone, or it is not running."`
}

type UnmanagedSlotsResponse struct {
	Body struct {
		Slots []UnmanagedSlotBody `json:"slots" doc:"Slots the supervisor reports whose names its document does not hold."`
	}
}

func (h *Handler) GetUnmanagedSlots(ctx context.Context, in *SupervisorDesiredStateRequest) (*UnmanagedSlotsResponse, error) {
	who := authn.PersonFromContext(ctx)
	svc, err := h.provisioning()
	if err != nil {
		return nil, err
	}
	id, err := parseAgentPathID(in.ID)
	if err != nil {
		return nil, err
	}
	slots, err := svc.UnmanagedSlots(ctx, who, id)
	if err != nil {
		return nil, h.mapProvisionError(err)
	}
	out := &UnmanagedSlotsResponse{}
	out.Body.Slots = make([]UnmanagedSlotBody, len(slots))
	for i, s := range slots {
		out.Body.Slots[i] = UnmanagedSlotBody{Name: s.Name, Kind: string(s.Kind), Phase: string(s.Phase), Offline: s.Offline}
	}
	return out, nil
}

type ImportDesiredStateRequest struct {
	ID   string `path:"id"`
	Body struct {
		Revision int64    `json:"revision" minimum:"0" doc:"The revision this write is based on."`
		Names    []string `json:"names" minItems:"1" maxItems:"64" doc:"Reported slots of this supervisor to add to the document."`
	}
}

func (h *Handler) ImportDesiredState(ctx context.Context, in *ImportDesiredStateRequest) (*DesiredStateResponse, error) {
	who := authn.PersonFromContext(ctx)
	svc, err := h.provisioning()
	if err != nil {
		return nil, err
	}
	id, err := parseAgentPathID(in.ID)
	if err != nil {
		return nil, err
	}
	state, err := svc.Import(ctx, who, id, in.Body.Revision, in.Body.Names)
	if err != nil {
		return nil, h.mapProvisionError(err)
	}
	return &DesiredStateResponse{Body: desiredStateBody(state)}, nil
}

// -- Supervisor --

func (h *Handler) SupervisorDesiredState(ctx context.Context, _ *struct{}) (*SupervisorDesiredStateResponse, error) {
	supervisor, err := h.supervisorCaller(ctx)
	if err != nil {
		return nil, err
	}
	svc, err := h.provisioning()
	if err != nil {
		return nil, err
	}
	state, on, err := svc.SupervisorView(ctx, supervisor)
	if err != nil {
		return nil, h.mapProvisionError(err)
	}
	reconcile, err := svc.WorkspaceReconcile(ctx)
	if err != nil {
		return nil, h.mapProvisionError(err)
	}
	return &SupervisorDesiredStateResponse{Body: SupervisorDesiredStateBody{DesiredStateBody: desiredStateBody(state), ProvisionFromBoard: on, WorkspaceReconcile: reconcile}}, nil
}

type ReportObservedRequest struct {
	Body struct {
		Agents []ObservedReportBody `json:"agents" maxItems:"64"`
	}
}

type ObservedReportBody struct {
	ID         string `json:"id"`
	Generation *int64 `json:"generation,omitempty" minimum:"0" doc:"The generation reached, for a document entry; ignored for the steward, which has no desired-state entry."`
	Phase      string `json:"phase" enum:"provisioning,running,paused,failed,retired" doc:"retired is accepted only for a removed agent, at its current generation."`
	Reason     string `json:"reason,omitempty" maxLength:"500" doc:"Why, for failed."`
	Harness    string `json:"harness,omitempty" maxLength:"32" doc:"The harness the agent is running, for a started agent. Not stored for the steward or a retired agent."`
}

func (h *Handler) ReportObserved(ctx context.Context, in *ReportObservedRequest) (*struct{}, error) {
	supervisor, err := h.supervisorCaller(ctx)
	if err != nil {
		return nil, err
	}
	svc, err := h.provisioning()
	if err != nil {
		return nil, err
	}
	reports := make([]ObservedReport, len(in.Body.Agents))
	for i, a := range in.Body.Agents {
		id, err := uuid.Parse(a.ID)
		if err != nil {
			return nil, huma.Error422UnprocessableEntity("agent id must be a valid uuid")
		}
		reports[i] = ObservedReport{ID: id, Phase: Phase(a.Phase), Reason: a.Reason, Harness: a.Harness, NoGeneration: a.Generation == nil}
		if a.Generation != nil {
			reports[i].Generation = *a.Generation
		}
	}
	if err := svc.Report(ctx, supervisor, reports); err != nil {
		return nil, h.mapProvisionError(err)
	}
	return &struct{}{}, nil
}

func registerProvisionRoutes(api huma.API, h *Handler) {
	huma.Register(api, huma.Operation{
		OperationID: "getSupervisorDesiredState",
		Metadata:    authn.RequireUser(),
		Method:      http.MethodGet,
		Path:        "/api/supervisors/{id}/desired_state",
		Summary:     "A supervisor's desired-state document, with what it has reported",
		Description: "The tenant owner only. Returned whether or not provision_from_board is on.",
	}, h.GetDesiredState)
	huma.Register(api, huma.Operation{
		OperationID: "putSupervisorDesiredState",
		Metadata:    authn.RequireUser(),
		Method:      http.MethodPut,
		Path:        "/api/supervisors/{id}/desired_state",
		Summary:     "Replace a supervisor's desired-state document",
		Description: "The tenant owner only, never an agent. Names every agent of the document; the write carries the revision it was " +
			"based on and 409s if it is stale. Removing an agent is state=removed: it retires the agent and ends its credentials, " +
			"and its name is held until the supervisor reports it retired (409).",
	}, h.PutDesiredState)
	huma.Register(api, huma.Operation{
		OperationID: "getSupervisorUnmanagedSlots",
		Metadata:    authn.RequireUser(),
		Method:      http.MethodGet,
		Path:        "/api/supervisors/{id}/unmanaged_slots",
		Summary:     "Slots the supervisor reports that its desired-state document does not name",
		Description: "The tenant owner only.",
	}, h.GetUnmanagedSlots)
	huma.Register(api, huma.Operation{
		OperationID: "importSupervisorSlots",
		Metadata:    authn.RequireUser(),
		Method:      http.MethodPost,
		Path:        "/api/supervisors/{id}/desired_state/import",
		Summary:     "Add reported, unmanaged slots to a supervisor's desired-state document",
		Description: "The tenant owner only. Each entry is created running, or paused if the slot reports paused, so applying it changes " +
			"nothing on the host. 409 on a stale revision or a name already in the document; 422 for a name the supervisor does not report.",
	}, h.ImportDesiredState)
	huma.Register(api, huma.Operation{
		OperationID: "getOwnDesiredState",
		Metadata:    authn.RequireSupervisor(),
		Method:      http.MethodGet,
		Path:        "/api/supervisor/desired_state",
		Summary:     "The calling supervisor's own desired-state document",
		Description: "Empty while the tenant's provision_from_board setting is off.",
	}, h.SupervisorDesiredState)
	huma.Register(api, huma.Operation{
		OperationID:   "reportSupervisorObserved",
		Metadata:      authn.RequireSupervisor(),
		Method:        http.MethodPost,
		Path:          "/api/supervisor/desired_state/observed",
		Summary:       "Report which generation of each agent the supervisor has reached, and why one failed",
		Description:   "Only for the caller's own entries, or the tenant's steward, whose report is keyed by the agent alone and needs no generation (the latest wins; retired is not accepted for it); the batch is stored whole or not at all. A retired report ends a removed agent's hold on its name.",
		DefaultStatus: http.StatusNoContent,
	}, h.ReportObserved)
}
