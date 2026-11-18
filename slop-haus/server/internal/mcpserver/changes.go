package mcpserver

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/change"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// Changes is the slice of change.Service the change tools use. The tenant is
// the authenticated agent's, taken from its identity here and from nowhere
// in the request.
type Changes interface {
	Open(ctx context.Context, tenant, agentID uuid.UUID, in *change.OpenInput) (*change.Opened, error)
	Checks(ctx context.Context, tenant, agentID uuid.UUID, expectedSHA string) (*change.Report, error)
	Merge(ctx context.Context, tenant, agentID uuid.UUID, in *change.MergeInput) (*change.Merged, error)
	Review(ctx context.Context, tenant, agentID uuid.UUID, params *ticket.ReviewParams) (*ticket.Ticket, error)
	FinishWithoutChange(ctx context.Context, tenant, agentID uuid.UUID, reason string) (*ticket.Ticket, error)
	NarrowScopeToChange(ctx context.Context, tenant, agentID uuid.UUID) (*ticket.NarrowResult, error)
}

// Lander hands a finished ticket to the server's landing queue. The bool is
// false, with nothing changed, when the tenant's tickets are landed by their
// agents.
type Lander interface {
	Land(ctx context.Context, tenant, agentID uuid.UUID, handoff string, flakeTicket int64) (*ticket.Ticket, bool, error)
}

// narrowToChange narrows the ticket the agent is about to hand on to the
// files its change touches. Best effort: a scope that stays broad costs
// other work a wait, a failed hand-off costs the agent its run, so a failure
// is logged and the hand-off goes on.
func (h *Handler) narrowToChange(ctx context.Context, agentID uuid.UUID) {
	if h.changes == nil {
		return
	}
	found, err := authenticatedAgent(ctx)
	if err != nil {
		return
	}
	if _, err := h.changes.NarrowScopeToChange(ctx, found.TenantID(), agentID); err != nil {
		h.logger.WarnContext(ctx, "could not narrow the ticket's scope to its change", "agent", agentID, "err", err)
	}
}

func registerChangeTools(server *mcp.Server, h *Handler) {
	addTool(server, h, &mcp.Tool{
		Name: "change_open",
		Description: "Open a pull request for the ticket you hold, from a branch you have pushed, in the ticket's repo. " +
			"slop-haus opens it, onto its base branch, and records it on the ticket. Pass draft to open one not ready for " +
			"review; call it again with the same branch and draft false to take a draft out of draft. " +
			"A new branch replaces the ticket's earlier change, which slop-haus will no longer merge. " +
			"On an attempt that is not the ticket's promoted one, the change is always a draft, onto the ticket's own base branch " +
			"(slop-haus creates it); draft false is refused, and so is its merge: finish with ticket_finish and a handoff, " +
			"and the attempt waits for a human's choice.",
	}, leaseHeld, h.changeOpen)

	addTool(server, h, &mcp.Tool{
		Name: "change_checks",
		Description: "Read the checks on your ticket's change, as of now, with the head commit they are for. " +
			"Passed is true only when something ran on that head and nothing failed or is pending; no checks yet is not a pass. " +
			"Pass the commit you pushed as head_sha to learn whether the checks are for it.",
	}, leaseHeld, h.changeChecks)

	addTool(server, h, &mcp.Tool{
		Name: "change_merge",
		Description: "Merge your ticket's change with a merge commit; slop-haus does it, not you. Refused unless you hold " +
			"the integration lock, the ticket's approval was given where it requires one (and covers the change's current head: a push " +
			"after ticket_finish needs another review), the change still targets the base branch, its head contains the base's " +
			"latest commit (merge the base branch into your branch and push when it does not; a head behind only by commits to the " +
			"files the workspace manifest names as deploy-only, which the change does not touch, merges as it is), and the checks passed on the change's " +
			"current head (read at the moment of the merge, which is then pinned: a push in between is refused). " +
			"Pass head_sha, the commit you pushed, to refuse the merge if the change's head is anything else. " +
			"On success slop-haus also closes the ticket as done with the change's link and releases the integration lock: " +
			"there is nothing more to call for it.",
	}, leaseHeld, h.changeMerge)
}

type changeOpenInput struct {
	Branch string `json:"branch" jsonschema:"the pushed branch to open the change from"`
	Title  string `json:"title" jsonschema:"one line; use the ticket's title"`
	Body   string `json:"body,omitempty" jsonschema:"the description; slop-haus adds a footer naming the ticket and the people involved"`
	Draft  bool   `json:"draft,omitempty" jsonschema:"open it as a draft"`
}

type changeOpenOutput struct {
	toolNotes
	URL      string `json:"url"`
	Number   int64  `json:"number"`
	Draft    bool   `json:"draft"`
	Existing bool   `json:"existing,omitempty"`
}

func (h *Handler) changeOpen(ctx context.Context, agentID uuid.UUID, in changeOpenInput) (*mcp.CallToolResult, changeOpenOutput, error) {
	found, err := authenticatedAgent(ctx)
	if err != nil {
		return nil, changeOpenOutput{}, err
	}
	opened, err := h.changes.Open(ctx, found.TenantID(), agentID, &change.OpenInput{
		Branch: in.Branch, Title: in.Title, Body: in.Body, Draft: in.Draft,
	})
	if err != nil {
		return nil, changeOpenOutput{}, err
	}
	h.progress(ctx, agentID)
	out := changeOpenOutput{URL: opened.URL, Number: opened.Number, Draft: opened.Draft, Existing: opened.Existing}
	state := "ready for review"
	if opened.Draft {
		state = "a draft"
	}
	if opened.Existing {
		return text("The change from %s is already open, %s: %s", opened.Branch, state, opened.URL), out, nil
	}
	return text("Opened %s (%s) and recorded it on your ticket.", opened.URL, state), out, nil
}

type changeChecksInput struct {
	HeadSHA string `json:"head_sha,omitempty" jsonschema:"the commit you pushed, to learn whether the checks are for it"`
}

type checkRef struct {
	Name  string `json:"name"`
	State string `json:"state"`
}

type changeChecksOutput struct {
	toolNotes
	// Note is what the text adds to the state: that no checks is not a pass,
	// and that they are not for the commit named.
	Note        string     `json:"note,omitempty"`
	URL         string     `json:"url"`
	HeadSHA     string     `json:"head_sha" jsonschema:"the commit the checks are for"`
	State       string     `json:"state" jsonschema:"success, failure, pending or none"`
	Passed      bool       `json:"passed"`
	Checks      []checkRef `json:"checks,omitempty"`
	ForYourHead *bool      `json:"for_your_head,omitempty" jsonschema:"whether the checks are for the head_sha you named"`
}

func (h *Handler) changeChecks(ctx context.Context, agentID uuid.UUID, in changeChecksInput) (*mcp.CallToolResult, changeChecksOutput, error) {
	found, err := authenticatedAgent(ctx)
	if err != nil {
		return nil, changeChecksOutput{}, err
	}
	report, err := h.changes.Checks(ctx, found.TenantID(), agentID, in.HeadSHA)
	if err != nil {
		return nil, changeChecksOutput{}, err
	}
	// Not progress: a read changes nothing, and counting it would let an agent
	// polling pending checks keep the stop-hook loop breaker from ever firing.
	out := changeChecksOutput{URL: report.URL, HeadSHA: report.HeadSHA, State: report.State, Passed: report.Passed, ForYourHead: report.ForExpectedHead}
	var b strings.Builder
	fmt.Fprintf(&b, "%s: checks on %s are %s", report.URL, report.HeadSHA, report.State)
	var notes []string
	if report.State == "none" {
		note := "nothing has reported on this commit: not a pass"
		notes = append(notes, note)
		b.WriteString(" (" + note + ")")
	}
	for _, c := range report.Checks {
		out.Checks = append(out.Checks, checkRef{Name: c.Name, State: c.State})
		fmt.Fprintf(&b, "\n- %s: %s", c.Name, c.State)
	}
	if report.ForExpectedHead != nil && !*report.ForExpectedHead {
		note := "These are NOT for the commit you named: the change's head is different."
		notes = append(notes, note)
		b.WriteString("\n" + note)
	}
	out.Note = strings.Join(notes, "\n")
	return text("%s", b.String()), out, nil
}

type changeMergeInput struct {
	HeadSHA string `json:"head_sha,omitempty" jsonschema:"the commit you pushed; the merge is refused if the change's head is anything else"`
}

type changeMergeOutput struct {
	toolNotes
	URL      string `json:"url"`
	MergeSHA string `json:"merge_sha"`
	// Guidance is what to do next.
	Guidance string `json:"guidance"`
}

func (h *Handler) changeMerge(ctx context.Context, agentID uuid.UUID, in changeMergeInput) (*mcp.CallToolResult, changeMergeOutput, error) {
	found, err := authenticatedAgent(ctx)
	if err != nil {
		return nil, changeMergeOutput{}, err
	}
	merged, err := h.changes.Merge(ctx, found.TenantID(), agentID, &change.MergeInput{ExpectedSHA: in.HeadSHA})
	if err != nil {
		return nil, changeMergeOutput{}, err
	}
	h.progress(ctx, agentID)
	guidance := fmt.Sprintf("Merged %s (merge commit %s); the ticket is done and the lock released. Nothing more to do for it.", merged.URL, merged.MergeSHA)
	if !merged.Closed {
		guidance = fmt.Sprintf("Merged %s (merge commit %s), but slop-haus could not close the ticket. Release the integration lock, then call ticket_finish with handoff and pr_url %s.", merged.URL, merged.MergeSHA, merged.URL)
	}
	return text("%s", guidance), changeMergeOutput{URL: merged.URL, MergeSHA: merged.MergeSHA, Guidance: guidance}, nil
}
