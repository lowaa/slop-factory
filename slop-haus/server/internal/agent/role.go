package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

// Role is a tenant's named grouping of agents. A human assigns roles to
// agents; nothing an agent can call changes them. That is what lets a
// ticket require a role to be claimed: a role an agent could grant itself
// would gate nothing.
type Role struct {
	ID          uuid.UUID
	Name        string
	Description string
	CreatedAt   time.Time
}

const (
	maxRoleNameLen        = 64
	maxRoleDescriptionLen = 500
)

var (
	ErrRoleNotFound = errors.New("agent role: not found")
	// ErrRoleInUse matches a *RoleInUseError.
	ErrRoleInUse     = errors.New("agent role: tickets require it")
	ErrRoleNameTaken = errors.New("agent role: name already in use")
	ErrRoleInvalid   = errors.New("agent role: invalid")
)

// RoleInUseError refuses a role delete while tickets require the role,
// naming them. Deleting it anyway would have to clear it from them, which
// opens any ticket whose only requirement it was to every agent -- so the
// person deleting it is told which tickets, and asks for that explicitly.
type RoleInUseError struct {
	Tickets []model.TicketRef
}

func (e *RoleInUseError) Error() string {
	ids := make([]string, len(e.Tickets))
	for i, t := range e.Tickets {
		ids[i] = fmt.Sprintf("#%d", t.ID)
	}
	return fmt.Sprintf("agent role: required by %d ticket(s): %s", len(e.Tickets), strings.Join(ids, ", "))
}

func (e *RoleInUseError) Is(target error) bool { return target == ErrRoleInUse }

// RoleParams is what a role is created or edited with. On an edit, a nil
// field is left as it is.
type RoleParams struct {
	Name        *string
	Description *string
}

type RoleStore interface {
	ListRoles(ctx context.Context) ([]Role, error)
	CreateRole(ctx context.Context, id uuid.UUID, name, description string) (*Role, error)
	UpdateRole(ctx context.Context, id uuid.UUID, params *RoleParams) (*Role, error)
	// DeleteRole refuses with a *RoleInUseError while tickets require the
	// role, unless clearTickets, which removes it from them in the same
	// transaction.
	DeleteRole(ctx context.Context, id uuid.UUID, clearTickets bool) error
	// SetAgentRoles replaces the agent's whole set of roles.
	SetAgentRoles(ctx context.Context, agentID uuid.UUID, roleIDs []uuid.UUID) ([]Role, error)
	RolesFor(ctx context.Context, agentID uuid.UUID) ([]Role, error)
	// RolesByAgent is every agent's roles, for a listing, in one query.
	RolesByAgent(ctx context.Context) (map[uuid.UUID][]Role, error)
}

// RoleService holds the rules about roles; RoleStore only persists them.
//
// Every method acts in the context's tenant. Who may call them is decided
// at the transport: the REST routes are user-session only, and there is
// deliberately no MCP tool that reaches anything here but RoleNames.
type RoleService struct {
	store  RoleStore
	logger *slog.Logger
}

func NewRoleService(store RoleStore, logHandler slog.Handler) *RoleService {
	return &RoleService{store: store, logger: slog.New(logHandler)}
}

func (s *RoleService) List(ctx context.Context) ([]Role, error) {
	return s.store.ListRoles(ctx)
}

func (s *RoleService) Create(ctx context.Context, who *authn.User, name, description string) (*Role, error) {
	if err := authn.RequirePerson(who); err != nil {
		return nil, err
	}
	name, err := cleanRoleName(name)
	if err != nil {
		return nil, err
	}
	description, err = cleanRoleDescription(description)
	if err != nil {
		return nil, err
	}
	return s.store.CreateRole(ctx, uuid.New(), name, description)
}

func (s *RoleService) Update(ctx context.Context, who *authn.User, id uuid.UUID, params *RoleParams) (*Role, error) {
	if err := authn.RequirePerson(who); err != nil {
		return nil, err
	}
	cleaned := &RoleParams{}
	if params.Name != nil {
		name, err := cleanRoleName(*params.Name)
		if err != nil {
			return nil, err
		}
		cleaned.Name = &name
	}
	if params.Description != nil {
		description, err := cleanRoleDescription(*params.Description)
		if err != nil {
			return nil, err
		}
		cleaned.Description = &description
	}
	return s.store.UpdateRole(ctx, id, cleaned)
}

// Delete removes a role and, by cascade, every agent's membership of it.
// While tickets require it the delete is refused, naming them, unless
// clearTickets: then it is removed from those tickets too, which can open
// one to every agent. A person's call -- see RoleInUseError.
func (s *RoleService) Delete(ctx context.Context, who *authn.User, id uuid.UUID, clearTickets bool) error {
	if err := authn.RequirePerson(who); err != nil {
		return err
	}
	return s.store.DeleteRole(ctx, id, clearTickets)
}

// SetAgentRoles replaces an agent's roles with exactly roleIDs. Duplicates
// are ignored; an id that isn't one of this tenant's roles refuses the
// whole change, so a typo never leaves the agent with half of what was
// asked for.
func (s *RoleService) SetAgentRoles(ctx context.Context, who *authn.User, agentID uuid.UUID, roleIDs []uuid.UUID) ([]Role, error) {
	if err := authn.RequirePerson(who); err != nil {
		return nil, err
	}
	seen := make(map[uuid.UUID]bool, len(roleIDs))
	unique := make([]uuid.UUID, 0, len(roleIDs))
	for _, id := range roleIDs {
		if !seen[id] {
			seen[id] = true
			unique = append(unique, id)
		}
	}
	return s.store.SetAgentRoles(ctx, agentID, unique)
}

func (s *RoleService) RolesFor(ctx context.Context, agentID uuid.UUID) ([]Role, error) {
	return s.store.RolesFor(ctx, agentID)
}

func (s *RoleService) RolesByAgent(ctx context.Context) (map[uuid.UUID][]Role, error) {
	return s.store.RolesByAgent(ctx)
}

// RoleNames is the agent's roles by name, for checkin to tell it what it
// has. Read-only: an agent learning its roles is fine, changing them isn't.
func (s *RoleService) RoleNames(ctx context.Context, agentID uuid.UUID) ([]string, error) {
	roles, err := s.store.RolesFor(ctx, agentID)
	if err != nil {
		return nil, err
	}
	names := make([]string, len(roles))
	for i, r := range roles {
		names[i] = r.Name
	}
	return names, nil
}

func cleanRoleName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("%w: name must not be empty", ErrRoleInvalid)
	}
	if utf8.RuneCountInString(name) > maxRoleNameLen {
		return "", fmt.Errorf("%w: name must be at most %d characters", ErrRoleInvalid, maxRoleNameLen)
	}
	// A name is quoted back to agents in tool results and matched by
	// name when a ticket is filed, so it has to be one line of plain text.
	if strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return "", fmt.Errorf("%w: name must not contain control characters", ErrRoleInvalid)
	}
	return name, nil
}

func cleanRoleDescription(description string) (string, error) {
	description = strings.TrimSpace(description)
	if utf8.RuneCountInString(description) > maxRoleDescriptionLen {
		return "", fmt.Errorf("%w: description must be at most %d characters", ErrRoleInvalid, maxRoleDescriptionLen)
	}
	return description, nil
}
