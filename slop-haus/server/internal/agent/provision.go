package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
)

// The desired-state document: which agents a tenant's owner wants a
// supervisor to run. The supervisor converges on it and reports how far it
// got; this package decides nothing about hosts.

type ProvisionKind string

const (
	ProvisionSlot          ProvisionKind = "slot"
	ProvisionSteward       ProvisionKind = "steward"
	ProvisionRemoteControl ProvisionKind = "remote_control"
)

// ProvisionLaunch says who runs a Remote Control host: the machine's supervisor,
// or a person with .slop-haus/run_remote_control.sh. Only remote_control
// entries have a choice; every other kind reads as manual.
type ProvisionLaunch string

const (
	LaunchSupervisor ProvisionLaunch = "supervisor"
	LaunchManual     ProvisionLaunch = "manual"
)

type ProvisionState string

const (
	// StateRunning and StatePaused are the owner's wish; a supervisor's own
	// cause-pause is observed state and never written here.
	StateRunning ProvisionState = "running"
	StatePaused  ProvisionState = "paused"
	// StateRemoved retires the agent. The entry stays in the document until
	// the supervisor reports it retired, which is what frees its name.
	StateRemoved ProvisionState = "removed"
)

// Phase is what the supervisor reports about an agent.
type Phase string

const (
	PhaseProvisioning Phase = "provisioning"
	PhaseRunning      Phase = "running"
	PhasePaused       Phase = "paused"
	PhaseFailed       Phase = "failed"
	PhaseRetired      Phase = "retired"
)

const (
	MaxDesiredAgents  = 64
	MaxFailureReason  = 500
	defaultHarnessKey = "claude"
)

// Names become directories on the supervisor's host. The pattern is the one
// the supervisor's Layout enforces and the table's check repeats.
var (
	desiredNamePattern    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,99}$`)
	desiredHarnessPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)
)

// DesiredAgent is one entry of the document, with the supervisor's report on it.
type DesiredAgent struct {
	ID               uuid.UUID
	Kind             ProvisionKind
	Name             string
	Harness          string
	State            ProvisionState
	Launch           ProvisionLaunch
	Generation       int64
	ResumeGeneration int64
	Observed         ObservedAgent
}

// ObservedAgent is the supervisor's latest report. Generation is the last
// generation of the entry it has acted on.
type ObservedAgent struct {
	Generation int64
	Phase      Phase
	Reason     string
	// Harness is the one the supervisor says the agent runs; empty until reported.
	Harness string
}

// ObservedSteward is the supervisor's latest report on the steward, which has
// no desired-state entry and so no generation.
type ObservedSteward struct {
	Phase      Phase
	Reason     string
	ReportedAt time.Time
}

// DesiredState is a supervisor's whole document.
type DesiredState struct {
	Revision int64
	Agents   []DesiredAgent
}

// DesiredAgentEdit is one entry of a write. A nil ID adds an agent.
type DesiredAgentEdit struct {
	ID      *uuid.UUID
	Kind    ProvisionKind
	Name    string
	Harness string
	State   ProvisionState
	// Launch is empty when the writer does not say: a new Remote Control
	// agent then defaults to supervisor and an existing entry keeps what it
	// has, so a client that predates the field cannot flip it by omission.
	Launch           ProvisionLaunch
	ResumeGeneration int64
}

// ObservedReport is the supervisor's word on one agent.
type ObservedReport struct {
	ID         uuid.UUID
	Generation int64
	Phase      Phase
	Reason     string
	Harness    string
	// NoGeneration is a report that sent none: only the steward's may.
	NoGeneration bool
}

var (
	// ErrStaleRevision is a write based on a revision that is no longer current.
	ErrStaleRevision = errors.New("agent: the desired state changed since it was read")
	// ErrDesiredInvalid is an edit the document's rules refuse.
	ErrDesiredInvalid = errors.New("agent: invalid desired state")
	// ErrDesiredNameHeld is a name still held by an agent that has not been
	// reported retired.
	ErrDesiredNameHeld = errors.New("agent: that name is held until the supervisor reports the old agent retired")
)

func desiredInvalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrDesiredInvalid, fmt.Sprintf(format, args...))
}

func (k ProvisionKind) valid() bool {
	return k == ProvisionSlot || k == ProvisionSteward || k == ProvisionRemoteControl
}

func (l ProvisionLaunch) valid() bool { return l == LaunchSupervisor || l == LaunchManual }

func (s ProvisionState) valid() bool {
	return s == StateRunning || s == StatePaused || s == StateRemoved
}

func (p Phase) valid() bool {
	switch p {
	case PhaseProvisioning, PhaseRunning, PhasePaused, PhaseFailed, PhaseRetired:
		return true
	}
	return false
}

// applyDesiredEdit turns a write into the rows to store: new entries, and
// existing ones whose generation moved. Entries it leaves out are unchanged.
// Every live entry must be named, so a stale or partial client cannot drop
// agents by omission; removal is a state.
func applyDesiredEdit(current []DesiredAgent, edits []DesiredAgentEdit) ([]DesiredAgent, error) {
	if len(edits) > MaxDesiredAgents {
		return nil, desiredInvalid("at most %d agents", MaxDesiredAgents)
	}
	byID := make(map[uuid.UUID]*DesiredAgent, len(current))
	for i := range current {
		byID[current[i].ID] = &current[i]
	}
	heldNames := make(map[string]bool, len(current))
	for i := range current {
		heldNames[current[i].Name] = true
	}
	seenID := map[uuid.UUID]bool{}
	seenName := map[string]bool{}
	var changed []DesiredAgent
	for i := range edits {
		e := &edits[i]
		e.Name = strings.TrimSpace(e.Name)
		if e.Harness == "" {
			e.Harness = defaultHarnessKey
		}
		switch {
		case !e.Kind.valid():
			return nil, desiredInvalid("kind must be slot, steward or remote_control")
		case !e.State.valid():
			return nil, desiredInvalid("state must be running, paused or removed")
		case !desiredNamePattern.MatchString(e.Name) || e.Name == "." || e.Name == "..":
			return nil, desiredInvalid("name %q must be letters, digits, '.', '_' or '-', and start with a letter or digit", e.Name)
		case !desiredHarnessPattern.MatchString(e.Harness):
			return nil, desiredInvalid("harness %q is not a harness name", e.Harness)
		case e.Launch != "" && !e.Launch.valid():
			return nil, desiredInvalid("launch must be supervisor or manual")
		case e.Launch != "" && e.Kind != ProvisionRemoteControl:
			return nil, desiredInvalid("launch applies to remote_control agents only")
		case e.ResumeGeneration < 0:
			return nil, desiredInvalid("resume_generation must not be negative")
		case e.ID == nil && heldNames[e.Name]:
			return nil, fmt.Errorf("%w: %q", ErrDesiredNameHeld, e.Name)
		case seenName[e.Name]:
			return nil, desiredInvalid("name %q appears twice", e.Name)
		}
		seenName[e.Name] = true

		if e.ID == nil {
			if e.State == StateRemoved {
				return nil, desiredInvalid("a new agent cannot start removed")
			}
			if e.ResumeGeneration != 0 {
				return nil, desiredInvalid("a new agent starts with resume_generation 0")
			}
			changed = append(changed, DesiredAgent{ID: uuid.New(), Kind: e.Kind, Name: e.Name, Harness: e.Harness, State: e.State, Launch: newLaunch(e), Generation: 1})
			continue
		}
		if seenID[*e.ID] {
			return nil, desiredInvalid("agent %s appears twice", *e.ID)
		}
		seenID[*e.ID] = true
		have, ok := byID[*e.ID]
		switch {
		case !ok:
			return nil, desiredInvalid("agent %s is not in the document", *e.ID)
		case e.Kind != have.Kind || e.Name != have.Name:
			return nil, desiredInvalid("an agent's kind and name cannot change: remove it and add another")
		case have.State == StateRemoved && e.State != StateRemoved:
			return nil, desiredInvalid("a removed agent stays removed")
		case e.ResumeGeneration < have.ResumeGeneration:
			return nil, desiredInvalid("resume_generation only goes up")
		}
		launch := have.Launch
		if e.Launch != "" {
			launch = e.Launch
		}
		if e.State == have.State && e.Harness == have.Harness && e.ResumeGeneration == have.ResumeGeneration && launch == have.Launch {
			continue
		}
		next := *have
		next.State, next.Harness, next.ResumeGeneration, next.Launch = e.State, e.Harness, e.ResumeGeneration, launch
		next.Generation++
		changed = append(changed, next)
	}
	for _, have := range current {
		if !seenID[have.ID] {
			return nil, desiredInvalid("agent %s (%s) is missing: set its state to removed to remove it", have.ID, have.Name)
		}
	}
	return changed, nil
}

// newLaunch is a new entry's launch. Board-added Remote Control agents are run
// by the supervisor unless the owner says otherwise; the other kinds have no
// choice and read as manual.
func newLaunch(e *DesiredAgentEdit) ProvisionLaunch {
	switch {
	case e.Kind != ProvisionRemoteControl:
		return LaunchManual
	case e.Launch == "":
		return LaunchSupervisor
	}
	return e.Launch
}

// ProvisionSettings says whether the tenant provisions from the board, and
// whether its supervisors reconcile workspaces. Satisfied by *settings.Service.
type ProvisionSettings interface {
	ProvisionFromBoardEnabled(ctx context.Context) (bool, error)
	WorkspaceReconcileEnabled(ctx context.Context) (bool, error)
}

// ProvisionService keeps the desired-state documents.
type ProvisionService struct {
	store    ProvisionStore
	agents   *Service
	roles    TenantRoles
	gate     *authn.OwnerGate
	settings ProvisionSettings
	reports  SlotReports
	logger   *slog.Logger
}

func NewProvisionService(store ProvisionStore, agents *Service, roles TenantRoles, settings ProvisionSettings, logHandler slog.Handler, opts ...ProvisionOption) *ProvisionService {
	p := &ProvisionService{store: store, agents: agents, roles: roles, gate: authn.NewOwnerGate(roles, authn.WithRefusal(ErrNotTenantOwner)), settings: settings, logger: slog.New(logHandler)}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// supervisorOf is the live supervisor a document belongs to; anything else
// reads as not found.
func (p *ProvisionService) supervisorOf(ctx context.Context, id uuid.UUID) (*Agent, error) {
	found, err := p.agents.store.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if !found.IsSupervisor() || found.Archived() {
		return nil, ErrNotFound
	}
	return found, nil
}

// OwnerView is a supervisor's document for the tenant's owner, whether or not
// the supervisor is let read it yet.
func (p *ProvisionService) OwnerView(ctx context.Context, who *authn.User, supervisorID uuid.UUID) (*DesiredState, error) {
	if err := p.gate.RequireOwner(ctx, who); err != nil {
		return nil, err
	}
	if _, err := p.supervisorOf(ctx, supervisorID); err != nil {
		return nil, err
	}
	return p.store.Load(ctx, supervisorID)
}

// SetSlotCap changes how many live slots a supervisor may have. Lowering it
// below the live count keeps the slots running and refuses new ones until
// enough retire; raising it lets the supervisor's next reconcile create the
// slots it was refused. The tenant's owner only: the cap is the tenant's
// resource limit.
func (p *ProvisionService) SetSlotCap(ctx context.Context, who *authn.User, supervisorID uuid.UUID, slotCap int) (*Agent, error) {
	if err := p.gate.RequireOwner(ctx, who); err != nil {
		return nil, err
	}
	if slotCap < 1 || slotCap > MaxSlotCap {
		return nil, ErrSlotCapInvalid
	}
	updated, previous, err := p.agents.store.SetSlotCap(ctx, supervisorID, slotCap)
	if err != nil {
		return nil, err
	}
	p.logger.InfoContext(ctx, "audit", "action", "supervisor_slot_cap_changed", "user_id", who.ID, "tenant", who.TenantID,
		"supervisor", supervisorID, "old_slot_cap", previous, "new_slot_cap", slotCap)
	return updated, nil
}

// Edit replaces the document, if baseRevision is still current. Removed
// agents are retired here -- disabled, so their grants end, and archived --
// after the write commits. Retiring is idempotent, repeated by every later
// write and again when the supervisor reports the agent retired.
func (p *ProvisionService) Edit(ctx context.Context, who *authn.User, supervisorID uuid.UUID, baseRevision int64, edits []DesiredAgentEdit) (*DesiredState, error) {
	if err := p.gate.RequireOwner(ctx, who); err != nil {
		return nil, err
	}
	supervisor, err := p.supervisorOf(ctx, supervisorID)
	if err != nil {
		return nil, err
	}
	return p.write(ctx, who, supervisor, baseRevision, edits)
}

// write is Edit once who may make it.
func (p *ProvisionService) write(ctx context.Context, who *authn.User, supervisor *Agent, baseRevision int64, edits []DesiredAgentEdit) (*DesiredState, error) {
	next, err := p.store.Update(ctx, supervisor.ID, baseRevision, func(current []DesiredAgent) ([]DesiredAgent, error) {
		return applyDesiredEdit(current, edits)
	})
	if err != nil {
		return nil, err
	}
	// Every removed entry still held, not only the ones this write removed:
	// a retire that failed after an earlier write committed is retried by the
	// next write, whatever it changes.
	var removed []uuid.UUID
	for i := range next.Agents {
		if next.Agents[i].State == StateRemoved {
			removed = append(removed, next.Agents[i].ID)
		}
	}
	p.logger.InfoContext(ctx, "audit", "action", "supervisor_desired_state_edited", "tenant", who.TenantID,
		"user_id", who.ID, "supervisor", supervisor.ID, "revision", next.Revision, "agents", len(next.Agents), "removed", len(removed))
	if err := p.store.RetireRemoved(ctx, supervisor.ID, removed, p.retireFunc(supervisor)); err != nil {
		return nil, err
	}
	return next, nil
}

func (p *ProvisionService) retireFunc(supervisor *Agent) RetireFunc {
	return func(ctx context.Context, entry *DesiredAgent) error { return p.retire(ctx, supervisor, entry) }
}

// retire ends the real agent behind a removed entry, if the supervisor has
// made one. Disabling revokes its grants, as RetireSlot does for a slot. Only
// an agent of the entry's own kind is touched, so a name cannot be pointed at
// an unrelated agent.
func (p *ProvisionService) retire(ctx context.Context, supervisor *Agent, entry *DesiredAgent) error {
	found, err := p.agents.store.GetByName(ctx, entry.Name)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	switch entry.Kind {
	case ProvisionSlot:
		if found.SupervisorID == nil || *found.SupervisorID != supervisor.ID {
			return nil
		}
		_, err = p.agents.RetireSlot(ctx, supervisor, found.ID)
		return err
	case ProvisionSteward:
		if !found.IsSteward() {
			return nil
		}
	case ProvisionRemoteControl:
		// Only one this supervisor made, whoever approves it now.
		if !runsHost(supervisor, found) {
			return nil
		}
	}
	if _, err := p.agents.setDisabled(ctx, found.ID, true); err != nil {
		return err
	}
	if _, err := p.agents.store.SetArchived(ctx, found.ID, true); err != nil {
		return err
	}
	p.logger.InfoContext(ctx, "audit", "action", "supervisor_desired_agent_retired", "tenant", found.Tenant,
		"supervisor", supervisor.ID, "agent", found.ID, "kind", entry.Kind)
	return nil
}

// SupervisorView is what a supervisor reconciles against: its own document,
// or an empty one while the tenant does not provision from the board. The bool
// is whether it does.
func (p *ProvisionService) SupervisorView(ctx context.Context, supervisor *Agent) (*DesiredState, bool, error) {
	on, err := p.settings.ProvisionFromBoardEnabled(ctx)
	if err != nil {
		return nil, false, err
	}
	if !on {
		return &DesiredState{Agents: []DesiredAgent{}}, false, nil
	}
	state, err := p.store.Load(ctx, supervisor.ID)
	return state, err == nil, err
}

// WorkspaceReconcile is whether supervisors reconcile their slots' checkouts
// against the manifest. Independent of provisioning from the board.
func (p *ProvisionService) WorkspaceReconcile(ctx context.Context) (bool, error) {
	return p.settings.WorkspaceReconcileEnabled(ctx)
}

// Report records what a supervisor observed. Reports about agents that are
// not its own are refused, so it cannot retire another supervisor's entry and
// free that name. A retired report ends the entry's hold on its name, and
// only for an entry the owner removed, at its current generation.
func (p *ProvisionService) Report(ctx context.Context, supervisor *Agent, reports []ObservedReport) error {
	for _, r := range reports {
		if !r.Phase.valid() {
			return desiredInvalid("phase must be provisioning, running, paused, failed or retired")
		}
		if r.Generation < 0 {
			return desiredInvalid("generation must not be negative")
		}
		if len([]rune(r.Reason)) > MaxFailureReason {
			return desiredInvalid("reason is longer than %d characters", MaxFailureReason)
		}
	}
	return p.store.Observe(ctx, supervisor.ID, reports, p.retireFunc(supervisor))
}

// StewardReports are the supervisor reports on the tenant's steward, by agent id.
func (p *ProvisionService) StewardReports(ctx context.Context) (map[uuid.UUID]ObservedSteward, error) {
	return p.store.StewardReports(ctx)
}
