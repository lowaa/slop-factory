package api

import (
	"encoding/json"
	"time"
)

// Agent and messaging routes.
const (
	PathTell         = "/v1/tell"
	PathInbox        = "/v1/inbox"
	PathAsk          = "/v1/ask"
	PathAsks         = "/v1/asks"
	PathDiscuss      = "/v1/discuss"
	PathDiscussions  = "/v1/discussions"
	PathResolve      = "/v1/discussions/{ticket}/resolve"
	PathReadyToMerge = "/v1/ready-to-merge"
	PathDone         = "/v1/done"
	PathNote         = "/v1/status/note"
	PathStatus       = "/v1/status"
	PathEvents       = "/v1/events"
	PathAutonomy     = "/v1/autonomy"
)

// Tell is POST /v1/tell.
type Tell struct {
	// To is "user", an agent id, or a ticket id; ignored with All.
	To        string `json:"to,omitempty"`
	Text      string `json:"text"`
	All       bool   `json:"all,omitempty"`
	Interrupt bool   `json:"interrupt,omitempty"`
}

// Told answers POST /v1/tell.
type Told struct {
	Recipients int `json:"recipients"`
}

// Message is one inbox message.
type Message struct {
	ID        int64     `json:"id"`
	From      string    `json:"from"`
	To        string    `json:"to"`
	Kind      string    `json:"kind"`
	Body      string    `json:"body"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}

// Ask is POST /v1/ask, and an entry of GET /v1/asks.
type Ask struct {
	ID        int64     `json:"id,omitempty"`
	Agent     string    `json:"agent,omitempty"`
	Question  string    `json:"question"`
	Options   []string  `json:"options,omitempty"`
	Recommend string    `json:"recommend,omitempty"`
	CreatedAt time.Time `json:"created_at,omitzero"`
}

// Discuss is POST /v1/discuss.
type Discuss struct {
	Decision  string   `json:"decision"`
	Options   []string `json:"options,omitempty"`
	Recommend string   `json:"recommend,omitempty"`
}

// Resolve is POST /v1/discussions/{ticket}/resolve.
type Resolve struct {
	Resolution string `json:"resolution"`
}

// ReadyToMerge is POST /v1/ready-to-merge.
type ReadyToMerge struct {
	PR string `json:"pr,omitempty"`
}

// Done is POST /v1/done.
type Done struct {
	Summary string `json:"summary"`
}

// Note is POST /v1/status/note.
type Note struct {
	Note string `json:"note"`
}

// AgentStatus is one agent in GET /v1/status.
type AgentStatus struct {
	ID     string `json:"id"`
	Ticket string `json:"ticket"`
	Title  string `json:"title"`
	// TicketStatus is the ticket's own status, e.g. preparing or in_progress.
	TicketStatus string `json:"ticket_status,omitempty"`
	// Blockers are the ticket's blockers that aren't done.
	Blockers []string  `json:"blockers,omitempty"`
	State    string    `json:"state"`
	Since    time.Time `json:"since"`
	Harness  string    `json:"harness"`
	Model    string    `json:"model,omitempty"`
	Effort   string    `json:"effort,omitempty"`
	Pool     string    `json:"pool"`
	Note     string    `json:"note,omitempty"`
	Nudges   int       `json:"nudges,omitempty"`
}

// Status is GET /v1/status.
type Status struct {
	Agents   []AgentStatus `json:"agents"`
	Autonomy bool          `json:"autonomy"`
	// Budget and Outstanding are the autonomy budget and its use.
	Budget      int         `json:"budget"`
	Outstanding int         `json:"outstanding"`
	Pools       []PoolUsage `json:"pools,omitempty"`
	// Allowances are plan allowances per harness; empty until the daemon
	// tracks them.
	Allowances []Allowance `json:"allowances,omitempty"`
}

// PoolUsage is how many of a pool's slots are in a turn.
type PoolUsage struct {
	Pool   string `json:"pool"`
	Active int    `json:"active"`
	Max    int    `json:"max"`
}

// Allowance is a harness's plan allowance.
type Allowance struct {
	Harness     string    `json:"harness"`
	UsedPercent float64   `json:"used_percent"`
	ResetsAt    time.Time `json:"resets_at,omitzero"`
}

// Event is one entry of GET /v1/events.
type Event struct {
	Seq     int64           `json:"seq"`
	At      time.Time       `json:"at"`
	Type    string          `json:"type"`
	Agent   string          `json:"agent,omitempty"`
	Ticket  string          `json:"ticket,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// Autonomy is GET and POST /v1/autonomy.
type Autonomy struct {
	Enabled bool `json:"enabled"`
}
