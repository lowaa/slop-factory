package api

import "time"

// Ticket routes.
const (
	PathTickets = "/v1/tickets"
	// PathTicket and the routes under it take the ticket id.
	PathTicket         = "/v1/tickets/{id}"
	PathTicketAssign   = "/v1/tickets/{id}/assign"
	PathTicketComments = "/v1/tickets/{id}/comments"
	PathTicketBlockers = "/v1/tickets/{id}/blockers"
	PathTicketBlocker  = "/v1/tickets/{id}/blockers/{blocker}"
)

// Principal is who did something, as the CLI shows it.
type Principal struct {
	// Kind is "user" or "agent".
	Kind   string `json:"kind"`
	Agent  string `json:"agent,omitempty"`
	Ticket string `json:"ticket,omitempty"`
}

// AgentSpec is a ticket's harness, model and effort.
type AgentSpec struct {
	Harness string `json:"harness,omitempty"`
	Model   string `json:"model,omitempty"`
	Effort  string `json:"effort,omitempty"`
}

// AgentSpecChange is an edit to a ticket's AgentSpec: only the fields given
// change, so setting one doesn't blank the others. An empty string given
// clears the field back to the configured default.
type AgentSpecChange struct {
	Harness *string `json:"harness,omitempty"`
	Model   *string `json:"model,omitempty"`
	Effort  *string `json:"effort,omitempty"`
}

// TicketRef is the other end of a blocker.
type TicketRef struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Status string `json:"status"`
}

// Comment is one comment.
type Comment struct {
	ID        string    `json:"id"`
	Author    Principal `json:"author"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
}

// BlockerEdge is one blocker edge.
type BlockerEdge struct {
	Ticket  TicketRef `json:"ticket"`
	AddedBy Principal `json:"added_by"`
}

// Ticket is a ticket as the CLI sees it.
type Ticket struct {
	ID           string      `json:"id"`
	Title        string      `json:"title"`
	Body         string      `json:"body,omitempty"`
	Status       string      `json:"status"`
	Assignee     string      `json:"assignee,omitempty"`
	BlockedBy    []TicketRef `json:"blocked_by,omitempty"`
	Agent        AgentSpec   `json:"agent"`
	AutoMerge    bool        `json:"auto_merge"`
	Urgent       bool        `json:"urgent"`
	UrgentReason string      `json:"urgent_reason,omitempty"`
	CreatedBy    Principal   `json:"created_by"`
	Parent       string      `json:"parent,omitempty"`
	Repos        []string    `json:"repos,omitempty"`
	Comments     []Comment   `json:"comments,omitempty"`
	// Blockers are both directions, on GET of one ticket only.
	Blockers *Blockers `json:"blockers,omitempty"`
	// SpecError is set when the agent spec doesn't validate on this
	// machine: the ticket won't be scheduled until it's fixed.
	SpecError string    `json:"spec_error,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Blockers are a ticket's edges both ways.
type Blockers struct {
	BlockedBy []BlockerEdge `json:"blocked_by"`
	Blocks    []BlockerEdge `json:"blocks"`
}

// NewTicket is POST /v1/tickets.
type NewTicket struct {
	Title        string    `json:"title"`
	Body         string    `json:"body,omitempty"`
	Status       string    `json:"status,omitempty"`
	Agent        AgentSpec `json:"agent"`
	AutoMerge    bool      `json:"auto_merge,omitempty"`
	Urgent       bool      `json:"urgent,omitempty"`
	UrgentReason string    `json:"urgent_reason,omitempty"`
	Repos        []string  `json:"repos,omitempty"`
	BlockedOn    []string  `json:"blocked_on,omitempty"`
	// Assign, when true, assigns the ticket to slop-bot's user.
	Assign bool `json:"assign,omitempty"`
}

// TicketChange is PATCH /v1/tickets/{id}; absent fields are left alone.
type TicketChange struct {
	Title        *string          `json:"title,omitempty"`
	Body         *string          `json:"body,omitempty"`
	Status       *string          `json:"status,omitempty"`
	Agent        *AgentSpecChange `json:"agent,omitempty"`
	AutoMerge    *bool            `json:"auto_merge,omitempty"`
	Urgent       *bool            `json:"urgent,omitempty"`
	UrgentReason string           `json:"urgent_reason,omitempty"`
	Repos        *[]string        `json:"repos,omitempty"`
}

// Assign is POST /v1/tickets/{id}/assign. A blank Assignee unassigns; "me"
// is slop-bot's user.
type Assign struct {
	Assignee string `json:"assignee"`
}

// NewComment is POST /v1/tickets/{id}/comments.
type NewComment struct {
	Body string `json:"body"`
}

// NewBlocker is POST /v1/tickets/{id}/blockers.
type NewBlocker struct {
	On string `json:"on"`
}

// BlockerChange answers adding or removing a blocker.
type BlockerChange struct {
	Ticket  Ticket `json:"ticket"`
	Changed bool   `json:"changed"`
}
