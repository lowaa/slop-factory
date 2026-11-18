// Package agent is the durable identity a lease hangs off: a named agent,
// which clients are authorized to act as through OAuth (internal/oauth).
//
// The credential exists so authorship is trustworthy, not because a home
// cluster has attackers. When two agents disagree about who broke the build, an
// unauthenticated record is unreliable exactly when it is needed most.
package agent

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

// Kind is what an agent is for. A worker takes tickets from the queue; a
// conductor is a server-dispatched session that belongs to one user and
// proposes like a console does; a console is the person's own interactive
// session, which proposes human actions for approval and claims only the
// ticket it is told to. A supervisor creates and retires workers of its own
// (its slots), up to a cap, and does nothing else.
type Kind string

const (
	KindWorker     Kind = "worker"
	KindConductor  Kind = "conductor"
	KindConsole    Kind = "console"
	KindSupervisor Kind = "supervisor"
	// KindSteward is a server-side helper that proposes on the owner's
	// behalf and records, per proposal, which routine category it believes
	// the proposal falls in (see internal/steward). It claims no tickets.
	KindSteward Kind = "steward"
)

// MaxSlotCap is the most slots a supervisor may be given. The database
// refuses a slot_cap above it too (agents_slot_cap_range), so raising it takes
// a migration.
const MaxSlotCap = 16

// AgentRef names another agent.
type AgentRef struct {
	ID   uuid.UUID
	Name string
}

type Agent struct {
	ID         uuid.UUID
	Tenant     uuid.UUID
	Name       string
	CreatedAt  time.Time
	DisabledAt *time.Time
	// ArchivedAt is when a person put a disabled agent away. It stays
	// readable by id and name, so history still names it; only the lists
	// leave it out.
	ArchivedAt *time.Time
	// Kind is fixed when the agent is registered; the database refuses to
	// change it.
	Kind Kind
	// OwnerUserID is the signed-in person who registered a conductor, and the
	// only one who may use it. Set for a conductor and for nothing else; the
	// database refuses to change it. Nothing else links an agent to a user.
	OwnerUserID *uuid.UUID
	// OwnerUserDeleted says OwnerUserID is nil because the owner's account
	// was deleted, which leaves a conductor nobody can use.
	OwnerUserDeleted bool
	// SupervisorID is the supervisor that created this worker as one of its
	// slots; nil for every other agent. The database refuses to change it.
	SupervisorID *uuid.UUID
	// RunBySupervisorID is the supervisor that made this conductor for a
	// Remote Control it runs; nil for a hand-registered one and for every
	// other kind.
	RunBySupervisorID *uuid.UUID
	// SlotCap is how many live slots a supervisor may have; nil for every
	// other agent.
	SlotCap *int
	// Replaces is the agent this one took over from when it was authorized,
	// and ReplacedBy the one that took over from this. Names are carried so
	// history can say "replaced by" without a second lookup.
	Replaces   *AgentRef
	ReplacedBy *AgentRef
	// readOnly is how this agent was authenticated, not something stored
	// about it: the same agent may hold a read-only credential and a
	// full one. Set only by Service.Authenticate.
	readOnly bool
}

// AgentID and AgentName satisfy authn.Identity, so an authenticated agent
// can be carried on a request context without that package having to
// import this one (which would close a cycle -- see authn.Identity).
func (a *Agent) AgentID() uuid.UUID { return a.ID }

func (a *Agent) AgentName() string { return a.Name }

func (a *Agent) TenantID() uuid.UUID { return a.Tenant }

func (a *Agent) ReadOnly() bool { return a.readOnly }

// Disabled reports whether this agent has been switched off. A disabled
// agent fails authentication on every transport, is skipped by the claim
// path, and has any active lease force-released -- defined once here so the
// three transports can't each invent their own answer.
func (a *Agent) Disabled() bool { return a.DisabledAt != nil }

func (a *Agent) Archived() bool { return a.ArchivedAt != nil }

// AgentKind names the agent's kind for aggregate usage counts, which hold no
// agent id (mcpserver.ToolUsage).
func (a *Agent) AgentKind() string { return string(a.Kind) }

func (a *Agent) IsConductor() bool { return a.Kind == KindConductor }

func (a *Agent) IsSteward() bool { return a.Kind == KindSteward }

func (a *Agent) IsSupervisor() bool { return a.Kind == KindSupervisor }

// IsConsole is true for the person's own interactive session, which may
// propose human actions for approval (see internal/pendingaction).
func (a *Agent) IsConsole() bool { return a.Kind == KindConsole }

// OwnedBy reports whether userID registered this conductor. A worker has no
// owner, so it is owned by nobody.
func (a *Agent) OwnedBy(userID uuid.UUID) bool {
	return a.OwnerUserID != nil && *a.OwnerUserID == userID
}

var (
	ErrNotFound  = errors.New("agent: not found")
	ErrNameTaken = errors.New("agent: name already in use")
	// ErrNameRetired is a supervisor's slot name held by one of its own
	// retired slots that did work, so it cannot be reused.
	ErrNameRetired = errors.New("agent: name held by a retired slot of this supervisor")
	// ErrNotAdoptable is a supervisor adopting an agent that is not a worker.
	ErrNotAdoptable = errors.New("agent: only a worker can be adopted as a slot")
	ErrEmptyName    = errors.New("agent: name must not be empty")
	ErrBadToken     = errors.New("agent: unknown or invalid token")
	ErrIsDisabled   = errors.New("agent: disabled")
	// ErrNotDisabled is archiving an agent that is still enabled: it must be
	// disabled first, which also ends what it was holding.
	ErrNotDisabled = errors.New("agent: not disabled")
	// ErrNotReplaceable is replacing an agent that is archived, already
	// replaced, or not the caller's to replace.
	ErrNotReplaceable = errors.New("agent: cannot be replaced")
	// ErrArchived is enabling an archived agent: it is unarchived first.
	ErrArchived = errors.New("agent: archived")
	ErrBadKind  = errors.New("agent: kind must be worker or console")
	// ErrNotSupervisor is asking a slot operation of an agent that is not a
	// supervisor.
	ErrNotSupervisor = errors.New("agent: not a supervisor")

	// ErrNotSupervisorCaller is a request that is not from a live supervisor's
	// own credential.
	ErrNotSupervisorCaller = errors.New("agent: not an enabled supervisor's own credential")

	// ErrSlotCapReached is creating a slot when the supervisor already has
	// slot_cap live ones.
	ErrSlotCapReached = errors.New("agent: supervisor is at its slot cap")
	// ErrNotOwner is a user using a conductor they did not register. Tenant
	// owners are refused too: they may disable a conductor, never use it.
	ErrNotOwner = errors.New("agent: only the conductor's owner may use it")
	// ErrRunBySupervisor is archiving a Remote Control a supervisor runs,
	// which is removed from that supervisor instead.
	ErrRunBySupervisor = errors.New("agent: the remote control is run by a supervisor")
	// ErrNotTenantOwner is a user asking for what only the tenant's owner
	// may have.
	ErrNotTenantOwner = errors.New("agent: only the tenant's owner may do this")
)
