package agent

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/steward"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/workspace"
)

// supervisorCaller hands the request's identity to the service, which
// decides whether it is a live supervisor.
func (h *Handler) supervisorCaller(ctx context.Context) (*Agent, error) {
	who, _ := authn.FromContext(ctx)
	supervisor, err := h.svc.SupervisorCaller(ctx, who)
	if err != nil {
		return nil, h.mapSupervisorError(err)
	}
	return supervisor, nil
}

// SupervisorSlotBody is one of a supervisor's workers.
type SupervisorSlotBody struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	CreatedAt  time.Time  `json:"created_at"`
	DisabledAt *time.Time `json:"disabled_at"`
	ArchivedAt *time.Time `json:"archived_at"`
}

func supervisorSlotBody(a *Agent) SupervisorSlotBody {
	return SupervisorSlotBody{ID: a.ID.String(), Name: a.Name, CreatedAt: a.CreatedAt, DisabledAt: a.DisabledAt, ArchivedAt: a.ArchivedAt}
}

// -- Create --

type CreateSlotRequest struct {
	Body struct {
		Name string `json:"name" minLength:"1" maxLength:"100" doc:"The new worker's name, unique among the tenant's agents"`
	}
}

type SlotResponse struct {
	Body SupervisorSlotBody
}

func (h *Handler) CreateSlot(ctx context.Context, in *CreateSlotRequest) (*SlotResponse, error) {
	supervisor, err := h.supervisorCaller(ctx)
	if err != nil {
		return nil, err
	}
	created, err := h.svc.CreateSlot(ctx, supervisor, in.Body.Name)
	if err != nil {
		return nil, h.mapSupervisorError(err)
	}
	return &SlotResponse{Body: supervisorSlotBody(created)}, nil
}

// -- Adopt --

type AdoptSlotRequest struct {
	Body struct {
		Name string `json:"name" minLength:"1" maxLength:"100" doc:"The name of an existing worker with no supervisor"`
	}
}

func (h *Handler) AdoptSlot(ctx context.Context, in *AdoptSlotRequest) (*SlotResponse, error) {
	supervisor, err := h.supervisorCaller(ctx)
	if err != nil {
		return nil, err
	}
	adopted, err := h.svc.AdoptSlot(ctx, supervisor, in.Body.Name)
	if errors.Is(err, ErrArchived) {
		// The target's state, not the caller's: supervisorCaller already vetted the caller.
		return nil, huma.Error409Conflict("that agent is archived and cannot be adopted")
	}
	if err != nil {
		return nil, h.mapSupervisorError(err)
	}
	return &SlotResponse{Body: supervisorSlotBody(adopted)}, nil
}

// -- Retire --

type SlotIDRequest struct {
	ID string `path:"id"`
}

func (h *Handler) RetireSlot(ctx context.Context, in *SlotIDRequest) (*SlotResponse, error) {
	supervisor, err := h.supervisorCaller(ctx)
	if err != nil {
		return nil, err
	}
	id, err := parseAgentPathID(in.ID)
	if err != nil {
		return nil, err
	}
	retired, err := h.svc.RetireSlot(ctx, supervisor, id)
	if err != nil {
		return nil, h.mapSupervisorError(err)
	}
	return &SlotResponse{Body: supervisorSlotBody(retired)}, nil
}

// -- List --

type ListSlotsRequest struct {
	IncludeArchived bool `query:"include_archived" doc:"Also list retired slots, which carry archived_at. Their names stay taken."`
}

type ListSlotsResponse struct {
	Body struct {
		Slots   []SupervisorSlotBody `json:"slots" doc:"the supervisor's live slots; retired ones only with include_archived"`
		SlotCap int                  `json:"slot_cap"`
	}
}

func (h *Handler) ListSlots(ctx context.Context, in *ListSlotsRequest) (*ListSlotsResponse, error) {
	supervisor, err := h.supervisorCaller(ctx)
	if err != nil {
		return nil, err
	}
	slots, err := h.svc.Slots(ctx, supervisor, in.IncludeArchived)
	if err != nil {
		return nil, h.mapSupervisorError(err)
	}
	resp := &ListSlotsResponse{}
	resp.Body.SlotCap = *supervisor.SlotCap
	resp.Body.Slots = make([]SupervisorSlotBody, len(slots))
	for i := range slots {
		resp.Body.Slots[i] = supervisorSlotBody(&slots[i])
	}
	return resp, nil
}

// -- Credential --

// SlotCredentialResponse is the token pair the device sign-in returns, so a
// client that signs in by device and one that is handed a slot's credential
// parse the same thing.
type SlotCredentialResponse struct {
	CacheControl string `header:"Cache-Control"`
	Body         TokenResponse
}

func (h *Handler) IssueSlotCredential(ctx context.Context, in *SlotIDRequest) (*SlotCredentialResponse, error) {
	supervisor, err := h.supervisorCaller(ctx)
	if err != nil {
		return nil, err
	}
	id, err := parseAgentPathID(in.ID)
	if err != nil {
		return nil, err
	}
	issued, err := h.svc.IssueSlotCredential(ctx, supervisor.ID, id)
	if err != nil {
		return nil, h.mapSupervisorError(err)
	}
	return &SlotCredentialResponse{CacheControl: "no-store", Body: *issued.Tokens}, nil
}

// -- Steward --

type StewardResponse struct {
	Body SupervisorSlotBody
}

func (h *Handler) EnsureSteward(ctx context.Context, _ *struct{}) (*StewardResponse, error) {
	supervisor, err := h.supervisorCaller(ctx)
	if err != nil {
		return nil, err
	}
	steward, err := h.svc.Steward(ctx, supervisor.ID)
	if err != nil {
		return nil, h.mapSupervisorError(err)
	}
	return &StewardResponse{Body: supervisorSlotBody(steward)}, nil
}

func (h *Handler) IssueStewardCredential(ctx context.Context, _ *struct{}) (*SlotCredentialResponse, error) {
	supervisor, err := h.supervisorCaller(ctx)
	if err != nil {
		return nil, err
	}
	issued, err := h.svc.IssueStewardCredential(ctx, supervisor.ID)
	if err != nil {
		return nil, h.mapSupervisorError(err)
	}
	return &SlotCredentialResponse{CacheControl: "no-store", Body: *issued.Tokens}, nil
}

// -- Remote Control hosts --

type RemoteControlHostRequest struct {
	Name string `path:"name" doc:"The name of a remote_control entry of the supervisor's desired state that it runs"`
}

func (h *Handler) EnsureRemoteControlHost(ctx context.Context, in *RemoteControlHostRequest) (*StewardResponse, error) {
	supervisor, err := h.supervisorCaller(ctx)
	if err != nil {
		return nil, err
	}
	p, err := h.provisioning()
	if err != nil {
		return nil, err
	}
	host, err := p.RemoteControlHost(ctx, supervisor.ID, in.Name)
	if err != nil {
		return nil, h.mapSupervisorError(err)
	}
	return &StewardResponse{Body: supervisorSlotBody(host)}, nil
}

func (h *Handler) IssueRemoteControlCredential(ctx context.Context, in *RemoteControlHostRequest) (*SlotCredentialResponse, error) {
	supervisor, err := h.supervisorCaller(ctx)
	if err != nil {
		return nil, err
	}
	p, err := h.provisioning()
	if err != nil {
		return nil, err
	}
	issued, err := p.IssueRemoteControlCredential(ctx, supervisor.ID, in.Name)
	if err != nil {
		return nil, h.mapSupervisorError(err)
	}
	return &SlotCredentialResponse{CacheControl: "no-store", Body: *issued.Tokens}, nil
}

type RevokeRemoteControlCredentialRequest struct {
	Name string `path:"name"`
	Body struct {
		RefreshToken string `json:"refresh_token" minLength:"1" doc:"a refresh token of the host's credential"`
	}
}

func (h *Handler) RevokeRemoteControlCredential(ctx context.Context, in *RevokeRemoteControlCredentialRequest) (*struct{}, error) {
	supervisor, err := h.supervisorCaller(ctx)
	if err != nil {
		return nil, err
	}
	p, err := h.provisioning()
	if err != nil {
		return nil, err
	}
	if err := p.RevokeRemoteControlCredential(ctx, supervisor.ID, in.Name, in.Body.RefreshToken); err != nil {
		return nil, h.mapSupervisorError(err)
	}
	return nil, nil
}

// -- Steward runner --

// StewardSupervision is what the steward runner reads and takes from the
// server. Satisfied by *steward.Supervision.
type StewardSupervision interface {
	Head(ctx context.Context) (int64, error)
	Events(ctx context.Context, after int64, limit int) ([]steward.BoardEvent, int64, error)
	AcquireLease(ctx context.Context, holder uuid.UUID) (bool, time.Time, error)
	ReleaseLease(ctx context.Context, holder uuid.UUID) error
	AgreementText(ctx context.Context, since, until time.Time) (string, error)
	Mode(ctx context.Context) (steward.Mode, error)
}

// agreementWindow is how far back the agreement report reads.
const agreementWindow = 90 * 24 * time.Hour

func (h *Handler) stewardSupervision() (StewardSupervision, error) {
	if h.steward == nil {
		return nil, huma.Error503ServiceUnavailable("the steward is not available")
	}
	return h.steward, nil
}

type StewardCredentialRefRequest struct {
	Body struct {
		RefreshToken string `json:"refresh_token" minLength:"1" doc:"a refresh token of the steward credential"`
	}
}

func (h *Handler) RevokeStewardCredential(ctx context.Context, in *StewardCredentialRefRequest) (*struct{}, error) {
	supervisor, err := h.supervisorCaller(ctx)
	if err != nil {
		return nil, err
	}
	if err := h.svc.RevokeStewardCredential(ctx, supervisor.ID, in.Body.RefreshToken); err != nil {
		return nil, h.mapSupervisorError(err)
	}
	return &struct{}{}, nil
}

type StewardBriefingResponse struct {
	Body struct {
		Text string `json:"text" doc:"the steward run's system prompt"`
	}
}

func (h *Handler) StewardBriefing(ctx context.Context, _ *struct{}) (*StewardBriefingResponse, error) {
	supervisor, err := h.supervisorCaller(ctx)
	if err != nil {
		return nil, err
	}
	stew, err := h.svc.Steward(ctx, supervisor.ID)
	if err != nil {
		return nil, h.mapSupervisorError(err)
	}
	fleet, err := h.fleetInstructions(ctx, &stew.ID)
	if err != nil {
		return nil, err
	}
	instructions, err := h.stewardInstructions(ctx)
	if err != nil {
		return nil, err
	}
	resp := &StewardBriefingResponse{}
	resp.Body.Text = workspace.StewardBriefingText(stew.Name, fleet, instructions)
	return resp, nil
}

type StewardEventsRequest struct {
	After int64 `query:"after" default:"-1" minimum:"-1" doc:"The cursor a previous call returned. -1 (the default): start from now, returning no events."`
	Limit int   `query:"limit" minimum:"0" maximum:"200" doc:"Most events to return; 0 is the maximum."`
}

type StewardEventBody struct {
	Kind     string `json:"kind" enum:"discussion,agent_filed" doc:"discussion: a ticket entered discussion. agent_filed: an agent filed a ticket in preparing."`
	TicketID int64  `json:"ticket_id"`
}

type StewardEventsResponse struct {
	Body struct {
		Events []StewardEventBody `json:"events"`
		Cursor int64              `json:"cursor" doc:"Pass as after on the next call; events up to it are not returned again."`
	}
}

func (h *Handler) StewardEvents(ctx context.Context, in *StewardEventsRequest) (*StewardEventsResponse, error) {
	if _, err := h.supervisorCaller(ctx); err != nil {
		return nil, err
	}
	sup, err := h.stewardSupervision()
	if err != nil {
		return nil, err
	}
	resp := &StewardEventsResponse{}
	resp.Body.Events = []StewardEventBody{}
	if in.After < 0 {
		if resp.Body.Cursor, err = sup.Head(ctx); err != nil {
			return nil, h.mapError(err)
		}
		return resp, nil
	}
	events, cursor, err := sup.Events(ctx, in.After, in.Limit)
	if err != nil {
		return nil, h.mapError(err)
	}
	for _, e := range events {
		resp.Body.Events = append(resp.Body.Events, StewardEventBody{Kind: string(e.Kind), TicketID: e.TicketID})
	}
	resp.Body.Cursor = cursor
	return resp, nil
}

type StewardLeaseResponse struct {
	Body struct {
		Acquired  bool       `json:"acquired" doc:"false while another supervisor's run holds the slot"`
		ExpiresAt *time.Time `json:"expires_at" doc:"when the hold lapses unless renewed; null when not acquired"`
	}
}

func (h *Handler) AcquireStewardLease(ctx context.Context, _ *struct{}) (*StewardLeaseResponse, error) {
	supervisor, err := h.supervisorCaller(ctx)
	if err != nil {
		return nil, err
	}
	sup, err := h.stewardSupervision()
	if err != nil {
		return nil, err
	}
	acquired, expires, err := sup.AcquireLease(ctx, supervisor.ID)
	if err != nil {
		return nil, h.mapError(err)
	}
	resp := &StewardLeaseResponse{}
	resp.Body.Acquired = acquired
	if acquired {
		resp.Body.ExpiresAt = &expires
	}
	return resp, nil
}

func (h *Handler) ReleaseStewardLease(ctx context.Context, _ *struct{}) (*struct{}, error) {
	supervisor, err := h.supervisorCaller(ctx)
	if err != nil {
		return nil, err
	}
	sup, err := h.stewardSupervision()
	if err != nil {
		return nil, err
	}
	if err := sup.ReleaseLease(ctx, supervisor.ID); err != nil {
		return nil, h.mapError(err)
	}
	return &struct{}{}, nil
}

type StewardModeResponse struct {
	Body struct {
		Mode string `json:"mode" enum:"off,shadow,live" doc:"the tenant's steward_mode; a supervisor runs no steward while it is off"`
	}
}

func (h *Handler) StewardMode(ctx context.Context, _ *struct{}) (*StewardModeResponse, error) {
	if _, err := h.supervisorCaller(ctx); err != nil {
		return nil, err
	}
	sup, err := h.stewardSupervision()
	if err != nil {
		return nil, err
	}
	mode, err := sup.Mode(ctx)
	if err != nil {
		return nil, h.mapError(err)
	}
	resp := &StewardModeResponse{}
	resp.Body.Mode = string(mode)
	return resp, nil
}

type StewardAgreementResponse struct {
	Body struct {
		Text string `json:"text" doc:"per category, how often the owner decided as the steward would have, then the same for discussion labels and answers; empty before any decision"`
	}
}

func (h *Handler) StewardAgreement(ctx context.Context, _ *struct{}) (*StewardAgreementResponse, error) {
	if _, err := h.supervisorCaller(ctx); err != nil {
		return nil, err
	}
	sup, err := h.stewardSupervision()
	if err != nil {
		return nil, err
	}
	text, err := sup.AgreementText(ctx, time.Now().Add(-agreementWindow), time.Time{})
	if err != nil {
		if errors.Is(err, steward.ErrWindowTooLarge) {
			return nil, huma.Error409Conflict(strings.TrimPrefix(err.Error(), "steward: "))
		}
		return nil, h.mapError(err)
	}
	resp := &StewardAgreementResponse{}
	resp.Body.Text = text
	return resp, nil
}

// -- Supervisors, for the people who manage them --

type SupervisorBody struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	CreatedAt  time.Time  `json:"created_at"`
	DisabledAt *time.Time `json:"disabled_at"`
	ArchivedAt *time.Time `json:"archived_at"`
	SlotCap    int        `json:"slot_cap"`
	SlotCount  int        `json:"slot_count" doc:"live slots"`
	LastSeenAt time.Time  `json:"last_seen_at" doc:"its last poll or slot report, or its creation when it has neither"`
	Stale      bool       `json:"stale" doc:"unheard of for over 24 hours: the board offers to end it"`
}

type ListSupervisorsRequest struct {
	IncludeArchived bool `query:"include_archived" doc:"Also list archived supervisors."`
}

type ListSupervisorsResponse struct {
	Body struct {
		Supervisors []SupervisorBody `json:"supervisors"`
	}
}

func (h *Handler) supervisorBody(ctx context.Context, a *Agent) (SupervisorBody, error) {
	slots, err := h.svc.Slots(ctx, a, false)
	if err != nil {
		return SupervisorBody{}, err
	}
	seen, stale, err := h.svc.SupervisorLastSeen(ctx, a.ID, time.Now())
	if err != nil {
		return SupervisorBody{}, err
	}
	return SupervisorBody{ID: a.ID.String(), Name: a.Name, CreatedAt: a.CreatedAt, DisabledAt: a.DisabledAt,
		ArchivedAt: a.ArchivedAt, SlotCap: *a.SlotCap, SlotCount: len(slots), LastSeenAt: seen,
		// An ended supervisor is not offered to be ended again.
		Stale: stale && a.DisabledAt == nil && a.ArchivedAt == nil}, nil
}

func (h *Handler) ListSupervisors(ctx context.Context, in *ListSupervisorsRequest) (*ListSupervisorsResponse, error) {
	found, err := h.svc.ListSupervisors(ctx, in.IncludeArchived)
	if err != nil {
		return nil, h.mapError(err)
	}
	resp := &ListSupervisorsResponse{}
	resp.Body.Supervisors = make([]SupervisorBody, len(found))
	for i := range found {
		if resp.Body.Supervisors[i], err = h.supervisorBody(ctx, &found[i]); err != nil {
			return nil, h.mapError(err)
		}
	}
	return resp, nil
}

type SetSupervisorDisabledResponse struct {
	Body SupervisorBody
}

// SetSupervisorDisabled switches a supervisor off, which takes its slots with
// it, or back on, which does not bring them back.
func (h *Handler) SetSupervisorDisabled(ctx context.Context, in *SetAgentDisabledRequest) (*SetSupervisorDisabledResponse, error) {
	id, err := parseAgentPathID(in.ID)
	if err != nil {
		return nil, err
	}
	updated, err := h.svc.SetSupervisorDisabled(ctx, id, in.Body.Disabled)
	if err != nil {
		return nil, h.mapError(err)
	}
	body, err := h.supervisorBody(ctx, updated)
	if err != nil {
		return nil, h.mapError(err)
	}
	return &SetSupervisorDisabledResponse{Body: body}, nil
}

type SetSupervisorSlotCapRequest struct {
	ID   string `path:"id"`
	Body struct {
		SlotCap int `json:"slot_cap" doc:"How many live slots the supervisor may have, 1 to 16"`
	}
}

// SetSupervisorSlotCap lets the tenant's owner change a supervisor's cap.
func (h *Handler) SetSupervisorSlotCap(ctx context.Context, in *SetSupervisorSlotCapRequest) (*SetSupervisorDisabledResponse, error) {
	who := authn.PersonFromContext(ctx)
	svc, err := h.provisioning()
	if err != nil {
		return nil, err
	}
	id, err := parseAgentPathID(in.ID)
	if err != nil {
		return nil, err
	}
	updated, err := svc.SetSlotCap(ctx, who, id, in.Body.SlotCap)
	if err != nil {
		return nil, h.mapProvisionError(err)
	}
	body, err := h.supervisorBody(ctx, updated)
	if err != nil {
		return nil, h.mapError(err)
	}
	return &SetSupervisorDisabledResponse{Body: body}, nil
}

func registerSupervisorRoutes(api huma.API, h *Handler) {
	huma.Register(api, huma.Operation{
		OperationID:   "createSupervisorSlot",
		Metadata:      authn.RequireSupervisor(),
		Method:        http.MethodPost,
		Path:          "/api/supervisor/slots",
		Summary:       "Create a worker for the calling supervisor to run tickets in",
		Description:   "Only a supervisor's own credential. The new agent is always a worker. 409 when the name is taken (a retired slot of the caller's that never did anything is revived instead) or the supervisor is at its slot cap.",
		DefaultStatus: http.StatusCreated,
	}, h.CreateSlot)
	huma.Register(api, huma.Operation{
		OperationID: "adoptSupervisorSlot",
		Metadata:    authn.RequireSupervisor(),
		Method:      http.MethodPost,
		Path:        "/api/supervisor/slots/adopt",
		Summary:     "Adopt an existing worker that has no supervisor as one of the calling supervisor's slots",
		Description: "For an agent that began as an interactive worker and was reused as a slot. Only a worker that is not archived and has no supervisor; " +
			"adopting one the caller already has succeeds. 404 when there is no such agent or another supervisor has it, 409 for another kind, " +
			"an archived agent or the slot cap.",
	}, h.AdoptSlot)
	huma.Register(api, huma.Operation{
		OperationID: "retireSupervisorSlot",
		Metadata:    authn.RequireSupervisor(),
		Method:      http.MethodDelete,
		Path:        "/api/supervisor/slots/{id}",
		Summary:     "Retire one of the calling supervisor's workers",
		Description: "Disables it, releases its tickets, ends its credentials and archives it, which frees its place under the cap. " +
			"Retiring a retired slot succeeds. 404 for any agent that is not the caller's slot.",
	}, h.RetireSlot)
	huma.Register(api, huma.Operation{
		OperationID: "listSupervisorSlots",
		Metadata:    authn.RequireSupervisor(),
		Method:      http.MethodGet,
		Path:        "/api/supervisor/slots",
		Summary:     "List the calling supervisor's live workers, with its cap",
	}, h.ListSlots)
	huma.Register(api, huma.Operation{
		OperationID: "issueSupervisorSlotCredential",
		Metadata:    authn.RequireSupervisor(),
		Method:      http.MethodPost,
		Path:        "/api/supervisor/slots/{id}/credential",
		Summary:     "Issue a fresh credential for one of the calling supervisor's live workers",
		Description: "Returns the token pair the device sign-in returns, for the workspace client, approved by the person who " +
			"approved the supervisor. Only for the caller's own live slots: 404 for any other agent, 409 for a disabled slot. " +
			"Rate limited (429).",
	}, h.IssueSlotCredential)
	huma.Register(api, huma.Operation{
		OperationID: "ensureSupervisorSteward",
		Metadata:    authn.RequireSupervisor(),
		Method:      http.MethodPut,
		Path:        "/api/supervisor/steward",
		Summary:     "Get the tenant's steward, making it if there is none",
		Description: "Only a supervisor's own credential. One steward per tenant: a second call returns the same agent. " +
			"409 when an archived steward holds the name (unarchive it instead).",
	}, h.EnsureSteward)
	huma.Register(api, huma.Operation{
		OperationID: "issueSupervisorStewardCredential",
		Metadata:    authn.RequireSupervisor(),
		Method:      http.MethodPost,
		Path:        "/api/supervisor/steward/credential",
		Summary:     "Issue a fresh credential for the tenant's steward, making the steward if there is none",
		Description: "Returns the token pair the device sign-in returns, approved by the person who approved the supervisor. " +
			"409 for a disabled steward. Rate limited (429).",
	}, h.IssueStewardCredential)
	huma.Register(api, huma.Operation{
		OperationID: "revokeSupervisorStewardCredential",
		Metadata:    authn.RequireSupervisor(),
		Method:      http.MethodPost,
		Path:        "/api/supervisor/steward/credential/revoke",
		Summary:     "End the steward grant a refresh token belongs to",
		Description: "For a supervisor replacing the credential it holds. 403 when the token is not a live grant of the tenant's steward.",
	}, h.RevokeStewardCredential)
	huma.Register(api, huma.Operation{
		OperationID: "ensureSupervisorRemoteControlHost",
		Metadata:    authn.RequireSupervisor(),
		Method:      http.MethodPut,
		Path:        "/api/supervisor/remote-control/{name}",
		Summary:     "Get the conductor behind a remote_control entry the supervisor runs, making it if there is none",
		Description: "Only a supervisor's own credential, and only for a launch=supervisor remote_control entry of its desired state (404 otherwise). " +
			"The conductor is owned by the person who approved the supervisor; a name held by any other agent is 404.",
	}, h.EnsureRemoteControlHost)
	huma.Register(api, huma.Operation{
		OperationID: "issueSupervisorRemoteControlCredential",
		Metadata:    authn.RequireSupervisor(),
		Method:      http.MethodPost,
		Path:        "/api/supervisor/remote-control/{name}/credential",
		Summary:     "Issue a fresh credential for a Remote Control host the supervisor runs, making the agent if there is none",
		Description: "Returns the token pair the device sign-in returns, approved by the person who approved the supervisor. " +
			"409 for a disabled host. Rate limited (429).",
	}, h.IssueRemoteControlCredential)
	huma.Register(api, huma.Operation{
		OperationID: "revokeSupervisorRemoteControlCredential",
		Metadata:    authn.RequireSupervisor(),
		Method:      http.MethodPost,
		Path:        "/api/supervisor/remote-control/{name}/credential/revoke",
		Summary:     "End the grant a refresh token belongs to",
		Description: "403 when the token is not a live grant of that host.",
	}, h.RevokeRemoteControlCredential)
	huma.Register(api, huma.Operation{
		OperationID: "getSupervisorStewardBriefing",
		Metadata:    authn.RequireSupervisor(),
		Method:      http.MethodGet,
		Path:        "/api/supervisor/steward/briefing",
		Summary:     "The steward run's system prompt",
	}, h.StewardBriefing)
	huma.Register(api, huma.Operation{
		OperationID: "listSupervisorStewardEvents",
		Metadata:    authn.RequireSupervisor(),
		Method:      http.MethodGet,
		Path:        "/api/supervisor/steward/events",
		Summary:     "Board events that should wake the steward, after a cursor",
		Description: "A ticket entering discussion, or an agent filing a ticket in preparing. Without after (or with -1), returns no events and the cursor " +
			"for now. The returned cursor also moves past changes that wake nobody.",
	}, h.StewardEvents)
	huma.Register(api, huma.Operation{
		OperationID: "acquireSupervisorStewardLease",
		Metadata:    authn.RequireSupervisor(),
		Method:      http.MethodPost,
		Path:        "/api/supervisor/steward/lease",
		Summary:     "Take the tenant's single steward run slot",
		Description: "Renews the hold when the caller already has it. The hold expires on its own, so a supervisor that died frees the slot.",
	}, h.AcquireStewardLease)
	huma.Register(api, huma.Operation{
		OperationID: "releaseSupervisorStewardLease",
		Metadata:    authn.RequireSupervisor(),
		Method:      http.MethodDelete,
		Path:        "/api/supervisor/steward/lease",
		Summary:     "Give the steward run slot back",
		Description: "Releasing a slot the caller does not hold changes nothing.",
	}, h.ReleaseStewardLease)
	huma.Register(api, huma.Operation{
		OperationID: "getSupervisorStewardMode",
		Metadata:    authn.RequireSupervisor(),
		Method:      http.MethodGet,
		Path:        "/api/supervisor/steward/mode",
		Summary:     "The tenant's steward_mode, so a supervisor runs no steward while it is off",
	}, h.StewardMode)
	huma.Register(api, huma.Operation{
		OperationID: "getSupervisorStewardAgreement",
		Metadata:    authn.RequireSupervisor(),
		Method:      http.MethodGet,
		Path:        "/api/supervisor/steward/agreement",
		Summary:     "The agreement report a steward run starts from",
	}, h.StewardAgreement)
	huma.Register(api, huma.Operation{
		OperationID: "listSupervisors",
		Metadata:    authn.RequireUser(),
		Method:      http.MethodGet,
		Path:        "/api/supervisors",
		Summary:     "List the tenant's supervisors",
	}, h.ListSupervisors)
	huma.Register(api, huma.Operation{
		OperationID: "setSupervisorDisabled",
		Metadata:    authn.RequireUser(),
		Method:      http.MethodPatch,
		Path:        "/api/supervisors/{id}",
		Summary:     "Enable or disable a supervisor",
		Description: "Disabling takes its slots with it and ends its credentials; enabling it again does not bring the slots back.",
	}, h.SetSupervisorDisabled)
	huma.Register(api, huma.Operation{
		OperationID: "setSupervisorSlotCap",
		Metadata:    authn.RequireUser(),
		Method:      http.MethodPatch,
		Path:        "/api/supervisors/{id}/slot-cap",
		Summary:     "Change how many live workers a supervisor may have",
		Description: "The tenant owner only. 422 outside 1..16. Lowering the cap below the live slots retires none: they keep running " +
			"and the supervisor cannot create more until enough retire. Raising it lets the supervisor's next reconcile create the slots it was refused.",
	}, h.SetSupervisorSlotCap)
}
