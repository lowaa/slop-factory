package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
)

// DirectoryEntry is a conductor and the two facts about its use that every
// member may see.
type DirectoryEntry struct {
	Agent Agent
	// OwnerName is the owner's display name, nil once their account is deleted.
	OwnerName *string
	// HostSeenAt is when its host last polled; nil if it never has, or has
	// signed out.
	HostSeenAt *time.Time
	// HasLiveSession is whether a conversation is starting or ready on it.
	HasLiveSession bool
}

type Store interface {
	Create(ctx context.Context, id uuid.UUID, name string, kind Kind) (*Agent, error)
	Get(ctx context.Context, id uuid.UUID) (*Agent, error)
	GetByName(ctx context.Context, name string) (*Agent, error)
	// List is the tenant's workers; conductors are not in it. Archived ones
	// only when includeArchived.
	List(ctx context.Context, includeArchived bool) ([]Agent, error)
	// CreateConductor registers a conductor for ownerID, a member of the
	// context's tenant.
	CreateConductor(ctx context.Context, id uuid.UUID, name string, ownerID uuid.UUID, runBy *uuid.UUID) (*Agent, error)
	// ListConductors is the conductors ownerID registered, or all of the
	// tenant's when ownerID is nil.
	// Archived ones only when includeArchived.
	ListConductors(ctx context.Context, ownerID *uuid.UUID, includeArchived bool) ([]Agent, error)
	// ListConductorDirectory is all of the tenant's conductors with their
	// host's last poll and whether a conversation is live on them.
	ListConductorDirectory(ctx context.Context, includeArchived bool) ([]DirectoryEntry, error)
	// SetDisabled enabling an archived agent is ErrArchived.
	SetDisabled(ctx context.Context, id uuid.UUID, disabled bool) (*Agent, error)
	// SetArchived archiving an enabled agent is ErrNotDisabled.
	SetArchived(ctx context.Context, id uuid.UUID, archived bool) (*Agent, error)
	// Replace creates a worker or console that takes over from oldID and
	// disables and archives oldID, atomically. ErrNotReplaceable for an
	// agent that is archived or already replaced.
	Replace(ctx context.Context, newID uuid.UUID, name string, kind Kind, oldID uuid.UUID) (created, old *Agent, err error)
	// CreateSupervisor registers a supervisor allowed slotCap slots.
	CreateSupervisor(ctx context.Context, id uuid.UUID, name string, slotCap int) (*Agent, error)
	// SetSlotCap gives a live supervisor a new cap, returning it and the cap it
	// had. It takes the row lock slot creation takes, so a create sees the old
	// cap or the new one. Lowering below the live slots is allowed: nothing is
	// retired, and creates are refused until enough are. ErrNotFound for
	// anything but a supervisor, ErrArchived for an archived one.
	SetSlotCap(ctx context.Context, id uuid.UUID, slotCap int) (updated *Agent, previous int, err error)
	// ListSupervisors is the tenant's supervisors; archived ones only when
	// includeArchived.
	ListSupervisors(ctx context.Context, includeArchived bool) ([]Agent, error)
	// SupervisorLastSeen is the newest sign the supervisor is alive: its last
	// poll or its slots' newest report, or its creation when it has neither.
	SupervisorLastSeen(ctx context.Context, id uuid.UUID) (time.Time, error)
	// CreateSlot registers a worker for the supervisor, under a lock on the
	// supervisor's row. The kind is not a parameter: a slot is a worker.
	// ErrNotSupervisor for an agent that is not one, ErrIsDisabled or
	// ErrArchived for one that is not live, ErrSlotCapReached at the cap.
	CreateSlot(ctx context.Context, id, supervisorID uuid.UUID, name string) (*Agent, error)
	// AdoptSlot links an existing worker without a supervisor to the
	// supervisor, under the same lock and cap as CreateSlot. ErrNotFound for
	// no such agent or one of another supervisor, ErrNotAdoptable for a kind
	// other than worker, ErrArchived for a retired one.
	AdoptSlot(ctx context.Context, supervisorID uuid.UUID, name string) (*Agent, error)
	// EnsureSteward is the tenant's live steward, made by supervisorID if there is none.
	EnsureSteward(ctx context.Context, id, supervisorID uuid.UUID) (*Agent, error)
	// ListSlots is the supervisor's slots, archived ones only when asked.
	ListSlots(ctx context.Context, supervisorID uuid.UUID, includeArchived bool) ([]Agent, error)
	// DisableSlots switches off the supervisor's slots that are still
	// enabled, returning them.
	DisableSlots(ctx context.Context, supervisorID uuid.UUID, reason string) ([]Agent, error)
	// ListEnabled is every enabled agent of the context's tenant, whatever
	// its kind, so a kind added later is covered without a change here.
	ListEnabled(ctx context.Context) ([]Agent, error)
}

// Grants is what this package needs from OAuth: resolving an access token
// to the tenant and agent it was issued for, and cutting off every
// workspace signed in as an agent. Satisfied by *oauth.Service.
type Grants interface {
	Authenticate(ctx context.Context, accessToken string) (*Access, error)
	RevokeAgentGrants(ctx context.Context, tenantID, agentID uuid.UUID) error
}

// LeaseReleaser hands back what a disabled agent was holding when it was
// disabled at acquiredBy, sparing leases taken after that. Declared
// here as the minimal thing this package needs rather than importing the
// whole ticket service, and satisfied by *ticket.Service.
type LeaseReleaser interface {
	ReleaseLeasesFor(ctx context.Context, agentID uuid.UUID, reason string, acquiredBy time.Time) error
}

// ProposalVoider ends an agent's pending proposals once it has stopped being
// an enabled console. Satisfied by *pendingaction.Service.
type ProposalVoider interface {
	VoidAgent(ctx context.Context, agentID uuid.UUID, reason string) (int64, error)
}

// SlotCredentials issues grants for a supervisor's slots. Satisfied by
// *oauth.Service.
type SlotCredentials interface {
	IssueSlotCredential(ctx context.Context, tenantID, supervisorID, slotID uuid.UUID) (*SlotCredential, error)
	// SlotApprover is who approved the supervisor, for the audit lines.
	SlotApprover(ctx context.Context, tenantID, supervisorID uuid.UUID) (*uuid.UUID, error)
	// RevokeAgentCredential ends the grant a refresh token was issued under, when it is agentID's.
	RevokeAgentCredential(ctx context.Context, tenantID, agentID uuid.UUID, refreshToken string) error
}

// WithSlotCredentials lets a supervisor have credentials issued for its
// slots. Without it none can: issuing fails closed.
func WithSlotCredentials(c SlotCredentials) Option {
	return func(s *Service) { s.slotCredentials = c }
}

type Service struct {
	store           Store
	slotCredentials SlotCredentials
	releaser        LeaseReleaser
	grants          Grants
	voider          ProposalVoider
	members         TenantMembers
	logger          *slog.Logger
}

type Option func(*Service)

// WithProposalVoider makes disabling an agent and revoking its last grant void its pending proposals. Without it there
// are none to void.
func WithProposalVoider(v ProposalVoider) Option {
	return func(s *Service) { s.voider = v }
}

// TenantMembers says who belongs to a tenant. Satisfied by *user.Service.
type TenantMembers interface {
	IsTenantMember(ctx context.Context, userID, tenantID uuid.UUID) (bool, error)
}

// WithMembers lets Choose check that a conductor's owner is still a member.
// Without it no conductor can be chosen: the check fails closed.
func WithMembers(m TenantMembers) Option {
	return func(s *Service) { s.members = m }
}

func NewService(store Store, releaser LeaseReleaser, grants Grants, logHandler slog.Handler, opts ...Option) *Service {
	s := &Service{store: store, releaser: releaser, grants: grants, logger: slog.New(logHandler)}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

func (s *Service) voidProposals(ctx context.Context, agentID uuid.UUID, reason string) error {
	if s.voider == nil {
		return nil
	}
	if _, err := s.voider.VoidAgent(ctx, agentID, reason); err != nil {
		return fmt.Errorf("agent: void proposals: %w", err)
	}
	return nil
}

// GrantsEnded is called when an agent has no live OAuth grant left, so
// nothing can act as it any more. Satisfies oauth.GrantsEnded.
func (s *Service) GrantsEnded(ctx context.Context, tenantID, agentID uuid.UUID) error {
	ctx = db.WithTenant(ctx, tenantID)
	if err := s.voidProposals(ctx, agentID, "agent has no grants left"); err != nil {
		return err
	}
	// A supervisor with nothing left to act as it can no longer retire its
	// slots, so they would be left running with no one to stop them.
	return s.disableSlotsOf(ctx, agentID, "supervisor has no grants left")
}

// Create registers an agent. It has no credential of its own: a client is
// authorized to act as it through OAuth (see internal/oauth), and revoking
// that authorization or disabling the agent ends it.
func (s *Service) Create(ctx context.Context, name string) (*Agent, error) {
	return s.CreateKind(ctx, name, KindWorker)
}

// CreateKind is Create for a kind that is not the default. A conductor is
// registered through CreateConductor, which needs an owner.
func (s *Service) CreateKind(ctx context.Context, name string, kind Kind) (*Agent, error) {
	if name == "" {
		return nil, ErrEmptyName
	}
	if kind != KindWorker && kind != KindConsole {
		return nil, ErrBadKind
	}
	return s.store.Create(ctx, uuid.New(), name, kind)
}

// Choose is what an authorization's consent page picks: an existing,
// enabled agent in the user's tenant (a conductor only for its owner), or a
// new one by name, of the given kind (worker when empty). The kind is
// ignored for an existing agent, whose own never changes. It satisfies
// oauth.Agents.
func (s *Service) Choose(ctx context.Context, who *authn.User, agentID *uuid.UUID, name, kind string) (uuid.UUID, error) {
	if agentID == nil {
		if kind == "" {
			kind = string(KindWorker)
		}
		created, err := s.CreateKind(ctx, strings.TrimSpace(name), Kind(kind))
		if errors.Is(err, ErrNameTaken) {
			return uuid.Nil, ErrAgentNameTaken
		}
		if errors.Is(err, ErrBadKind) {
			return uuid.Nil, ErrAgentKindInvalid
		}
		if errors.Is(err, ErrEmptyName) {
			return uuid.Nil, ErrAgentUnavailable
		}
		if err != nil {
			return uuid.Nil, err
		}
		return created.ID, nil
	}
	found, err := s.pickable(ctx, who, *agentID, true, ErrAgentUnavailable)
	if err != nil {
		return uuid.Nil, err
	}
	return found.ID, nil
}

// ChooseSupervisor creates the supervisor a consent names, with its slot cap.
// It satisfies oauth.Agents. The cap is checked here as well as by the
// database: this is where an out-of-range one becomes a 422 rather than a 500.
func (s *Service) ChooseSupervisor(ctx context.Context, who *authn.User, name string, slotCap int) (uuid.UUID, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return uuid.Nil, ErrAgentUnavailable
	}
	if slotCap < 1 || slotCap > MaxSlotCap {
		return uuid.Nil, ErrSlotCapInvalid
	}
	created, err := s.store.CreateSupervisor(ctx, uuid.New(), name, slotCap)
	if errors.Is(err, ErrNameTaken) {
		return uuid.Nil, ErrAgentNameTaken
	}
	if err != nil {
		return uuid.Nil, err
	}
	s.logger.InfoContext(ctx, "audit", "action", "supervisor_created", "user_id", who.ID, "tenant", who.TenantID,
		"supervisor", created.ID, "slot_cap", slotCap)
	return created.ID, nil
}

// pickable loads the agent a consent names and applies the checks Choose and
// ChooseReplacing share, returning refusal for any that fails: it exists, is
// not a supervisor or steward, and if a conductor is the user's own. The
// disabled check is Choose's alone, since a replaced agent may be disabled.
func (s *Service) pickable(ctx context.Context, who *authn.User, id uuid.UUID, mustBeEnabled bool, refusal error) (*Agent, error) {
	found, err := s.store.Get(ctx, id)
	if errors.Is(err, ErrNotFound) {
		return nil, refusal
	}
	if err != nil {
		return nil, err
	}
	if mustBeEnabled && found.Disabled() {
		return nil, refusal
	}
	// A supervisor's or steward's credential is issued by a flow of its own,
	// not by any member picking it on the consent page.
	if found.IsSupervisor() || found.IsSteward() {
		return nil, refusal
	}
	// Approving this sign-in hands the signing-in machine the agent's
	// credential. A conductor's is its owner's to hand out: anyone else in
	// the tenant picking it would be using it.
	if found.IsConductor() {
		if err := s.requireOwnConductor(ctx, who, found, refusal); err != nil {
			return nil, err
		}
	}
	return found, nil
}

// requireOwnConductor is the rule for acting on a conductor from the consent
// page: its owner, still a member. refusal is what a failure reads as.
func (s *Service) requireOwnConductor(ctx context.Context, who *authn.User, conductor *Agent, refusal error) error {
	if !conductor.OwnedBy(who.ID) {
		return refusal
	}
	// Removing the owner disables it, but that is not what this
	// relies on: a former member is refused whatever its state.
	if s.members == nil {
		return refusal
	}
	member, err := s.members.IsTenantMember(ctx, who.ID, conductor.Tenant)
	if err != nil {
		return err
	}
	if !member {
		return refusal
	}
	return nil
}

// ChooseReplacing is Choose for a new agent that takes over from an existing
// one the user may disable: the old agent is disabled and archived in the
// same transaction that creates the new one, and its leases are released as
// for any disable. The new agent inherits nothing from it: not its
// credentials (it has none of its own; grants are per sign-in), and not its
// roles or variant either. Roles decide which tickets an agent may claim and
// the old agent held them as a grant to that identity; carrying them to an
// agent that may be of another kind, on a sign-in a person can make by hand,
// would widen it silently. They are one click on the Agents screen.
// It satisfies oauth.Agents.
//
// Only a worker or console, or the user's own conductor, can be replaced.
func (s *Service) ChooseReplacing(ctx context.Context, who *authn.User, replaces uuid.UUID, name, kind string) (uuid.UUID, error) {
	if kind == "" {
		kind = string(KindWorker)
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return uuid.Nil, ErrAgentUnavailable
	}
	if Kind(kind) != KindWorker && Kind(kind) != KindConsole {
		return uuid.Nil, ErrAgentKindInvalid
	}
	if _, err := s.pickable(ctx, who, replaces, false, ErrReplaceUnavailable); err != nil {
		return uuid.Nil, err
	}
	created, old, err := s.store.Replace(ctx, uuid.New(), name, Kind(kind), replaces)
	switch {
	case errors.Is(err, ErrNameTaken):
		return uuid.Nil, ErrAgentNameTaken
	case errors.Is(err, ErrNotReplaceable), errors.Is(err, ErrNotFound):
		return uuid.Nil, ErrReplaceUnavailable
	case err != nil:
		return uuid.Nil, err
	}
	if err := s.releaser.ReleaseLeasesFor(ctx, old.ID, "agent replaced", *old.DisabledAt); err != nil {
		return uuid.Nil, fmt.Errorf("agent: release leases on replace: %w", err)
	}
	s.logger.InfoContext(ctx, "audit", "action", "agent_replaced", "user_id", who.ID, "tenant", who.TenantID,
		"agent", created.ID, "replaced_agent", old.ID)
	return created.ID, nil
}

// MayPropose reports whether the agent may put proposals in front of a
// person: an enabled console, conductor or steward. Read from the
// database at call time, so flipping the flag off or disabling the agent
// refuses its very next call. Satisfies mcpserver.Proposers.
func (s *Service) MayPropose(ctx context.Context, agentID uuid.UUID) (bool, error) {
	found, err := s.store.Get(ctx, agentID)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return (found.IsConsole() || found.IsConductor() || found.IsSteward()) && !found.Disabled(), nil
}

// Enabled reports whether an agent may still act. Satisfies oauth.Agents.
func (s *Service) Enabled(ctx context.Context, agentID uuid.UUID) (bool, error) {
	found, err := s.store.Get(ctx, agentID)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return !found.Disabled(), nil
}

func (s *Service) Get(ctx context.Context, id uuid.UUID) (*Agent, error) {
	return s.store.Get(ctx, id)
}

// GetWorker is Get for the worker-facing routes: a conductor, a steward or a
// supervisor is not one of them, and reads as missing, so nothing reaching an agent by id
// from the Agents screen can act on someone's conductor, or hand out or change
// a supervisor.
func (s *Service) GetWorker(ctx context.Context, id uuid.UUID) (*Agent, error) {
	found, err := s.store.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if found.IsConductor() || found.IsSupervisor() || found.IsSteward() {
		return nil, ErrNotFound
	}
	return found, nil
}

// SetWorkerRoles replaces a worker's roles as the person who. Roles decide
// what an agent may claim, which is no business of a conductor, supervisor or
// steward: those read as not found. The person is checked first, so a caller
// that is not one learns nothing about which agents exist.
func (s *Service) SetWorkerRoles(ctx context.Context, who *authn.User, roles *RoleService, agentID uuid.UUID, roleIDs []uuid.UUID) ([]Role, error) {
	if err := authn.RequirePerson(who); err != nil {
		return nil, err
	}
	if _, err := s.GetWorker(ctx, agentID); err != nil {
		return nil, err
	}
	return roles.SetAgentRoles(ctx, who, agentID, roleIDs)
}

// getManageable is Get for what a person may switch off or put away from the
// Agents screen: a worker, a console or the steward. Not a conductor or a
// supervisor, which have screens of their own.
func (s *Service) getManageable(ctx context.Context, id uuid.UUID) (*Agent, error) {
	found, err := s.store.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if found.IsSteward() {
		return found, nil
	}
	return s.GetWorker(ctx, id)
}

// List is the tenant's workers, consoles and steward. Archived ones are left out
// unless includeArchived: they are put away, not gone, and anything that
// names an agent from history asks for them.
func (s *Service) List(ctx context.Context, includeArchived bool) ([]Agent, error) {
	return s.store.List(ctx, includeArchived)
}

// SetArchived puts a disabled worker or console away, or brings it back
// (still disabled). Nothing else about the agent changes, and it still
// resolves by id and name.
func (s *Service) SetArchived(ctx context.Context, who *authn.User, id uuid.UUID, archived bool) (*Agent, error) {
	if err := authn.RequirePerson(who); err != nil {
		return nil, err
	}
	if _, err := s.getManageable(ctx, id); err != nil {
		return nil, err
	}
	return s.setArchived(ctx, who, id, archived)
}

// setArchived is SetArchived for any kind of agent; who may call it is the
// caller's to decide.
func (s *Service) setArchived(ctx context.Context, who *authn.User, id uuid.UUID, archived bool) (*Agent, error) {
	updated, err := s.store.SetArchived(ctx, id, archived)
	if err != nil {
		return nil, err
	}
	s.logger.InfoContext(ctx, "audit", "action", "agent_archived_set", "user_id", who.ID, "tenant", who.TenantID, "agent", id, "archived", archived)
	return updated, nil
}

// SetDisabled switches an agent off or back on. Disabling also revokes its
// grants, so a token issued before is refused after it is switched back on,
// and releases whatever it was holding, so the ticket returns to the queue immediately
// rather than after the lease TTL elapses for an agent nobody is going to
// run.
func (s *Service) SetDisabled(ctx context.Context, id uuid.UUID, disabled bool) (*Agent, error) {
	if _, err := s.getManageable(ctx, id); err != nil {
		return nil, err
	}
	return s.setDisabled(ctx, id, disabled)
}

// setDisabled is SetDisabled for any kind of agent; who may call it is the
// caller's to decide.
func (s *Service) setDisabled(ctx context.Context, id uuid.UUID, disabled bool) (*Agent, error) {
	updated, err := s.store.SetDisabled(ctx, id, disabled)
	if err != nil {
		return nil, err
	}
	if !disabled {
		return updated, nil
	}
	// Order matters: the agent is marked disabled first, so a claim racing
	// this is refused by the disabled check rather than being granted a
	// ticket that is about to be released out from under it. The store's
	// disable also voided its proposals and revoked its grants. The two
	// steps are not one transaction, so the release is bounded by when this
	// disable happened: an agent re-enabled in between that took a new lease
	// keeps it.
	if err := s.releaser.ReleaseLeasesFor(ctx, id, "agent disabled", *updated.DisabledAt); err != nil {
		return nil, fmt.Errorf("agent: release leases on disable: %w", err)
	}
	if updated.IsSupervisor() {
		// The database disabled the slots with it; their leases are ours to release.
		slots, err := s.store.ListSlots(ctx, id, false)
		if err != nil {
			return nil, err
		}
		if err := s.releaseSlots(ctx, id, slots, "supervisor disabled"); err != nil {
			return nil, err
		}
	}
	return updated, nil
}

// DisableTenantAgents switches off every enabled agent of the tenant, of
// every kind, through the same path a single disable takes: grants revoked,
// proposals voided, and leases and the integration lock released. It is
// what archiving a tenant does to its agents; nothing switches them back on.
func (s *Service) DisableTenantAgents(ctx context.Context, tenantID uuid.UUID) error {
	ctx = db.WithTenant(ctx, tenantID)
	agents, err := s.store.ListEnabled(ctx)
	if err != nil {
		return err
	}
	for i := range agents {
		if _, err := s.setDisabled(ctx, agents[i].ID, true); err != nil {
			return fmt.Errorf("agent: disable %s: %w", agents[i].ID, err)
		}
	}
	return nil
}

// SignOutWorkspaces revokes every OAuth grant acting as the agent, so any
// workspace signed in as it -- an old checkout, or a leaked one -- stops
// working at its next request or refresh. It is the other half of handing
// out a new setup script for an existing agent: that script's device
// sign-in then issues a fresh grant.
//
// A disabled agent is refused rather than signed out: its workspaces
// already fail, and the new script's sign-in would be refused too (see
// Choose), so going ahead would only look like it had worked.
//
// The agent's lease is deliberately left alone. It is the same agent, and
// the re-set-up workspace can carry on with the ticket it held.
func (s *Service) SignOutWorkspaces(ctx context.Context, id uuid.UUID) (*Agent, error) {
	// Read in the caller's tenant first: the revoke below takes a tenant as
	// given, so it must come from an agent this caller can see.
	found, err := s.GetWorker(ctx, id)
	if err != nil {
		return nil, err
	}
	return s.signOut(ctx, found)
}

func (s *Service) signOut(ctx context.Context, found *Agent) (*Agent, error) {
	if found.Disabled() {
		return nil, ErrIsDisabled
	}
	if err := s.grants.RevokeAgentGrants(ctx, found.Tenant, found.ID); err != nil {
		return nil, fmt.Errorf("agent: revoke grants: %w", err)
	}
	return found, nil
}

// Authenticate resolves an access token to its agent, refusing a disabled
// one. Callers get ErrBadToken for an unknown, expired or revoked token and
// ErrIsDisabled for a known-but-switched-off agent -- distinguishable here
// so the server can log which happened, but both are a 401 on the wire.
func (s *Service) Authenticate(ctx context.Context, token string) (*Agent, error) {
	access, err := s.grants.Authenticate(ctx, token)
	if err != nil {
		return nil, ErrBadToken
	}
	// The token says which tenant; the agent itself is that tenant's data,
	// so it is read scoped to it.
	found, err := s.store.Get(db.WithTenant(ctx, access.TenantID), access.AgentID)
	if err != nil {
		return nil, ErrBadToken
	}
	if found.Disabled() {
		return nil, ErrIsDisabled
	}
	found.readOnly = access.ReadOnly
	return found, nil
}

// Name returns an agent's display name. The MCP checkin tool uses it to
// tell an agent who it is -- the token identifies it, but the name is what
// appears on the board.
func (s *Service) Name(ctx context.Context, id uuid.UUID) (string, error) {
	found, err := s.store.Get(ctx, id)
	if err != nil {
		return "", err
	}
	return found.Name, nil
}

// IsSteward reports whether the agent is an enabled steward, read at call
// time. Satisfies mcpserver.Stewards.
func (s *Service) IsSteward(ctx context.Context, agentID uuid.UUID) (bool, error) {
	found, err := s.store.Get(ctx, agentID)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return found.IsSteward() && !found.Disabled(), nil
}
