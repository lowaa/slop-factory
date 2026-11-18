package mcpserver

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"strings"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/pendingaction"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

type Handler struct {
	tickets  Tickets
	messages Messages
	sessions Sessions
	agents   Agents
	roles    Roles
	// workspace may be nil.
	workspace Workspace
	// manifests may be nil: workspace_get is then not registered.
	manifests Manifests
	// proposers and proposals are both set or both nil: without them the
	// console tools are not registered.
	proposers Proposers
	proposals Proposals
	// stewards and assessor are both set or both nil: without them
	// steward_assess is not registered.
	stewards Stewards
	assessor Assessor
	// drafter is set with stewards: without it steward_draft is not
	// registered.
	drafter Drafter
	// labeller is set with stewards: without it steward_label_discussion is
	// not registered.
	labeller DiscussionLabeller
	// drafts may be nil: list_proposals then shows no drafts.
	drafts pendingaction.Drafts
	// batches may be nil: landing_batches is then not registered.
	batches Batches
	// usage may be nil: working_on is then not registered.
	usage  Declarer
	events *audit.Recorder
	// refusals may be nil: refused writes are then audited but not counted.
	refusals Refusals
	// toolUsage may be nil: calls are then not counted.
	toolUsage ToolUsage
	boardURL  string
	logger    *slog.Logger
	// changes may be nil: the change tools are then not registered.
	changes Changes
	// lander may be nil: ticket_finish then never enqueues for landing.
	lander Lander
	// queueMode may be nil: the lock tools are then offered to every tenant.
	queueMode QueueMode

	// attachments may be nil: the attachment tools are then not registered.
	attachments Attachments
	// reviews may be nil: the slop-review tools are then not registered.
	reviews Reviews

	boundary ticket.ExternalBoundary
	// discussions may be nil: ticket_get then names no discussion label.
	discussions ticket.AttemptDiscussions
	// labelToWorkers is the one flag on who sees the label; see Deps.
	labelToWorkers bool
}

type Deps struct {
	Tickets Tickets
	// Messages carries the human's words to the agent on its calls; nil
	// delivers none.
	Messages Messages
	Sessions Sessions
	Agents   Agents
	Roles    Roles
	// Workspace is optional.
	Workspace Workspace
	// Manifests switches workspace_get on; a server given none does not offer it.
	Manifests Manifests
	// Proposers and Proposals together switch the console tools on; a server
	// given neither does not offer them.
	Proposers Proposers
	Proposals Proposals
	// Stewards and Assessor together switch steward_assess on; a server
	// given neither does not offer it.
	Stewards Stewards
	Assessor Assessor
	// Drafter (with Stewards) switches steward_draft on; Drafts, when set,
	// shows each of a console's proposals the steward's draft of it.
	Drafter Drafter
	Drafts  pendingaction.Drafts
	// Labeller (with Stewards) switches steward_label_discussion on.
	Labeller DiscussionLabeller
	// Batches switches landing_batches on; a server given none does not offer it.
	Batches Batches
	// Usage switches working_on on (with Proposers, which says who may
	// call it); a server given none does not offer it.
	Usage Declarer
	// Events records refusals; nil records them to the handler's log.
	Events *audit.Recorder
	// Refusals counts the writes refused for holding a credential
	// (*metrics.Metrics); nil counts none.
	Refusals Refusals
	// ToolUsage counts every tool call in aggregate (*usagemetrics.Recorder);
	// nil counts none.
	ToolUsage ToolUsage
	// Changes switches the change tools (change_open, change_checks,
	// change_merge) on; a server given none does not offer them.
	Changes Changes
	// Lander makes ticket_finish put a finished ticket of a tenant on the merge
	// queue in the server's landing queue; nil leaves every tenant on the worker flow.
	Lander Lander
	// QueueMode hides the lock tools from a tenant that lands by the merge
	// queue; nil offers them to every tenant.
	QueueMode QueueMode
	// BoardURL is where the board is served (PUBLIC_BASE_URL), for the
	// ticket links agents put in their PR descriptions. Empty gives no link.
	BoardURL string
	// Attachments switches the attachment tools on and has ticket_get list a
	// ticket's attachments; a server given none does neither.
	Attachments Attachments
	// Reviews switches the slop-review tools (review_request, review_findings,
	// review_label) on; a server given none does not offer them.
	Reviews Reviews
	// Boundary makes the fence of external content; nil is random, which
	// is the only choice outside tests.
	Boundary ticket.ExternalBoundary
	// Discussions lets ticket_get name the steward's label on a discussion
	// ticket; nil names none.
	Discussions ticket.AttemptDiscussions
	// DiscussionLabelToWorkers shows that label to a worker as well. Off by
	// default: a worker that sees "routine" may phrase its question to be
	// classified so, which biases the record the label exists to build.
	DiscussionLabelToWorkers bool
}

func NewHandler(deps Deps, logHandler slog.Handler) *Handler {
	h := &Handler{
		tickets:   deps.Tickets,
		messages:  deps.Messages,
		sessions:  deps.Sessions,
		agents:    deps.Agents,
		roles:     deps.Roles,
		workspace: deps.Workspace,
		manifests: deps.Manifests,
		proposers: deps.Proposers,
		stewards:  deps.Stewards,
		assessor:  deps.Assessor,
		drafter:   deps.Drafter,
		labeller:  deps.Labeller,
		drafts:    deps.Drafts,
		proposals: deps.Proposals,
		batches:   deps.Batches,
		usage:     deps.Usage,
		events:    deps.Events,
		refusals:  deps.Refusals,
		toolUsage: deps.ToolUsage,
		changes:   deps.Changes,
		lander:    deps.Lander,
		queueMode: deps.QueueMode,
		boardURL:  strings.TrimRight(deps.BoardURL, "/"),
		logger:    slog.New(logHandler),

		boundary:       deps.Boundary,
		discussions:    deps.Discussions,
		attachments:    deps.Attachments,
		reviews:        deps.Reviews,
		labelToWorkers: deps.DiscussionLabelToWorkers,
	}
	if h.events == nil {
		h.events = audit.New(logHandler, nil)
	}
	return h
}

// authenticatedAgent returns the agent the request was authenticated as.
func authenticatedAgent(ctx context.Context) (authn.Identity, error) {
	found, err := authn.FromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("mcpserver: unauthenticated tool call: %w", err)
	}
	return found, nil
}

// ticketURL is the ticket's page on the board, in the caller's tenant. The
// tenant is part of the link because a ticket's number is only unique within
// one: without it the board would open whichever #N the viewer's own tenant
// has.
func (h *Handler) ticketURL(ctx context.Context, id int64) string {
	if h.boardURL == "" {
		return ""
	}
	found, err := authn.FromContext(ctx)
	if err != nil {
		return ""
	}
	query := url.Values{"tenant": {found.TenantID().String()}}
	return fmt.Sprintf("%s/tickets/%d?%s", h.boardURL, id, query.Encode())
}

// progress marks that the agent changed something, resetting the stop-hook
// loop breaker. Called only by tools that actually mutate state -- a denied
// scope extension deliberately does not count, or an agent stuck in an
// extend-deny cycle would evade the breaker forever.
func (h *Handler) progress(ctx context.Context, agentID uuid.UUID) {
	if err := h.sessions.RecordProgress(ctx, agentID); err != nil {
		h.logger.Warn("record progress", "agent_id", agentID, "err", err)
	}
}

// text builds the terse, human-readable result an agent reads. Tools also
// return structured output; this is what the model actually sees first, so
// it stays short.
func text(format string, args ...any) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf(format, args...)}},
	}
}

// -- tool inputs and outputs --
//
// These are the schemas every agent carries in its context on every turn,
// so field sets stay minimal and descriptions carry their weight.

type ticketIDInput struct {
	TicketID int64 `json:"ticket_id"`
}

type emptyInput struct{}

type okOutput struct {
	toolNotes
	OK bool `json:"ok"`
	// Message is the text's answer, with what to do next. It is the only
	// reason given for an ok of false.
	Message string `json:"message,omitempty"`
}

// okSays answers with msg in the text and in the output.
func okSays(ok bool, format string, args ...any) (*mcp.CallToolResult, okOutput, error) {
	msg := fmt.Sprintf(format, args...)
	return text("%s", msg), okOutput{OK: ok, Message: msg}, nil
}
