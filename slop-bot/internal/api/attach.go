package api

// Attach routes. {agent} is an agent id, or a ticket id for its live agent.
const (
	// PathAttach streams AttachFrames for as long as the attach lasts: the
	// open connection is the lease.
	PathAttach   = "/v1/agents/{agent}/attach"
	PathLaunched = "/v1/agents/{agent}/attach/launched"
	PathHandBack = "/v1/agents/{agent}/handback"
	PathDetach   = "/v1/agents/{agent}/detach"
)

// AttachRequest is POST /v1/agents/{agent}/attach.
type AttachRequest struct {
	// Holder names the attaching CLI; it holds the lease.
	Holder string `json:"holder"`
	// Now interrupts the agent's turn instead of waiting for it.
	Now bool `json:"now,omitempty"`
	// NoResume releases the agent to the person for good.
	NoResume bool `json:"no_resume,omitempty"`
	// Reattach is the holder reconnecting after a daemon restart.
	Reattach bool `json:"reattach,omitempty"`
}

// Attach frame types.
const (
	FrameAttached  = "attached"
	FrameHeartbeat = "heartbeat"
	// FrameDetach asks the CLI to end its TUI and hand the agent back.
	FrameDetach = "detach"
	// FrameEnded says the attach is over without the CLI: its TUI must go.
	FrameEnded = "ended"
)

// AttachFrame is one line of the attach stream.
type AttachFrame struct {
	Type   string      `json:"type"`
	Attach *Attachment `json:"attach,omitempty"`
	Reason string      `json:"reason,omitempty"`
}

// Attachment is the attached agent, as the CLI shows and runs it.
type Attachment struct {
	Agent  string    `json:"agent"`
	Ticket string    `json:"ticket"`
	Title  string    `json:"title"`
	State  string    `json:"state"`
	Note   string    `json:"note,omitempty"`
	Held   []Message `json:"held,omitempty"`
	// Command is the harness's TUI on the conversation; nil on a reattach.
	Command *Command `json:"command,omitempty"`
	// Released: slop-bot no longer runs the agent, and nothing hands back.
	Released bool `json:"released,omitempty"`
}

// Command is a process for the CLI to run in the person's terminal.
type Command struct {
	Path string   `json:"path"`
	Args []string `json:"args"`
	// Env is the whole environment, agent token included.
	Env []string `json:"env"`
	Dir string   `json:"dir"`
}

// Launched is POST /v1/agents/{agent}/attach/launched: the TUI's pid, so a
// hang-up can make sure it's gone before the conversation resumes.
type Launched struct {
	Holder string `json:"holder"`
	PID    int    `json:"pid"`
}

// HandBack is POST /v1/agents/{agent}/handback.
type HandBack struct {
	Holder string `json:"holder"`
}

// HandedBack answers a hand-back or a detach: resumed, finished or ended.
type HandedBack struct {
	Outcome string `json:"outcome"`
}
