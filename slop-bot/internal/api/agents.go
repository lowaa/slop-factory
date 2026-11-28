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
	// PathTranscript streams an agent's transcript as transcript.Entry JSON
	// lines: ?tail=N, ?since=<RFC 3339>, ?full (untruncated), ?follow.
	PathTranscript = "/v1/transcript/{target}"
	// PathWatch streams an agent's conversation events as Event JSON lines.
	PathWatch = "/v1/watch/{target}"
)

// WatchStarted is the first event of a watch: whom it is watching. Its
// payload is a WatchTarget.
const WatchStarted = "watch.started"

// WatchTarget is WatchStarted's payload.
type WatchTarget struct {
	State string `json:"state"`
	Ended bool   `json:"ended,omitempty"`
}

// HarnessLimit is one of a harness's plan windows in GET /v1/status.
type HarnessLimit struct {
	Harness string `json:"harness"`
	// Window is which of the plan's windows; "" for a harness with one.
	Window      string    `json:"window,omitempty"`
	UsedPercent float64   `json:"used_percent"`
	ResetsAt    time.Time `json:"resets_at,omitzero"`
	ReportedAt  time.Time `json:"reported_at"`
	Threshold   float64   `json:"threshold"`
	// Held means the window is at the threshold: no new agent starts on
	// the harness until it resets.
	Held bool `json:"held,omitempty"`
}

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
	ID      string    `json:"id"`
	Ticket  string    `json:"ticket"`
	Title   string    `json:"title"`
	State   string    `json:"state"`
	Since   time.Time `json:"since"`
	Harness string    `json:"harness"`
	Model   string    `json:"model,omitempty"`
	Effort  string    `json:"effort,omitempty"`
	Pool    string    `json:"pool"`
	Note    string    `json:"note,omitempty"`
	Nudges  int       `json:"nudges,omitempty"`
}

// Status is GET /v1/status.
type Status struct {
	Agents   []AgentStatus `json:"agents"`
	Autonomy bool          `json:"autonomy"`
	// Budget and Outstanding are the autonomy budget and its use.
	Budget      int `json:"budget"`
	Outstanding int `json:"outstanding"`
	// Limits are the harnesses' plan allowances, as last reported.
	Limits []HarnessLimit `json:"limits"`
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
