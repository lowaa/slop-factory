package scriptedmodel

import "encoding/json"

// A Request is what an adapter extracts from one wire request: what a test
// asserts on about what the model was shown, whatever the wire API's shape.
type Request struct {
	// API names the adapter that parsed it ("anthropic", "openai",
	// "responses", "gemini").
	API       string            `json:"api"`
	Model     string            `json:"model"`
	System    string            `json:"system"`
	ToolNames []string          `json:"tool_names"`
	Messages  []json.RawMessage `json:"messages"`
	// MessageText is every piece of text the harness sent in its messages,
	// tool results included; ToolResults is the tool results alone.
	MessageText string   `json:"message_text"`
	ToolResults []string `json:"tool_results"`
	// Auxiliary marks a request the harness makes for itself (a quota probe,
	// a title), not a step of the conversation under test: it is answered
	// without consuming the script.
	Auxiliary bool            `json:"auxiliary"`
	Stream    bool            `json:"stream"`
	Raw       json.RawMessage `json:"raw"`
}

// HasTool reports whether the harness offered the named tool.
func (r *Request) HasTool(name string) bool {
	for _, n := range r.ToolNames {
		if n == name {
			return true
		}
	}
	return false
}

// A Recording is every request received since the scenario was loaded, and
// every unexpected-request failure.
type Recording struct {
	Requests []*Request
	Errors   []string
}

// Conversation is the requests that were steps of the conversation, without
// the harness's own housekeeping.
func (r *Recording) Conversation() []*Request {
	out := make([]*Request, 0, len(r.Requests))
	for _, req := range r.Requests {
		if !req.Auxiliary {
			out = append(out, req)
		}
	}
	return out
}

func (r *Request) normalise() *Request {
	if r.ToolNames == nil {
		r.ToolNames = []string{}
	}
	if r.Messages == nil {
		r.Messages = []json.RawMessage{}
	}
	if r.ToolResults == nil {
		r.ToolResults = []string{}
	}
	r.Auxiliary = len(r.ToolNames) == 0
	return r
}
