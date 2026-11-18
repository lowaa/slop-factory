package ticket

import (
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

// A ticket may require roles of the agent that claims it. Roles are an
// authorization boundary for claiming only -- reading, commenting, blockers
// and the integration lock are open to every agent in the tenant -- and
// only a person can loosen one: agents add requirements when they file a
// ticket, and nothing an agent can call removes or relaxes them.
//
// The roles themselves, and which agents hold them, belong to
// internal/agent; this package queries those tables itself because the
// answer has to be part of the claim transaction to mean anything. The
// queries only ask whether an agent holds a role and repeat none of
// agent's rules, and the Postgres-backed claim tests would fail on a
// schema change under them.

// RoleRequirement is what a ticket is filed with, or has its requirement
// replaced by. People name roles by id; agents, through ticket_create, by
// name. Match empty means any.
type RoleRequirement struct {
	IDs   []uuid.UUID
	Names []string
	Match model.RoleMatch
}

func (r *RoleRequirement) empty() bool {
	return len(r.IDs) == 0 && len(r.Names) == 0
}

// matchOrDefault is Match, or any when it was left empty -- so a caller
// that skipped clean still files an ordinary ticket.
func (r *RoleRequirement) matchOrDefault() model.RoleMatch {
	if r.Match == "" {
		return model.RoleMatchAny
	}
	return r.Match
}

// clean defaults and checks Match, and drops duplicates.
func (r *RoleRequirement) clean() error {
	r.Match = r.matchOrDefault()
	if !r.Match.Valid() {
		return fmt.Errorf("%w: role_match %q (any or all)", ErrInvalidEnum, r.Match)
	}
	r.IDs = uniqueOf(r.IDs)
	r.Names = uniqueOf(r.Names)
	return nil
}

func uniqueOf[T comparable](in []T) []T {
	seen := make(map[T]bool, len(in))
	out := make([]T, 0, len(in))
	for _, v := range in {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

var (
	// ErrUnknownRole is a requirement naming a role the tenant doesn't have.
	ErrUnknownRole = errors.New("ticket: no such role")
	// ErrMissingRole matches a *MissingRoleError.
	ErrMissingRole = errors.New("ticket: agent lacks a role the ticket requires")
)

// MissingRoleError refuses a claim of one specific ticket whose required
// roles the agent doesn't meet. Said as such rather than as an empty
// queue: an agent told "queue empty" about a ticket it can see is open
// concludes the server is wrong, or retries.
type MissingRoleError struct {
	TicketID int64
	Roles    []string
	Match    model.RoleMatch
}

func (e *MissingRoleError) Error() string {
	return fmt.Sprintf("#%d needs %s, which you don't have. Only a person can change an agent's roles; "+
		"take other work.", e.TicketID, DescribeRoles(e.Roles, e.Match))
}

func (e *MissingRoleError) Is(target error) bool { return target == ErrMissingRole }

// DescribeRoles is a requirement in words: "role deploy", "one of the
// roles deploy, review", "all of the roles deploy, review".
func DescribeRoles(names []string, match model.RoleMatch) string {
	quoted := strings.Join(names, ", ")
	switch {
	case len(names) == 1:
		return "role " + quoted
	case match == model.RoleMatchAll:
		return "all of the roles " + quoted
	default:
		return "one of the roles " + quoted
	}
}
