package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/user"
)

// A person's own supervisor-run Remote Controls, managed from the Remote
// Control page. A supervisor-run Remote Control is a conductor of whoever
// approved the supervisor, so that person, and only that person, may add or
// remove Remote Control entries in its desired state, whether or not they own
// the tenant: anyone else's entry would make a conductor that is not theirs.
// Every other kind of entry, and every other supervisor, stays the tenant
// owner's to edit on the board.

// maxOwnEditAttempts bounds re-reading a document another writer changed
// under an add or a remove.
const maxOwnEditAttempts = 3

var (
	// ErrNotApprover is a person acting on a supervisor they did not approve.
	ErrNotApprover = errors.New("agent: only the person who approved the supervisor may do that")
	// ErrProvisionOff is an own Remote Control asked for while the tenant does
	// not provision from the board, which the supervisor would never read.
	ErrProvisionOff = errors.New("agent: the team does not provision agents from the board")
	// ErrSupervisorOff is an own Remote Control added to or removed from a
	// supervisor someone switched off.
	ErrSupervisorOff = errors.New("agent: the supervisor is switched off")
)

// OwnRemoteControlSupervisor is a live supervisor who approved, with its
// Remote Control entries that it runs.
type OwnRemoteControlSupervisor struct {
	Supervisor *Agent
	Entries    []DesiredAgent
}

// OwnRemoteControlSupervisors is the live supervisors who approved, and
// whether the tenant provisions from the board; with it off, there are none.
func (p *ProvisionService) OwnRemoteControlSupervisors(ctx context.Context, who *authn.User) (bool, []OwnRemoteControlSupervisor, error) {
	if err := authn.RequirePerson(who); err != nil {
		return false, nil, err
	}
	on, err := p.settings.ProvisionFromBoardEnabled(ctx)
	if err != nil || !on {
		return false, nil, err
	}
	supervisors, err := p.agents.store.ListSupervisors(ctx, false)
	if err != nil {
		return false, nil, err
	}
	var out []OwnRemoteControlSupervisor
	for i := range supervisors {
		s := &supervisors[i]
		if s.Disabled() {
			continue
		}
		mine, err := p.approvedBy(ctx, who, s)
		if err != nil {
			return false, nil, err
		}
		if !mine {
			continue
		}
		state, err := p.store.Load(ctx, s.ID)
		if err != nil {
			return false, nil, err
		}
		out = append(out, OwnRemoteControlSupervisor{Supervisor: s, Entries: supervisorRunHosts(state.Agents)})
	}
	return true, out, nil
}

// AddOwnRemoteControl adds a Remote Control the supervisor is to run, which
// it makes as who's conductor. Owning one lets it put proposals in front of
// the person, so, like registering a conductor by hand, it needs a recent
// sign-in.
func (p *ProvisionService) AddOwnRemoteControl(ctx context.Context, who *authn.User, supervisorID uuid.UUID, name, harness string) (*DesiredAgent, error) {
	if err := user.RequireRecentAuth(who); err != nil {
		return nil, err
	}
	supervisor, err := p.ownSupervisor(ctx, who, supervisorID)
	if err != nil {
		return nil, err
	}
	name = strings.TrimSpace(name)
	// Entry names are unique among entries only: one an agent already holds
	// would leave the supervisor refused its conductor for good.
	if _, err := p.agents.store.GetByName(ctx, name); err == nil {
		return nil, fmt.Errorf("%w: %q", ErrNameTaken, name)
	} else if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	add := DesiredAgentEdit{Kind: ProvisionRemoteControl, Name: name, Harness: harness, State: StateRunning, Launch: LaunchSupervisor}
	next, err := p.editOwn(ctx, who, supervisor, func(current []DesiredAgent) ([]DesiredAgentEdit, error) {
		return append(asEdits(current), add), nil
	})
	if err != nil {
		return nil, err
	}
	for i := range next.Agents {
		if next.Agents[i].Name == add.Name {
			return &next.Agents[i], nil
		}
	}
	return nil, errors.New("agent: the added remote control is not in the document written")
}

// RemoveOwnRemoteControl marks the supervisor's Remote Control entry removed,
// which retires its conductor as a removal on the board does.
func (p *ProvisionService) RemoveOwnRemoteControl(ctx context.Context, who *authn.User, supervisorID, entryID uuid.UUID) error {
	supervisor, err := p.ownSupervisor(ctx, who, supervisorID)
	if err != nil {
		return err
	}
	_, err = p.editOwn(ctx, who, supervisor, func(current []DesiredAgent) ([]DesiredAgentEdit, error) {
		edits := asEdits(current)
		for i := range current {
			e := &current[i]
			if e.ID == entryID && e.Kind == ProvisionRemoteControl && e.Launch == LaunchSupervisor {
				edits[i].State = StateRemoved
				return edits, nil
			}
		}
		return nil, ErrNotFound
	})
	return err
}

// ownSupervisor is the live supervisor who approved, while the tenant
// provisions from the board. One that is not there reads as not found and
// another's as ErrNotApprover, before anything is said about the board.
func (p *ProvisionService) ownSupervisor(ctx context.Context, who *authn.User, supervisorID uuid.UUID) (*Agent, error) {
	if err := authn.RequirePerson(who); err != nil {
		return nil, err
	}
	supervisor, err := p.supervisorOf(ctx, supervisorID)
	if err != nil {
		return nil, err
	}
	mine, err := p.approvedBy(ctx, who, supervisor)
	if err != nil {
		return nil, err
	}
	if !mine {
		return nil, ErrNotApprover
	}
	if supervisor.Disabled() {
		return nil, ErrSupervisorOff
	}
	on, err := p.settings.ProvisionFromBoardEnabled(ctx)
	if err != nil {
		return nil, err
	}
	if !on {
		return nil, ErrProvisionOff
	}
	return supervisor, nil
}

func (p *ProvisionService) approvedBy(ctx context.Context, who *authn.User, supervisor *Agent) (bool, error) {
	owner, err := p.hostOwner(ctx, supervisor)
	if errors.Is(err, ErrGrantNoApprover) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return owner == who.ID, nil
}

// editOwn writes the edits change makes of the document as it is, read again
// when another writer got there first.
func (p *ProvisionService) editOwn(ctx context.Context, who *authn.User, supervisor *Agent,
	change func(current []DesiredAgent) ([]DesiredAgentEdit, error)) (*DesiredState, error) {
	for attempt := 1; ; attempt++ {
		state, err := p.store.Load(ctx, supervisor.ID)
		if err != nil {
			return nil, err
		}
		edits, err := change(state.Agents)
		if err != nil {
			return nil, err
		}
		next, err := p.write(ctx, who, supervisor, state.Revision, edits)
		if errors.Is(err, ErrStaleRevision) && attempt < maxOwnEditAttempts {
			continue
		}
		return next, err
	}
}

// asEdits is the document as a client sends it back unchanged.
func asEdits(current []DesiredAgent) []DesiredAgentEdit {
	edits := make([]DesiredAgentEdit, len(current))
	for i := range current {
		a := &current[i]
		id := a.ID
		edits[i] = DesiredAgentEdit{ID: &id, Kind: a.Kind, Name: a.Name, Harness: a.Harness, State: a.State, ResumeGeneration: a.ResumeGeneration}
		// Only a Remote Control has a launch on the wire; the others read as manual and refuse one.
		if a.Kind == ProvisionRemoteControl {
			edits[i].Launch = a.Launch
		}
	}
	return edits
}

// supervisorRunHosts is the document's Remote Control entries the supervisor runs.
func supervisorRunHosts(entries []DesiredAgent) []DesiredAgent {
	var out []DesiredAgent
	for i := range entries {
		if entries[i].Kind == ProvisionRemoteControl && entries[i].Launch == LaunchSupervisor {
			out = append(out, entries[i])
		}
	}
	return out
}
