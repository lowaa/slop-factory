package agent

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/supervisorwatch"
)

// Only slots can be unmanaged. A slot is the one agent kind whose supervisor
// reports on it (supervisor_slot_states) and that is linked to a supervisor
// without the document: `slop-haus slot add` makes one directly. The steward
// is minted by a supervisor but has no report, and a Remote Control host is a
// console agent with no supervisor at all, so neither can be told apart from
// an agent the owner never meant to hand over. Both enter the document only
// by being added from the board.

// SlotReports is the supervisors' slot heartbeats, for the context's tenant.
// Satisfied by *supervisorwatch.Service.
type SlotReports interface {
	Reported(ctx context.Context) ([]*supervisorwatch.Slot, error)
}

// WithSlotReports lets the service list and import slots a supervisor reports
// but its document does not name.
func WithSlotReports(reports SlotReports) ProvisionOption {
	return func(p *ProvisionService) { p.reports = reports }
}

// ProvisionOption configures a ProvisionService.
type ProvisionOption func(*ProvisionService)

// ErrImportUnavailable is an import asked of a service built without slot reports.
var ErrImportUnavailable = errors.New("agent: importing slots is not available")

// UnmanagedSlot is a slot a supervisor reports that its document does not name.
type UnmanagedSlot struct {
	Name    string
	Kind    ProvisionKind
	Phase   supervisorwatch.Phase
	Offline bool
}

// maxImportAttempts bounds how often ImportAll retries a supervisor whose
// document changed under it.
const maxImportAttempts = 3

func (p *ProvisionService) reportedSlotsOf(ctx context.Context, supervisorID uuid.UUID) (map[string]*supervisorwatch.Slot, error) {
	if p.reports == nil {
		return nil, ErrImportUnavailable
	}
	all, err := p.reports.Reported(ctx)
	if err != nil {
		return nil, err
	}
	mine := map[string]*supervisorwatch.Slot{}
	for _, s := range all {
		if s.SupervisorID != nil && *s.SupervisorID == supervisorID {
			mine[s.AgentName] = s
		}
	}
	return mine, nil
}

// unmanaged is the reported slots whose names the document does not hold, by name order.
func unmanaged(reported map[string]*supervisorwatch.Slot, current []DesiredAgent) []UnmanagedSlot {
	held := make(map[string]bool, len(current))
	for i := range current {
		held[current[i].Name] = true
	}
	var out []UnmanagedSlot
	for name, s := range reported {
		if !held[name] {
			out = append(out, UnmanagedSlot{Name: name, Kind: ProvisionSlot, Phase: s.Phase, Offline: s.Offline})
		}
	}
	slices.SortFunc(out, func(a, b UnmanagedSlot) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// UnmanagedSlots is what the owner could import into the supervisor's document.
func (p *ProvisionService) UnmanagedSlots(ctx context.Context, who *authn.User, supervisorID uuid.UUID) ([]UnmanagedSlot, error) {
	if err := p.gate.RequireOwner(ctx, who); err != nil {
		return nil, err
	}
	if _, err := p.supervisorOf(ctx, supervisorID); err != nil {
		return nil, err
	}
	reported, err := p.reportedSlotsOf(ctx, supervisorID)
	if err != nil {
		return nil, err
	}
	state, err := p.store.Load(ctx, supervisorID)
	if err != nil {
		return nil, err
	}
	return unmanaged(reported, state.Agents), nil
}

// importEntry is the entry that adopts a reported slot as it is: paused if
// the slot reports paused, else running, so applying the document changes
// nothing on the host. The harness is claude: slots report none, and run
// nothing else.
func importEntry(s *supervisorwatch.Slot) DesiredAgent {
	state := StateRunning
	if s.Phase == supervisorwatch.PhasePaused {
		state = StatePaused
	}
	return DesiredAgent{ID: uuid.New(), Kind: ProvisionSlot, Name: s.AgentName, Harness: defaultHarnessKey, State: state, Launch: LaunchManual, Generation: 1}
}

// importEdit adds an entry for each name, refusing one that is not a reported
// slot of the supervisor or is already in the document.
func importEdit(reported map[string]*supervisorwatch.Slot, names []string) func(current []DesiredAgent) ([]DesiredAgent, error) {
	return func(current []DesiredAgent) ([]DesiredAgent, error) {
		if len(current)+len(names) > MaxDesiredAgents {
			return nil, desiredInvalid("at most %d agents", MaxDesiredAgents)
		}
		held := make(map[string]bool, len(current))
		for i := range current {
			held[current[i].Name] = true
		}
		seen := map[string]bool{}
		added := make([]DesiredAgent, 0, len(names))
		for _, name := range names {
			s, ok := reported[name]
			switch {
			case seen[name]:
				return nil, desiredInvalid("name %q appears twice", name)
			case held[name]:
				return nil, fmt.Errorf("%w: %q", ErrDesiredNameHeld, name)
			case !ok:
				return nil, desiredInvalid("%q is not a slot this supervisor reports", name)
			}
			seen[name] = true
			added = append(added, importEntry(s))
		}
		return added, nil
	}
}

// Import adds a running or paused entry to the supervisor's document for each
// named reported slot, if baseRevision is still current.
func (p *ProvisionService) Import(ctx context.Context, who *authn.User, supervisorID uuid.UUID, baseRevision int64, names []string) (*DesiredState, error) {
	if err := p.gate.RequireOwner(ctx, who); err != nil {
		return nil, err
	}
	supervisor, err := p.supervisorOf(ctx, supervisorID)
	if err != nil {
		return nil, err
	}
	next, err := p.importNames(ctx, supervisor, baseRevision, names)
	if err != nil {
		return nil, err
	}
	p.logger.InfoContext(ctx, "audit", "action", "supervisor_desired_state_imported", "tenant", who.TenantID,
		"user_id", who.ID, "supervisor", supervisor.ID, "revision", next.Revision, "agents", len(next.Agents), "imported", len(names))
	return next, nil
}

func (p *ProvisionService) importNames(ctx context.Context, supervisor *Agent, baseRevision int64, names []string) (*DesiredState, error) {
	reported, err := p.reportedSlotsOf(ctx, supervisor.ID)
	if err != nil {
		return nil, err
	}
	return p.store.Update(ctx, supervisor.ID, baseRevision, importEdit(reported, names))
}

// ImportAll imports every reported unmanaged slot of every live supervisor of
// the context's tenant, one revision bump per supervisor, and returns how many
// slots it imported. It needs no person: the board's setting calls it when it
// turns provisioning on, so no slot is stranded. A supervisor whose document
// changes while it runs is retried, and one that cannot be imported does not
// stop the others; the errors are joined.
func (p *ProvisionService) ImportAll(ctx context.Context) (int, error) {
	supervisors, err := p.agents.store.ListSupervisors(ctx, false)
	if err != nil {
		return 0, err
	}
	total := 0
	var errs []error
	for i := range supervisors {
		n, err := p.importAllOf(ctx, &supervisors[i])
		total += n
		if err != nil {
			errs = append(errs, fmt.Errorf("supervisor %s: %w", supervisors[i].Name, err))
		}
	}
	return total, errors.Join(errs...)
}

func (p *ProvisionService) importAllOf(ctx context.Context, supervisor *Agent) (int, error) {
	for range maxImportAttempts {
		reported, err := p.reportedSlotsOf(ctx, supervisor.ID)
		if err != nil {
			return 0, err
		}
		state, err := p.store.Load(ctx, supervisor.ID)
		if err != nil {
			return 0, err
		}
		slots := unmanaged(reported, state.Agents)
		if len(slots) == 0 {
			return 0, nil
		}
		names := make([]string, len(slots))
		for i := range slots {
			names[i] = slots[i].Name
		}
		next, err := p.store.Update(ctx, supervisor.ID, state.Revision, importEdit(reported, names))
		if errors.Is(err, ErrStaleRevision) || errors.Is(err, ErrDesiredNameHeld) {
			continue
		}
		if err != nil {
			return 0, err
		}
		p.logger.InfoContext(ctx, "audit", "action", "supervisor_desired_state_imported", "tenant", supervisor.Tenant,
			"supervisor", supervisor.ID, "revision", next.Revision, "agents", len(next.Agents), "imported", len(names))
		return len(names), nil
	}
	return 0, ErrStaleRevision
}
