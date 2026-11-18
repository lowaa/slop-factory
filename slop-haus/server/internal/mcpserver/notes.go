package mcpserver

import "strings"

// toolNotes is what addTool puts on every tool's structured output besides
// what the tool itself says: the human's words and a revocation notice. They
// are also the first text of the result, but Claude Code shows the structured
// content in place of the text when a tool returns it, so a field is the only
// form that always reaches the agent.
//
// Every tool output embeds it; addTool refuses to register one that doesn't,
// so a new tool can't carry the human's message in its text alone.
type toolNotes struct {
	// HumanMessage is what the human has said to the agent since its last
	// call. Act on it.
	HumanMessage string `json:"human_message,omitempty"`
	// Notice is a human having taken the agent's ticket away, or the like:
	// stop what you are doing for it.
	Notice string `json:"notice,omitempty"`
}

// carry adds what the preamble handed over; calling it again appends.
func (n *toolNotes) carry(heard, notice string) {
	n.HumanMessage = joinLines(n.HumanMessage, heard)
	n.Notice = joinLines(n.Notice, notice)
}

type noteCarrier interface {
	carry(heard, notice string)
}

func joinLines(parts ...string) string {
	var kept []string
	for _, p := range parts {
		if p != "" {
			kept = append(kept, p)
		}
	}
	return strings.Join(kept, "\n\n")
}
