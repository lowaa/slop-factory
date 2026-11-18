package agent

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/fleetinstructions"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/harnessconfig"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/session"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/workspace"
)

// Handler is a thin HTTP wrapper over Service: every rule about what makes
// an agent valid lives there, so this file only translates between huma's
// request/response structs and Service's domain types, plus maps domain
// errors to HTTP status codes. See ticket.Handler's doc comment for why
// this stays thin.
// Leases is the slice of ticket.Service the handler reads: the lease an
// agent holds.
type Leases interface {
	LeaseFor(ctx context.Context, agentID uuid.UUID) (*ticket.Lease, error)
}

type Handler struct {
	svc        *Service
	conductors *ConductorService
	roles      *RoleService
	tickets    Leases
	sessions   Sessions
	harness    HarnessConfigs
	manifest   ManifestRepos
	fleet      FleetInstructions
	codeHost   CodeHost
	steward    StewardSupervision
	provision  *ProvisionService
	hostLease  *HostLeaseService
	logger     *slog.Logger
}

// ManifestRepos is the tenant's workspace manifest as the scripts need it,
// so they can clone what a workspace lacks.
type ManifestRepos interface {
	// ScriptRepos is empty while the tenant has declared no manifest.
	ScriptRepos(ctx context.Context) ([]workspace.Repo, error)
}

// FleetInstructions is the tenant's fleet instructions as the scripts need
// them, to put in the briefings.
type FleetInstructions interface {
	// Default is nil while the tenant has chosen none.
	Default(ctx context.Context) (*fleetinstructions.Version, error)
	// DefaultOf is the pinned version of a kind, nil while none is pinned.
	DefaultOf(ctx context.Context, kind fleetinstructions.Kind) (*fleetinstructions.Version, error)
	// Get is a version by id, archived or not.
	Get(ctx context.Context, id uuid.UUID) (*fleetinstructions.Version, error)
}

// CodeHost is whether the tenant has a code host credential, which decides
// how the briefings say a change lands. It is true only for a credential whose
// key opens (githubapp.Service.Connected), so a damaged one gets the repo-own-tool briefing.
type CodeHost interface {
	Connected(ctx context.Context) (bool, error)
}

// Sessions is the slice of session.Service the Agents screen reads: each
// agent's current run, and what that run says about itself.
type Sessions interface {
	List(ctx context.Context) ([]session.Session, error)
	Current(ctx context.Context, agentID uuid.UUID) (*session.Session, error)
}

// HarnessConfigs is the slice of harnessconfig.Service the Agents screen
// reads: which config revision each agent should be running, to mark a
// session started on an older one.
type HarnessConfigs interface {
	Load(ctx context.Context) (*harnessconfig.Tenant, error)
	// Membership is the variant an agent runs on, nil for none.
	Membership(ctx context.Context, agentID uuid.UUID) (*harnessconfig.VariantMembership, error)
}

// HandlerDeps are what the Agents screen reads besides the agents
// themselves: their roles, the tickets they hold and what is running as
// each. The screen is the one the human lives on -- a listing that didn't
// say who holds what, and what is running as each agent, would send them
// to a second screen for the question they came to ask.
type HandlerDeps struct {
	Agents *Service
	// Conductors serves the conductor routes. Required wherever the routes
	// can be called; the spec generator mounts them without one.
	Conductors *ConductorService
	Roles      *RoleService
	Tickets    Leases
	Sessions   Sessions
	// HarnessConfig is optional: without it every agent reads as having
	// no config, which is what a tenant that never set one has.
	HarnessConfig HarnessConfigs
	// Manifest is optional: without it the scripts clone nothing.
	Manifest ManifestRepos
	// Fleet is optional: without it the briefings carry no fleet
	// instructions.
	Fleet FleetInstructions
	// CodeHost is optional: without it the briefings send agents to the
	// repo's own way of landing a change.
	CodeHost CodeHost
	// Steward serves the steward runner's routes. Required wherever they
	// can be called; the spec generator mounts them without one.
	Steward StewardSupervision
	// Provision serves the desired-state routes. Required wherever they can
	// be called; the spec generator mounts them without one.
	Provision *ProvisionService
	// HostLeases serves the Remote Control host lease routes. Required
	// wherever they can be called; the spec generator mounts them without one.
	HostLeases *HostLeaseService
}

func NewHandler(deps *HandlerDeps, logHandler slog.Handler) *Handler {
	return &Handler{
		svc:        deps.Agents,
		conductors: deps.Conductors,
		roles:      deps.Roles,
		tickets:    deps.Tickets,
		sessions:   deps.Sessions,
		harness:    deps.HarnessConfig,
		manifest:   deps.Manifest,
		fleet:      deps.Fleet,
		codeHost:   deps.CodeHost,
		steward:    deps.Steward,
		provision:  deps.Provision,
		hostLease:  deps.HostLeases,
		logger:     slog.New(logHandler),
	}
}

// -- Wire types --

// LeaseBody is the ticket an agent currently holds, if any.
type LeaseBody struct {
	TicketID   int64     `json:"ticket_id"`
	AcquiredAt time.Time `json:"acquired_at"`
	ExpiresAt  time.Time `json:"expires_at"`
}

func leaseToBody(l ticket.Lease) LeaseBody {
	return LeaseBody{TicketID: l.TicketID, AcquiredAt: l.AcquiredAt, ExpiresAt: l.ExpiresAt}
}

// AgentRefBody names the agent on the other end of a replacement.
type AgentRefBody struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func refToBody(r *AgentRef) *AgentRefBody {
	if r == nil {
		return nil
	}
	return &AgentRefBody{ID: r.ID.String(), Name: r.Name}
}

// AgentBody is the wire representation of an Agent. Lease is nil when the
// agent holds nothing -- never an error, since most agents most of the
// time hold nothing.
//
// There is deliberately no token field here: Agent's tokenHash is
// unexported so it can never reach this struct by accident, and the
// plaintext is only ever handed back once, on CreateAgent's response.
type AgentBody struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	CreatedAt  time.Time  `json:"created_at"`
	DisabledAt *time.Time `json:"disabled_at"`
	// ArchivedAt is set while a disabled agent is put away: the agent lists
	// leave it out unless asked.
	ArchivedAt *time.Time `json:"archived_at"`
	// Replaces and ReplacedBy link an agent to the one it took over from, or
	// the one that took over from it, when it was re-authorized.
	Replaces   *AgentRefBody `json:"replaces"`
	ReplacedBy *AgentRefBody `json:"replaced_by"`
	// Kind is what the agent is for; fixed when it is registered.
	Kind Kind `json:"kind" enum:"worker,console,steward"`
	// SupervisorAgentID is the supervisor that runs this agent as a slot;
	// absent for one no supervisor runs (signed in by hand, a conductor).
	SupervisorAgentID *string      `json:"supervisor_agent_id,omitempty"`
	Lease             *LeaseBody   `json:"lease"`
	Session           *SessionBody `json:"session"`
	// Roles are what a human has assigned it, by name.
	Roles []RoleBody `json:"roles"`
	// ConfigRevision is the harness config revision it should be running:
	// the later of the defaults' and its override's, 0 for none.
	ConfigRevision int64 `json:"config_revision"`
	// Variant is the variant version it runs on; null for none. While it is
	// set, the agent's config is the version's alone.
	Variant *AgentVariantBody `json:"variant"`
	// HarnessOverrideInactive is true while the agent has a saved harness
	// config override that is being ignored because it is in a variant. The
	// override applies again once the agent leaves.
	HarnessOverrideInactive bool `json:"harness_override_inactive"`
	// Observed is what a supervisor last reported about the steward, e.g. why
	// it was not started. Absent for any other agent, or before a report.
	Observed *ObservedStewardBody `json:"observed,omitempty"`
}

// ObservedStewardBody is a supervisor's report on the steward.
type ObservedStewardBody struct {
	Phase      string    `json:"phase" enum:"provisioning,running,paused,failed"`
	Reason     string    `json:"reason,omitempty"`
	ReportedAt time.Time `json:"reported_at"`
}

// AgentVariantBody names the variant version an agent runs on.
type AgentVariantBody struct {
	VersionID string `json:"version_id"`
	Name      string `json:"name"`
	Version   int    `json:"version"`
	Harness   string `json:"harness"`
}

// SessionBody is the agent's current run: when it started and was last
// heard from, and what it says it is. The description is self-asserted by
// the agent's machine -- shown to a human, never trusted for anything.
// Nil when the agent has never run.
type SessionBody struct {
	StartedAt      time.Time         `json:"started_at"`
	LastSeenAt     time.Time         `json:"last_seen_at"`
	Harness        string            `json:"harness"`
	HarnessVersion string            `json:"harness_version"`
	Provider       string            `json:"provider"`
	Model          string            `json:"model"`
	Labels         map[string]string `json:"labels"`
	// ConfigRevision is the harness config revision the run says it was
	// started with; null when it did not say.
	ConfigRevision *int64 `json:"config_revision"`
	// ConfigStatus compares that with the agent's ConfigRevision: stale
	// means the run needs a restart to pick up the current config, unknown
	// that it did not say which it has, and none that there is no config
	// and it said nothing either.
	ConfigStatus string `json:"config_status" enum:"current,stale,unknown,none"`
	// BundleHash is the managed asset bundle the run's workspace says it
	// installed; empty when it did not say.
	BundleHash string `json:"bundle_hash" doc:"the hash of the managed asset bundle (hooks, scripts) the run's workspace says it installed; empty when it did not say"`
	// BundleStatus compares BundleHash with the bundle this server serves:
	// stale means the workspace has not run an update since the bundle changed.
	BundleStatus string `json:"bundle_status" enum:"current,stale,unknown" doc:"stale: the workspace's bundle is not the one the server serves now; unknown: it did not say"`
	// Stuck is set while the loop breaker's last word on this run stands:
	// it was blocked repeatedly with no progress and let stop. Null once
	// the agent next makes progress.
	Stuck *StuckBody `json:"stuck"`
}

// StuckBody is why a run was let stop, and since when.
type StuckBody struct {
	Reason string    `json:"reason"`
	At     time.Time `json:"at"`
}

// Config statuses, see SessionBody.ConfigStatus.
const (
	ConfigCurrent = "current"
	ConfigStale   = "stale"
	ConfigUnknown = "unknown"
	ConfigNone    = "none"
)

// configStatus is the rule behind SessionBody.ConfigStatus. A run reporting
// no revision, where there is config, is unknown rather than stale: it may
// be a harness started some other way than run_remote_control.sh, and claiming it is
// behind would be a guess. A session on a higher revision than its agent's is
// as stale as one on a lower: removing an override can leave the agent's
// effective revision below the one its session started on.
func configStatus(sessionRevision *int64, agentRevision int64) string {
	switch {
	case sessionRevision == nil && agentRevision == 0:
		return ConfigNone
	case sessionRevision == nil:
		return ConfigUnknown
	case *sessionRevision != agentRevision:
		return ConfigStale
	default:
		return ConfigCurrent
	}
}

func sessionToBody(s *session.Session, configRevision int64) *SessionBody {
	if s == nil {
		return nil
	}
	labels := s.Description.Labels
	if labels == nil {
		labels = map[string]string{}
	}
	var stuck *StuckBody
	if s.Stuck != nil {
		stuck = &StuckBody{Reason: s.Stuck.Reason, At: s.Stuck.At}
	}
	return &SessionBody{
		Stuck:          stuck,
		StartedAt:      s.StartedAt,
		LastSeenAt:     s.LastSeenAt,
		Harness:        s.Description.Harness,
		HarnessVersion: s.Description.HarnessVersion,
		Provider:       s.Description.Provider,
		Model:          s.Description.Model,
		Labels:         labels,
		ConfigRevision: s.Description.ConfigRevision,
		ConfigStatus:   configStatus(s.Description.ConfigRevision, configRevision),
		BundleHash:     s.Description.BundleHash,
		BundleStatus:   workspace.BundleStatusOf(s.Description.BundleHash),
	}
}

// harnessConfig is the tenant's saved harness config, or none when the
// handler was built without one.
func (h *Handler) harnessConfig(ctx context.Context) (*harnessconfig.Tenant, error) {
	if h.harness == nil {
		return &harnessconfig.Tenant{}, nil
	}
	return h.harness.Load(ctx)
}

// currentSession is the agent's live run, or nil if it has none.
func (h *Handler) currentSession(ctx context.Context, agentID uuid.UUID) (*session.Session, error) {
	current, err := h.sessions.Current(ctx, agentID)
	if errors.Is(err, session.ErrNotFound) {
		return nil, nil
	}
	return current, err
}

// agentToBody loads the agent's active lease to populate Lease, and takes
// its current session and roles from the caller -- a listing loads every
// session and every membership in one query each rather than one per
// agent. ticket.ErrNoLease means the agent holds nothing, which is not a
// failure -- everything else is, and is returned for the caller to map.
func (h *Handler) agentToBody(ctx context.Context, a Agent, current *session.Session, roles []Role, config *harnessconfig.Tenant) (AgentBody, error) {
	effective := config.EffectiveFor(a.ID)
	revision := effective.Revision
	body := AgentBody{
		ID:             a.ID.String(),
		Name:           a.Name,
		CreatedAt:      a.CreatedAt,
		DisabledAt:     a.DisabledAt,
		ArchivedAt:     a.ArchivedAt,
		Replaces:       refToBody(a.Replaces),
		ReplacedBy:     refToBody(a.ReplacedBy),
		Kind:           a.Kind,
		Session:        sessionToBody(current, revision),
		Roles:          rolesToBody(roles),
		ConfigRevision: revision,

		HarnessOverrideInactive: effective.OverrideInactive,
	}
	if a.SupervisorID != nil {
		id := a.SupervisorID.String()
		body.SupervisorAgentID = &id
	}
	if m := effective.Variant; m != nil {
		body.Variant = &AgentVariantBody{VersionID: m.VersionID.String(), Name: m.Name, Version: m.Version, Harness: m.Harness}
	}

	lease, err := h.tickets.LeaseFor(ctx, a.ID)
	if err != nil {
		if errors.Is(err, ticket.ErrNoLease) {
			return body, nil
		}
		return AgentBody{}, err
	}
	leaseBody := leaseToBody(*lease)
	body.Lease = &leaseBody
	return body, nil
}

// parseAgentPathID parses an agent id path parameter, returning a 422
// rather than letting a malformed uuid reach the store as a 500.
func parseAgentPathID(raw string) (uuid.UUID, error) {
	id, err := uuid.Parse(raw)
	if err != nil {
		return uuid.UUID{}, huma.Error422UnprocessableEntity("agent id must be a valid uuid")
	}
	return id, nil
}

// -- List --

type ListAgentsRequest struct {
	IncludeArchived bool `query:"include_archived" doc:"Also list archived agents. Anything naming an agent from history needs them."`
}

type ListAgentsResponse struct {
	Body struct {
		Agents []AgentBody `json:"agents"`
	}
}

func (h *Handler) ListAgents(ctx context.Context, input *ListAgentsRequest) (*ListAgentsResponse, error) {
	agents, err := h.svc.List(ctx, input.IncludeArchived)
	if err != nil {
		return nil, h.mapError(err)
	}
	sessions, err := h.sessions.List(ctx)
	if err != nil {
		return nil, h.mapError(err)
	}
	current := make(map[uuid.UUID]*session.Session, len(sessions))
	for i := range sessions {
		current[sessions[i].AgentID] = &sessions[i]
	}
	roles, err := h.roles.RolesByAgent(ctx)
	if err != nil {
		return nil, h.mapError(err)
	}
	config, err := h.harnessConfig(ctx)
	if err != nil {
		return nil, h.mapError(err)
	}

	var stewardReports map[uuid.UUID]ObservedSteward
	if h.provision != nil {
		if stewardReports, err = h.provision.StewardReports(ctx); err != nil {
			return nil, h.mapError(err)
		}
	}

	resp := &ListAgentsResponse{}
	resp.Body.Agents = make([]AgentBody, len(agents))
	for i, a := range agents {
		body, err := h.agentToBody(ctx, a, current[a.ID], roles[a.ID], config)
		if err != nil {
			return nil, h.mapError(err)
		}
		if o, ok := stewardReports[a.ID]; ok {
			body.Observed = &ObservedStewardBody{Phase: string(o.Phase), Reason: o.Reason, ReportedAt: o.ReportedAt}
		}
		resp.Body.Agents[i] = body
	}
	return resp, nil
}

// -- Create --

type CreateAgentBody struct {
	Name string `json:"name"`
}

type CreateAgentRequest struct {
	Body CreateAgentBody
}

type CreateAgentResponse struct {
	Body AgentBody
}

// CreateAgent registers an agent with no credential: a client is
// authorized to act as it through OAuth (see internal/oauth).
func (h *Handler) CreateAgent(ctx context.Context, input *CreateAgentRequest) (*CreateAgentResponse, error) {
	created, err := h.svc.Create(ctx, input.Body.Name)
	if err != nil {
		return nil, h.mapError(err)
	}

	// Newly registered: nothing has run as it yet, and it has no roles --
	// but the tenant's defaults already apply to it.
	config, err := h.harnessConfig(ctx)
	if err != nil {
		return nil, h.mapError(err)
	}
	body, err := h.agentToBody(ctx, *created, nil, nil, config)
	if err != nil {
		return nil, h.mapError(err)
	}
	return &CreateAgentResponse{Body: body}, nil
}

// -- Get --

type GetAgentRequest struct {
	ID string `path:"id"`
}

type AgentResponse struct {
	Body AgentBody
}

func (h *Handler) GetAgent(ctx context.Context, input *GetAgentRequest) (*AgentResponse, error) {
	id, err := parseAgentPathID(input.ID)
	if err != nil {
		return nil, err
	}

	a, err := h.svc.GetWorker(ctx, id)
	if err != nil {
		return nil, h.mapError(err)
	}
	return h.agentResponse(ctx, a)
}

// agentResponse is one agent's body, with its current session and roles
// looked up.
func (h *Handler) agentResponse(ctx context.Context, a *Agent) (*AgentResponse, error) {
	current, err := h.currentSession(ctx, a.ID)
	if err != nil {
		return nil, h.mapError(err)
	}
	roles, err := h.roles.RolesFor(ctx, a.ID)
	if err != nil {
		return nil, h.mapError(err)
	}
	config, err := h.harnessConfig(ctx)
	if err != nil {
		return nil, h.mapError(err)
	}
	body, err := h.agentToBody(ctx, *a, current, roles, config)
	if err != nil {
		return nil, h.mapError(err)
	}
	return &AgentResponse{Body: body}, nil
}

// -- Set disabled --

type SetAgentDisabledBody struct {
	Disabled bool `json:"disabled"`
}

type SetAgentDisabledRequest struct {
	ID   string `path:"id"`
	Body SetAgentDisabledBody
}

func (h *Handler) SetAgentDisabled(ctx context.Context, input *SetAgentDisabledRequest) (*AgentResponse, error) {
	id, err := parseAgentPathID(input.ID)
	if err != nil {
		return nil, err
	}

	a, err := h.svc.SetDisabled(ctx, id, input.Body.Disabled)
	if err != nil {
		return nil, h.mapError(err)
	}
	return h.agentResponse(ctx, a)
}

// -- Archive --

type SetAgentArchivedRequest struct {
	ID string `path:"id"`
}

func (h *Handler) ArchiveAgent(ctx context.Context, input *SetAgentArchivedRequest) (*AgentResponse, error) {
	return h.setArchived(ctx, input, true)
}

func (h *Handler) UnarchiveAgent(ctx context.Context, input *SetAgentArchivedRequest) (*AgentResponse, error) {
	return h.setArchived(ctx, input, false)
}

func (h *Handler) setArchived(ctx context.Context, input *SetAgentArchivedRequest, archived bool) (*AgentResponse, error) {
	who := authn.PersonFromContext(ctx)
	id, err := parseAgentPathID(input.ID)
	if err != nil {
		return nil, err
	}
	a, err := h.svc.SetArchived(ctx, who, id, archived)
	if err != nil {
		return nil, h.mapError(err)
	}
	return h.agentResponse(ctx, a)
}
