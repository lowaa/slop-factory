package api

// PathHookTUI is POST: one hook a TUI harness ran in an agent's session,
// relayed by `slop-bot hook tui <session> <event>` with the agent's token.
const PathHookTUI = "/v1/hooks/tui"

// HookTUI is the hook: its session, event name and the payload the harness
// passed on stdin, as it came.
type HookTUI struct {
	Session string `json:"session"`
	Event   string `json:"event"`
	Payload []byte `json:"payload"`
}

// HookTUIReply is what the harness reads back on the hook's stdout.
type HookTUIReply struct {
	Reply []byte `json:"reply"`
}
