package api

// PathPreTool is POST: the merge guard's verdict on one of the calling
// agent's tool calls, asked by `slop-bot hook pre-tool`.
const PathPreTool = "/v1/guard/pre-tool"

// PreTool is a tool call about to run.
type PreTool struct {
	Tool    string `json:"tool"`
	Command string `json:"command,omitempty"`
	Cwd     string `json:"cwd,omitempty"`
}

// PreToolVerdict is the guard's answer.
type PreToolVerdict struct {
	Allow bool `json:"allow"`
	// Merge says what merge the call was found to be, when it is one.
	Merge  string `json:"merge,omitempty"`
	Reason string `json:"reason,omitempty"`
}
