// Package agentmsg is how a person steers an agent that is working: what
// they say is held server-side and handed to the agent on its next call to
// slop-haus, whichever call that is. See Render for what the agent reads.
//
// The direction is one way by construction. Nothing here takes an agent as
// an author, and the only routes that send are signed-in-person routes (see
// RegisterRoutes), so an agent's credential -- or a console agent's, which
// acts for a person only by proposing -- can never put words in another
// agent's context.
package agentmsg

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

// Kind is what a message is.
type Kind string

const (
	// KindMessage is one a person sent to the agent from the Agents screen.
	KindMessage Kind = "message"
	// KindTicketComment is a person's comment on a ticket the agent holds.
	KindTicketComment Kind = "ticket_comment"
)

const (
	// DefaultMaxBody is the longest message, in characters. Steering is a few
	// paragraphs -- "stop, wrong approach", "also handle X" -- and a message
	// is put in front of the agent whole, on whatever call it makes next, so
	// a long one is context the agent pays for on every delivery. Material
	// that big belongs in a ticket comment. The schema's trigger cuts
	// comments to the same bound.
	DefaultMaxBody = 4000
	// DefaultMaxPending is how many undelivered messages one agent may have.
	// An agent that never calls slop-haus (a closed terminal) would otherwise
	// accumulate a backlog that floods its first call; a person with that
	// many unanswered has a different problem.
	DefaultMaxPending = 20
)

// Message is one thing a person said to an agent.
type Message struct {
	ID      int64
	AgentID uuid.UUID
	Kind    Kind
	// AuthorUserID is always a person, until their account is deleted: then
	// it is nil and AuthorUserDeleted is set. AuthorName is their display
	// name, empty if they have none or are gone.
	AuthorUserID      *uuid.UUID
	AuthorUserDeleted bool
	AuthorName        string
	// ViaAgentName is set for a KindTicketComment an owner-bound agent (a
	// conductor, or a console with a live full-access grant) made for the
	// person, and is the agent's name. Empty when the person made it.
	ViaAgentName string
	// TicketID is set for a KindTicketComment.
	TicketID    *int64
	Body        string
	CreatedAt   time.Time
	DeliveredAt *time.Time
	// DroppedAt is set when the agent's lease on TicketID ended before the
	// message was delivered; it is then never delivered.
	DroppedAt *time.Time
}

var (
	ErrEmptyBody = errors.New("agentmsg: a message must not be empty")
	ErrTooLong   = errors.New("agentmsg: message is too long")
	// ErrAgentNotFound is also what another tenant's agent looks like.
	ErrAgentNotFound  = errors.New("agentmsg: no such agent")
	ErrAgentDisabled  = errors.New("agentmsg: agent is disabled")
	ErrTooManyPending = errors.New("agentmsg: agent has too many undelivered messages")
)

// Config is Service's limits; a zero field takes its default.
type Config struct {
	MaxBody    int
	MaxPending int
}
