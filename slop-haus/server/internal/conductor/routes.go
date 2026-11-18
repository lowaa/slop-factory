package conductor

import (
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"github.com/danielgtaylor/huma/v2/sse"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
)

// longPollWriteSlack is how long past the wait a poll's response may take to
// be written, on top of the wait itself.
const longPollWriteSlack = 10 * time.Second

// RegisterRoutes mounts the owner's routes (a signed-in person, who must own
// the conductor) and the host's (the conductor's own credential).
func RegisterRoutes(api huma.API, h *Handler) {
	const conductorsBase = "/api/conductors/{id}/conversations"
	const conversationBase = conductorsBase + "/{conversation_id}"
	user := authn.RequireUser()

	huma.Register(api, huma.Operation{
		OperationID: "startConversation", Metadata: user, Method: http.MethodPost, Path: conductorsBase,
		Summary:       "Start a conversation on the caller's remote control",
		Description:   "Owner only. The host attaches to its working copy as it is. A live conversation of the remote control is replaced.",
		DefaultStatus: http.StatusCreated,
	}, h.StartConversation)
	huma.Register(api, huma.Operation{
		OperationID: "getConductorHost", Metadata: user, Method: http.MethodGet, Path: "/api/conductors/{id}/host",
		Summary:     "Whether the remote control's host is there",
		Description: "Owner only.",
	}, h.GetConductorHost)
	huma.Register(api, huma.Operation{
		OperationID: "listConversations", Metadata: user, Method: http.MethodGet, Path: conductorsBase,
		Summary: "List the remote control's conversations, newest activity first", Description: "Owner only.",
	}, h.ListConversations)
	huma.Register(api, huma.Operation{
		OperationID: "getConversation", Metadata: user, Method: http.MethodGet, Path: conversationBase,
		Summary: "Read a conversation", Description: "Owner only.",
	}, h.GetConversation)
	huma.Register(api, huma.Operation{
		OperationID: "renameConversation", Metadata: user, Method: http.MethodPatch, Path: conversationBase,
		Summary:     "Name a conversation, or clear its name",
		Description: "Owner only. Any state. A null or blank title clears it, and the client shows first_line again.",
	}, h.RenameConversation)
	huma.Register(api, huma.Operation{
		OperationID: "listConversationEvents", Metadata: user, Method: http.MethodGet, Path: conversationBase + "/events",
		Summary: "Read a conversation's transcript after a seq", Description: "Owner only.",
	}, h.ListEvents)
	huma.Register(api, huma.Operation{
		OperationID: "sendConversationMessage", Metadata: user, Method: http.MethodPost, Path: conversationBase + "/messages",
		Summary:       "Send a message to a conversation, reconnecting it first when it has no process",
		Description:   "Owner only. A conversation that is not live resumes its harness session and replaces the live one. 429 when the host is far behind, 413 at the conversation's size limit.",
		DefaultStatus: http.StatusCreated,
	}, h.SendMessage)
	huma.Register(api, huma.Operation{
		OperationID: "disconnectConversation", Metadata: user, Method: http.MethodPost, Path: conversationBase + "/disconnect",
		Summary:     "End the conversation's harness process, keeping the session",
		Description: "Owner only. Also stops a turn in progress. The next message reconnects it. Disconnecting one with no process changes nothing.",
	}, h.DisconnectConversation)
	huma.Register(api, huma.Operation{
		OperationID: "interruptConversation", Metadata: user, Method: http.MethodPost, Path: conversationBase + "/interrupt",
		Summary:     "Stop the turn in flight, keeping the process and the session",
		Description: "Owner only. Records interrupt_requested and queues an interrupt for the host; the turn's end then carries interrupted. A repeat for the same turn changes nothing. 409 when that turn is not running, and when the remote control's host has not reported the interrupt capability (it is on older code: restart it).",
	}, h.InterruptConversation)
	huma.Register(api, huma.Operation{
		OperationID: "clearConversation", Metadata: user, Method: http.MethodPost, Path: conversationBase + "/clear",
		Summary:     "Clear the session's context, keeping the conversation and its transcript",
		Description: "Owner only. Queues a clear for the host, which reports a context_cleared divider and context_tokens 0. 409 while a reply is running, unless interrupt is true, which stops it first.",
	}, h.ClearConversation)
	huma.Register(api, huma.Operation{
		OperationID: "compactConversation", Metadata: user, Method: http.MethodPost, Path: conversationBase + "/compact",
		Summary:     "Compact the session's context",
		Description: "Owner only. Queues a compact for the host, which reports a context_compacted divider. 409 while a reply is running, unless interrupt is true, which stops it first; 409 when can_compact is false.",
	}, h.CompactConversation)
	huma.Register(api, huma.Operation{
		OperationID: "deleteConversation", Metadata: user, Method: http.MethodDelete, Path: conversationBase,
		Summary: "Delete a conversation and its transcript",
		Description: "Owner only. A live conversation is ended first. 409 while the host has not acknowledged work for it, " +
			"which a live one always has (its stop): the conversation is then ended, and deleting again after the host has caught up succeeds. " +
			"Usage already reported is kept.",
		DefaultStatus: http.StatusNoContent,
	}, h.DeleteConversation)
	sse.Register(api, huma.Operation{
		OperationID: "streamConversation", Metadata: user, Method: http.MethodGet, Path: conversationBase + "/stream",
		Summary:     "Stream a conversation's transcript events as they are stored",
		Description: "Owner only. Each event's id is its seq: reconnect with Last-Event-ID (the browser does) or after to miss nothing.",
		Middlewares: huma.Middlewares{h.streamGate(api)},
	}, map[string]any{"message": EventBody{}}, h.StreamConversation)

	agentAuth := authn.RequireAgent()
	huma.Register(api, huma.Operation{
		OperationID: "pollConductorWork", Metadata: agentAuth, Method: http.MethodPost, Path: "/api/conductor/poll",
		Summary: "A remote control's host long-polls for work: start, message and stop items",
		Description: "The conductor's own credential. Items are delivered at least once until acknowledged with ack, in order; " +
			"a second host process is refused (409) while the first polls.",
		Middlewares: huma.Middlewares{h.allowLongPoll},
	}, h.PollConductorWork)
	huma.Register(api, huma.Operation{
		OperationID: "releaseConductor", Metadata: agentAuth, Method: http.MethodPost, Path: "/api/conductor/release",
		Summary: "A remote control's host that is exiting gives the conductor up",
		Description: "The conductor's own credential. Clears the host if it is still host_id, so the next process polls at once " +
			"rather than after the host timeout; any other host_id, or none, changes nothing.",
		DefaultStatus: http.StatusNoContent,
	}, h.ReleaseConductor)
	huma.Register(api, huma.Operation{
		OperationID: "reportConductorLayout", Metadata: agentAuth, Method: http.MethodPost, Path: "/api/conductor/layout",
		Summary: "A remote control's host reports what its workspace looks like on disk",
		Description: "The conductor's own credential. Replaces the last report; the Workspace page shows it beside the manifest. " +
			"Self-asserted: it is shown to people, never used to decide what the conductor may do.",
		DefaultStatus: http.StatusNoContent,
	}, h.ReportLayout)
	huma.Register(api, huma.Operation{
		OperationID: "reportConductorEvents", Metadata: agentAuth, Method: http.MethodPost, Path: "/api/conductor/conversations/{conversation_id}/events",
		Summary: "A remote control's host reports a conversation's output, turn ends and repo state",
		Description: "The conductor's own credential. Events carry the host's own numbers and are stored once and in order: " +
			"a batch sent again is skipped, one that skips a number is refused (409). Usage goes to /hooks/usage with the conversation id.",
		MaxBodyBytes: 1 << 20,
	}, h.ReportConductorEvents)
}

// allowLongPoll lifts the server-wide write timeout for a poll, which is
// held open on purpose: for as long as the service holds one, and a little
// more for the response to be written. The read deadline is left alone.
func (h *Handler) allowLongPoll(ctx huma.Context, next func(huma.Context)) {
	if _, w := humago.Unwrap(ctx); w != nil {
		// Only a writer that does not support deadlines fails here, and the
		// poll then still works, with the server's shorter write deadline.
		_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(h.svc.MaxWait() + longPollWriteSlack))
	}
	next(ctx)
}
