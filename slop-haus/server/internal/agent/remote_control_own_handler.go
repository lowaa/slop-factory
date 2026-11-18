package agent

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
)

type OwnRemoteControlSupervisorBody struct {
	ID             string             `json:"id"`
	Name           string             `json:"name"`
	RemoteControls []DesiredAgentView `json:"remote_controls" doc:"its Remote Control entries that it runs, with what it last reported of each"`
}

type OwnRemoteControlSupervisorsResponse struct {
	Body struct {
		ProvisionFromBoard bool                             `json:"provision_from_board" doc:"Whether the team provisions agents from the board; with it off no supervisor runs a Remote Control and the list is empty."`
		Supervisors        []OwnRemoteControlSupervisorBody `json:"supervisors"`
	}
}

func (h *Handler) ListOwnRemoteControlSupervisors(ctx context.Context, _ *struct{}) (*OwnRemoteControlSupervisorsResponse, error) {
	svc, err := h.provisioning()
	if err != nil {
		return nil, err
	}
	on, supervisors, err := svc.OwnRemoteControlSupervisors(ctx, authn.PersonFromContext(ctx))
	if err != nil {
		return nil, h.mapProvisionError(err)
	}
	resp := &OwnRemoteControlSupervisorsResponse{}
	resp.Body.ProvisionFromBoard = on
	resp.Body.Supervisors = make([]OwnRemoteControlSupervisorBody, len(supervisors))
	for i := range supervisors {
		s := &supervisors[i]
		views := make([]DesiredAgentView, len(s.Entries))
		for j := range s.Entries {
			views[j] = desiredAgentView(&s.Entries[j])
		}
		resp.Body.Supervisors[i] = OwnRemoteControlSupervisorBody{ID: s.Supervisor.ID.String(), Name: s.Supervisor.Name, RemoteControls: views}
	}
	return resp, nil
}

type AddOwnRemoteControlRequest struct {
	ID   string `path:"id" doc:"the supervisor"`
	Body struct {
		Name    string `json:"name" doc:"The remote control's name, unique across the team"`
		Harness string `json:"harness,omitempty" doc:"The harness it runs; the board's default when absent"`
	}
}

type OwnRemoteControlResponse struct {
	Body DesiredAgentView
}

func (h *Handler) AddOwnRemoteControl(ctx context.Context, in *AddOwnRemoteControlRequest) (*OwnRemoteControlResponse, error) {
	svc, err := h.provisioning()
	if err != nil {
		return nil, err
	}
	id, err := parseAgentPathID(in.ID)
	if err != nil {
		return nil, err
	}
	added, err := svc.AddOwnRemoteControl(ctx, authn.PersonFromContext(ctx), id, in.Body.Name, in.Body.Harness)
	if err != nil {
		return nil, h.mapProvisionError(err)
	}
	return &OwnRemoteControlResponse{Body: desiredAgentView(added)}, nil
}

type RemoveOwnRemoteControlRequest struct {
	ID      string `path:"id" doc:"the supervisor"`
	EntryID string `path:"entry_id" doc:"the remote control's entry"`
}

func (h *Handler) RemoveOwnRemoteControl(ctx context.Context, in *RemoveOwnRemoteControlRequest) (*struct{}, error) {
	svc, err := h.provisioning()
	if err != nil {
		return nil, err
	}
	id, err := parseAgentPathID(in.ID)
	if err != nil {
		return nil, err
	}
	entry, err := uuid.Parse(in.EntryID)
	if err != nil {
		return nil, huma.Error422UnprocessableEntity("entry_id must be a valid uuid")
	}
	if err := svc.RemoveOwnRemoteControl(ctx, authn.PersonFromContext(ctx), id, entry); err != nil {
		return nil, h.mapProvisionError(err)
	}
	return &struct{}{}, nil
}

func registerOwnRemoteControlRoutes(api huma.API, h *Handler) {
	huma.Register(api, huma.Operation{
		OperationID: "listOwnRemoteControlSupervisors",
		Metadata:    authn.RequireUser(),
		Method:      http.MethodGet,
		Path:        "/api/remote_control/supervisors",
		Summary:     "List the supervisors you approved, with the Remote Controls each runs",
		Description: "The machines a person can add a supervisor-run Remote Control to from the Remote Control page: live supervisors whose sign-in they approved. Empty while the team does not provision from the board.",
	}, h.ListOwnRemoteControlSupervisors)
	huma.Register(api, huma.Operation{
		OperationID:   "addOwnRemoteControl",
		Metadata:      authn.RequireUser(),
		Method:        http.MethodPost,
		Path:          "/api/remote_control/supervisors/{id}/remote_controls",
		DefaultStatus: http.StatusCreated,
		Summary:       "Add a Remote Control for a supervisor you approved to run",
		Description: "Adds a launch=supervisor remote_control entry to its desired state; the supervisor then makes it as your conductor and starts its host. " +
			"Only the person who approved the supervisor (403 otherwise), with a recent sign-in, while the team provisions from the board (409 otherwise). " +
			"422 for a name any agent holds.",
	}, h.AddOwnRemoteControl)
	huma.Register(api, huma.Operation{
		OperationID:   "removeOwnRemoteControl",
		Metadata:      authn.RequireUser(),
		Method:        http.MethodDelete,
		Path:          "/api/remote_control/supervisors/{id}/remote_controls/{entry_id}",
		DefaultStatus: http.StatusNoContent,
		Summary:       "Remove a Remote Control a supervisor you approved runs",
		Description:   "Marks the entry removed, which retires its conductor; the supervisor stops its host once it is idle. 404 for any other entry.",
	}, h.RemoveOwnRemoteControl)
}
