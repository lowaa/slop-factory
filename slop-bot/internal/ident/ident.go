// Package ident holds the identifiers every slop-bot package shares: tickets,
// agents, and who is making a call (the user, or one of the agents).
package ident

import "fmt"

// TicketID identifies a ticket within one TicketingSystem. It is a string
// because ticketing systems differ: local mode numbers its tickets, and
// others may not.
type TicketID string

// AgentID identifies one agent: the one slop-bot runs for one ticket.
type AgentID string

// PrincipalKind says who a Principal is.
type PrincipalKind int

const (
	// User is the person slop-bot runs for, calling through the CLI with no
	// agent token, or through the controller session.
	User PrincipalKind = iota + 1
	// Agent is one of slop-bot's agents, calling with its token.
	Agent
)

// Principal is who is making a call. The rules that tell the user and agents
// apart (only the user may set AutoMerge, an agent may edit only its own
// ticket's blockers, ...) key on it. They guard against a confused agent,
// not a malicious one: agents run as the user's uid (see the design doc,
// "Control socket").
type Principal struct {
	Kind PrincipalKind
	// Agent and Ticket are set only for Kind == Agent.
	Agent  AgentID
	Ticket TicketID
}

// UserPrincipal is the user.
func UserPrincipal() Principal { return Principal{Kind: User} }

// AgentPrincipal is the agent working ticket.
func AgentPrincipal(agent AgentID, ticket TicketID) Principal {
	return Principal{Kind: Agent, Agent: agent, Ticket: ticket}
}

// IsUser reports whether p is the user.
func (p Principal) IsUser() bool { return p.Kind == User }

// Valid reports whether p is a well-formed principal.
func (p Principal) Valid() bool {
	switch p.Kind {
	case User:
		return p.Agent == "" && p.Ticket == ""
	case Agent:
		return p.Agent != "" && p.Ticket != ""
	default:
		return false
	}
}

func (p Principal) String() string {
	if p.IsUser() {
		return "user"
	}
	return fmt.Sprintf("agent %s (ticket %s)", p.Agent, p.Ticket)
}
