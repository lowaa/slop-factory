package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/manifest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/pendingaction"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

var errNotConsole = errors.New("only a console agent -- the person's own session, marked as one on the Agents page -- or a conductor may use this tool")

// addConsoleTool registers a tool only a console or a conductor may call.
// That is read from the database on every call, here at the handler: hiding
// the tool from tools/list would not stop a worker that calls it by name.
// Every console tool goes through this, so a new one cannot forget the check.
func addConsoleTool[In, Out any](server *mcp.Server, h *Handler, tool *mcp.Tool, run toolFunc[In, Out]) {
	addTool(server, h, tool, leaseFree, func(ctx context.Context, agentID uuid.UUID, in In) (*mcp.CallToolResult, Out, error) {
		var zero Out
		mayPropose, err := h.proposers.MayPropose(ctx, agentID)
		if err != nil {
			return nil, zero, err
		}
		if !mayPropose {
			attrs := []any{"agent_id", agentID, "tool", tool.Name}
			if found, err := authenticatedAgent(ctx); err == nil {
				attrs = append(attrs, "tenant", found.TenantID())
			}
			h.events.Record(ctx, audit.ConsoleToolRefused, attrs...)
			return nil, zero, errNotConsole
		}
		return run(ctx, agentID, in)
	})
}

// attemptArgDoc is said of every tool that takes the optional attempt.
const attemptArgDoc = "attempt: only a console or a conductor passes it, to name which of a ticket's attempts it means. " +
	"With one live attempt it can be left out; with several and none named the call is refused with the list. " +
	"A worker never passes it -- its lease already names its attempt -- and is refused if it does."

const cannotApprove = "You cannot approve it: only the person can, with their own session."

// autoApproveDoc is said of every propose tool: the person may have asked for
// their own sessions' proposals of a kind to be applied at once.
const autoApproveDoc = "Exception: if the person has turned on auto-approve for this kind of proposal, it is applied as you propose it, and the result says so."

func registerConsoleTools(server *mcp.Server, h *Handler) {
	addConsoleTool(server, h, &mcp.Tool{
		Name: "propose_discussion_answer",
		Description: "Propose the person's answer to a ticket waiting in discussion. Nothing changes until they approve it " +
			"on the board; then it posts as their comment and re-queues the ticket. " + cannotApprove + " " + autoApproveDoc + " " + attemptArgDoc,
	}, h.proposeDiscussionAnswer)

	addConsoleTool(server, h, &mcp.Tool{
		Name: "propose_ready",
		Description: "Propose moving a ticket out of preparing, into the queue. Nothing changes until the person approves it " +
			"on the board. " + cannotApprove + " " + autoApproveDoc,
	}, h.proposeReady)

	addConsoleTool(server, h, &mcp.Tool{
		Name: "propose_status",
		Description: "Propose parking or cancelling a ticket, with the reason. Nothing changes until the person approves it " +
			"on the board. " + cannotApprove + " " + autoApproveDoc,
	}, h.proposeStatus)

	addConsoleTool(server, h, &mcp.Tool{
		Name: "propose_review_decision",
		Description: "Propose approving a ticket's change in review, or sending it back (request_changes, with what to address). " +
			"Nothing changes until the person approves it on the board. " + cannotApprove + " " + autoApproveDoc + " " + attemptArgDoc,
	}, h.proposeReviewDecision)

	addConsoleTool(server, h, &mcp.Tool{
		Name: "propose_ticket_edit",
		Description: "Propose changing a ticket you do not hold -- its title, body (the whole new text), paths (the whole new set of " +
			"filed paths, never empty) or priority -- as the person's own edit. base_revision is the ticket's revision you read " +
			"it at (ticket_get): the proposal is refused if the ticket has been edited since. It is checked again when the person " +
			"approves, along with the paths and priority you replace, so a ticket changed meanwhile is refused rather than overwritten: " +
			"re-read and re-propose. It cannot change status (use propose_ready or propose_status), except that open=true also moves a " +
			"ticket in preparing to open in the same approval: use that instead of a separate propose_ready when a preparing ticket needs both, " +
			"because a ready proposed beside a pending edit on the same ticket cannot be approved until the edit is decided. It cannot touch " +
			"a done or cancelled ticket, or edit one an agent holds: that agent owns it then, so comment on it instead. Paths that " +
			"overlap another ticket are shown on the approval card; where the tenant locks path scopes, they are refused only where the board refuses them too, when the " +
			"ticket or the one it overlaps holds its scope. Nothing changes until the person approves it on the board. " + cannotApprove + " " + autoApproveDoc,
	}, h.proposeTicketEdit)

	addConsoleTool(server, h, &mcp.Tool{
		Name: "propose_manifest_edit",
		Description: "Propose a new workspace manifest, for an owner of the tenant to approve. Pass the WHOLE new manifest (every repo, as " +
			"workspace_get returns them, with your changes made), not a patch, and base_revision: the revision workspace_get gave you. " +
			"It is refused if the manifest has changed since that revision, and checked again when the owner approves: a manifest saved in " +
			"between makes the proposal stale and nothing is written, so read it again and re-propose. deploy_only rules loosen the merge gate and " +
			"independent_apps (an owner-set list of directories whose apps build and deploy independently) and its two switches, batch_within_apps and skip_retest_across_apps, " +
			"shared_paths loosen scope exclusivity, protected_paths (an owner-set list the steward may not decide on; pass them back unchanged) limit it, which is why only an owner can approve this, and never automatically: auto-approve " +
			"does not apply to it. Nothing changes until an owner approves it on the board. " + cannotApprove,
	}, h.proposeManifestEdit)

	addConsoleTool(server, h, &mcp.Tool{
		Name: "propose_close_merged",
		Description: "Propose closing a ticket as done because its change was merged outside slop-haus (by someone slop-haus does not act on). " +
			"Give the reason. Only an owner of the tenant can approve it, never automatically: auto-approve does not apply to it. " +
			"When the owner approves, the code host is asked again and the close is refused unless the change is merged into the ticket's base branch; " +
			"failed checks or an unapproved head are shown to the owner and accepted by approving. Not for a ticket an agent holds: it is refused. " +
			"Nothing changes until an owner approves it on the board. " + cannotApprove,
	}, h.proposeCloseMerged)

	addConsoleTool(server, h, &mcp.Tool{
		Name:        "list_proposals",
		Description: "List your own proposals and how each ended, newest first, including why one was rejected.",
	}, h.listProposals)

	addConsoleTool(server, h, &mcp.Tool{
		Name:        "withdraw_proposal",
		Description: "Take back one of your own proposals that is still pending.",
	}, h.withdrawProposal)
}

type proposeAnswerInput struct {
	TicketID int64  `json:"ticket_id"`
	Attempt  *int   `json:"attempt,omitempty" jsonschema:"the attempt the answer is for; see the tool description"`
	Body     string `json:"body" jsonschema:"the answer, exactly as it should be posted"`
}

type proposeStatusInput struct {
	TicketID int64  `json:"ticket_id"`
	Status   string `json:"status" jsonschema:"parked or cancelled"`
	Reason   string `json:"reason"`
}

type proposeCloseMergedInput struct {
	TicketID int64  `json:"ticket_id"`
	Reason   string `json:"reason" jsonschema:"why the ticket is done: who merged the change and where"`
}

type proposeReviewInput struct {
	TicketID int64  `json:"ticket_id"`
	Attempt  *int   `json:"attempt,omitempty" jsonschema:"the attempt the decision is for; see the tool description"`
	Decision string `json:"decision" jsonschema:"approve or request_changes"`
	Body     string `json:"body,omitempty" jsonschema:"what to address; required for request_changes"`
}

type proposeEditInput struct {
	TicketID     int64    `json:"ticket_id"`
	BaseRevision int      `json:"base_revision" jsonschema:"the ticket's revision when you read it"`
	Title        *string  `json:"title,omitempty"`
	Body         *string  `json:"body,omitempty" jsonschema:"the whole new body, not a patch"`
	Paths        []string `json:"paths,omitempty" jsonschema:"the whole new set of filed paths, not a patch; never empty"`
	Priority     *string  `json:"priority,omitempty" jsonschema:"low, normal, high or urgent"`
	Open         bool     `json:"open,omitempty" jsonschema:"also move the ticket out of preparing when the edit is approved; only for a ticket in preparing"`
}

type proposeManifestEditInput struct {
	BaseRevision int64                  `json:"base_revision" jsonschema:"the manifest's revision when you read it with workspace_get; 0 if it was not declared"`
	Repos        []proposedManifestRepo `json:"repos" jsonschema:"the whole new manifest: every repo, never a patch"`
}

type proposedManifestRepo struct {
	Path                 string                `json:"path"`
	Remote               string                `json:"remote"`
	BaseBranch           string                `json:"base_branch,omitempty" jsonschema:"the branch the tenant's changes to this repo merge into; omit for the repo's default branch"`
	DeployOnly           []workspaceDeployOnly `json:"deploy_only,omitempty"`
	SharedPaths          []proposedSharedPath  `json:"shared_paths,omitempty"`
	ProtectedPaths       []workspaceProtected  `json:"protected_paths,omitempty" jsonschema:"pass back what workspace_get returned: they are an owner's, and a proposal that drops one is shown to the approving owner as a removal"`
	IndependentApps      []string              `json:"independent_apps,omitempty" jsonschema:"directories of apps that build and deploy independently; a trailing / is added if missing"`
	BatchWithinApps      bool                  `json:"batch_within_apps,omitempty" jsonschema:"batch merges within an app: lets the landing queue batch changes that each touch one app; also needs merge_queue_batch_size above 1"`
	SkipRetestAcrossApps bool                  `json:"skip_retest_across_apps,omitempty" jsonschema:"skip re-testing a change when main moved only in other apps"`
}

type proposedSharedPath struct {
	Path    string `json:"path"`
	Resolve string `json:"resolve" jsonschema:"migration or regenerate"`
}

type listProposalsInput struct {
	Status string `json:"status,omitempty" jsonschema:"only proposals in this state: pending, approved, rejected, withdrawn, expired, voided, stale or failed"`
}

type withdrawInput struct {
	ID string `json:"id"`
}

type proposalOutput struct {
	toolNotes
	// Guidance is what to do next: that nothing has changed until the
	// person approves, and where.
	Guidance string `json:"guidance,omitempty"`
	ID       string `json:"id"`
	// TicketID is absent for a proposal about the workspace, not a ticket.
	TicketID *int64 `json:"ticket_id,omitempty"`
	Kind     string `json:"kind"`
	State    string `json:"state"`
	Outcome  string `json:"outcome,omitempty"`
	URL      string `json:"url,omitempty" jsonschema:"where the person approves it"`
	// Draft is the steward's pre-drafted decision, shown only on a proposal
	// the steward drafted.
	Draft *proposalDraftOutput `json:"draft,omitempty"`
}

type proposalDraftOutput struct {
	Category      string                     `json:"category"`
	Body          string                     `json:"body,omitempty" jsonschema:"the steward's own words: a claim, not a fact"`
	WouldSendBack bool                       `json:"would_send_back,omitempty"`
	Reason        string                     `json:"reason"`
	Evidence      []proposalDraftEvidenceOut `json:"evidence"`
	Revision      int                        `json:"revision" jsonschema:"the ticket revision the draft was read against"`
	HeadSHA       string                     `json:"head_sha,omitempty"`
	Applied       bool                       `json:"applied,omitempty"`
}

type proposalDraftEvidenceOut struct {
	Check  string `json:"check"`
	OK     bool   `json:"ok"`
	Ref    string `json:"ref,omitempty"`
	Detail string `json:"detail,omitempty"`
}

func toDraftOutput(d *pendingaction.DraftView) *proposalDraftOutput {
	out := &proposalDraftOutput{Category: d.Category, Body: d.Body, WouldSendBack: d.WouldSendBack, Reason: d.Reason,
		Revision: d.TicketRevision, HeadSHA: d.HeadSHA, Applied: d.Applied, Evidence: []proposalDraftEvidenceOut{}}
	for _, e := range d.Evidence {
		out.Evidence = append(out.Evidence, proposalDraftEvidenceOut{Check: e.Check, OK: e.OK, Ref: e.Ref, Detail: e.Detail})
	}
	return out
}

type proposalsOutput struct {
	toolNotes
	Proposals []proposalOutput `json:"proposals"`
}

// approvalsURL is the person's approvals view on the board, in the caller's
// tenant.
func (h *Handler) approvalsURL(ctx context.Context) string {
	if h.boardURL == "" {
		return ""
	}
	found, err := authn.FromContext(ctx)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%s/approvals?%s", h.boardURL, url.Values{"tenant": {found.TenantID().String()}}.Encode())
}

// about names what a proposal is on, for the guidance given back.
func (p *proposalOutput) about() string {
	if p.TicketID == nil {
		return "the workspace"
	}
	return fmt.Sprintf("#%d", *p.TicketID)
}

func (h *Handler) toProposal(ctx context.Context, a *pendingaction.Action) proposalOutput {
	return proposalOutput{
		ID: a.ID.String(), TicketID: a.TicketID, Kind: string(a.Kind), State: string(a.State), Outcome: a.Outcome,
		URL: h.approvalsURL(ctx),
	}
}

func (h *Handler) propose(ctx context.Context, agentID uuid.UUID, params *pendingaction.ProposeParams) (*mcp.CallToolResult, proposalOutput, error) {
	params.AgentID = agentID
	created, err := h.proposals.Propose(ctx, params)
	if err != nil {
		return nil, proposalOutput{}, err
	}
	h.progress(ctx, agentID)
	out := h.toProposal(ctx, created)
	if created.AutoApproved() {
		out.Guidance = fmt.Sprintf("Proposed (%s) on %s as %s and applied at once: the person has turned on auto-approve for this kind.",
			out.Kind, out.about(), out.ID)
		return text("%s", out.Guidance), out, nil
	}
	if created.State != pendingaction.StatePending {
		// Auto-approval was tried and the usual checks refused it (the ticket
		// moved on, say): the outcome says why, and nothing has changed.
		out.Guidance = fmt.Sprintf("Proposed (%s) on %s as %s, but it ended %s: %s. Nothing has changed.",
			out.Kind, out.about(), out.ID, out.State, out.Outcome)
		return text("%s", out.Guidance), out, nil
	}
	msg := fmt.Sprintf("Proposed (%s) on %s as %s. Nothing has changed yet. %s", out.Kind, out.about(), out.ID, cannotApprove)
	if out.URL != "" {
		msg += " Tell them to approve it at " + out.URL
	}
	out.Guidance = msg
	return text("%s", msg), out, nil
}

func (h *Handler) proposeDiscussionAnswer(ctx context.Context, agentID uuid.UUID, in proposeAnswerInput) (*mcp.CallToolResult, proposalOutput, error) {
	return h.propose(ctx, agentID, &pendingaction.ProposeParams{
		TicketID: in.TicketID, Kind: pendingaction.KindDiscussionAnswer, Payload: pendingaction.Payload{Body: in.Body},
		Attempt: in.Attempt,
	})
}

func (h *Handler) proposeReady(ctx context.Context, agentID uuid.UUID, in ticketIDInput) (*mcp.CallToolResult, proposalOutput, error) {
	return h.propose(ctx, agentID, &pendingaction.ProposeParams{TicketID: in.TicketID, Kind: pendingaction.KindReady})
}

func (h *Handler) proposeStatus(ctx context.Context, agentID uuid.UUID, in proposeStatusInput) (*mcp.CallToolResult, proposalOutput, error) {
	return h.propose(ctx, agentID, &pendingaction.ProposeParams{
		TicketID: in.TicketID, Kind: pendingaction.KindStatus,
		Payload: pendingaction.Payload{Status: ticket.Status(in.Status), Body: in.Reason},
	})
}

func (h *Handler) proposeReviewDecision(ctx context.Context, agentID uuid.UUID, in proposeReviewInput) (*mcp.CallToolResult, proposalOutput, error) {
	return h.propose(ctx, agentID, &pendingaction.ProposeParams{
		TicketID: in.TicketID, Kind: pendingaction.KindReviewDecision,
		Payload: pendingaction.Payload{Decision: pendingaction.Decision(in.Decision), Body: in.Body},
		Attempt: in.Attempt,
	})
}

func (h *Handler) proposeTicketEdit(ctx context.Context, agentID uuid.UUID, in proposeEditInput) (*mcp.CallToolResult, proposalOutput, error) {
	edit := &pendingaction.EditPayload{BaseRevision: in.BaseRevision, Title: in.Title, Body: in.Body, Paths: in.Paths, Open: in.Open}
	if in.Priority != nil {
		priority := model.Priority(*in.Priority)
		edit.Priority = &priority
	}
	return h.propose(ctx, agentID, &pendingaction.ProposeParams{
		TicketID: in.TicketID, Kind: pendingaction.KindEdit, Payload: pendingaction.Payload{Edit: edit},
	})
}

func (h *Handler) proposeCloseMerged(ctx context.Context, agentID uuid.UUID, in proposeCloseMergedInput) (*mcp.CallToolResult, proposalOutput, error) {
	// Closing a ticket as done is not something the steward's judgement may
	// put in front of the owner: it is a console's or a conductor's to ask.
	if h.stewards != nil {
		isSteward, err := h.stewards.IsSteward(ctx, agentID)
		if err != nil {
			return nil, proposalOutput{}, err
		}
		if isSteward {
			h.events.Record(ctx, audit.ConsoleToolRefused, "agent_id", agentID, "tool", "propose_close_merged")
			return nil, proposalOutput{}, errNotConsole
		}
	}
	return h.propose(ctx, agentID, &pendingaction.ProposeParams{
		TicketID: in.TicketID, Kind: pendingaction.KindCloseMerged, Payload: pendingaction.Payload{Body: in.Reason},
	})
}

func (h *Handler) proposeManifestEdit(ctx context.Context, agentID uuid.UUID, in proposeManifestEditInput) (*mcp.CallToolResult, proposalOutput, error) {
	repos := make([]manifest.Repo, len(in.Repos))
	for i, r := range in.Repos {
		repos[i] = manifest.Repo{Path: r.Path, Remote: r.Remote, BaseBranch: r.BaseBranch, IndependentApps: r.IndependentApps, BatchWithinApps: r.BatchWithinApps, SkipRetestAcrossApps: r.SkipRetestAcrossApps}
		for _, d := range r.DeployOnly {
			repos[i].DeployOnly = append(repos[i].DeployOnly, manifest.DeployOnlyRule{Path: d.Path, Lines: d.Lines})
		}
		for _, p := range r.ProtectedPaths {
			repos[i].ProtectedPaths = append(repos[i].ProtectedPaths, manifest.ProtectedPathRule{Path: p.Path, Reason: p.Reason})
		}
		for _, sp := range r.SharedPaths {
			repos[i].SharedPaths = append(repos[i].SharedPaths, manifest.SharedPathRule{Path: sp.Path, Resolve: sp.Resolve})
		}
	}
	return h.propose(ctx, agentID, &pendingaction.ProposeParams{
		Kind:    pendingaction.KindManifestEdit,
		Payload: pendingaction.Payload{ManifestEdit: &pendingaction.ManifestEditPayload{BaseRevision: in.BaseRevision, Manifest: repos}},
	})
}

func (h *Handler) listProposals(ctx context.Context, agentID uuid.UUID, in listProposalsInput) (*mcp.CallToolResult, proposalsOutput, error) {
	filter := &pendingaction.ListFilter{AgentID: &agentID}
	if in.Status != "" {
		filter.States = []pendingaction.State{pendingaction.State(in.Status)}
	}
	found, err := h.proposals.List(ctx, filter)
	if err != nil {
		return nil, proposalsOutput{}, err
	}
	out := proposalsOutput{Proposals: make([]proposalOutput, len(found))}
	drafts := h.draftsOf(ctx, found)
	var lines []string
	for i := range found {
		out.Proposals[i] = h.toProposal(ctx, &found[i])
		if d := drafts[found[i].ID]; d != nil {
			out.Proposals[i].Draft = toDraftOutput(d)
		}
		p := out.Proposals[i]
		subject := "workspace"
		if p.TicketID != nil {
			subject = fmt.Sprintf("#%d", *p.TicketID)
		}
		line := fmt.Sprintf("%s  %s  %s  %s", p.ID, subject, p.Kind, p.State)
		// An outcome such as "rejected: Not yet." already names the state.
		if p.Outcome != "" {
			if rest, ok := strings.CutPrefix(p.Outcome, p.State+": "); ok {
				line += ": " + rest
			} else if p.Outcome != p.State {
				line += ": " + p.Outcome
			}
		}
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		return text("No proposals."), out, nil
	}
	return text("%s", strings.Join(lines, "\n")), out, nil
}

// draftsOf reads the steward's drafts of the proposals; a failure shows none,
// as a draft is advice.
func (h *Handler) draftsOf(ctx context.Context, found []pendingaction.Action) map[uuid.UUID]*pendingaction.DraftView {
	if h.drafts == nil || len(found) == 0 {
		return nil
	}
	ids := make([]uuid.UUID, len(found))
	for i := range found {
		ids[i] = found[i].ID
	}
	drafts, err := h.drafts.Drafts(ctx, ids)
	if err != nil {
		h.logger.WarnContext(ctx, "read steward drafts", "error", err)
		return nil
	}
	return drafts
}

func (h *Handler) withdrawProposal(ctx context.Context, agentID uuid.UUID, in withdrawInput) (*mcp.CallToolResult, proposalOutput, error) {
	id, err := uuid.Parse(in.ID)
	if err != nil {
		return nil, proposalOutput{}, fmt.Errorf("id must be a proposal id: %w", err)
	}
	withdrawn, err := h.proposals.Withdraw(ctx, agentID, id)
	if err != nil {
		return nil, proposalOutput{}, err
	}
	h.progress(ctx, agentID)
	return text("Withdrew %s.", id), h.toProposal(ctx, withdrawn), nil
}
