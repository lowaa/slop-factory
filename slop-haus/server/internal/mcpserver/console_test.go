package mcpserver_test

import (
	"context"
	"encoding/json"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/agent"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db/dbtest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/manifest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/pendingaction"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

// consoleSession is a second MCP session, signed in as an agent the person
// has marked a console (or not).
type consoleSession struct {
	agentID uuid.UUID
	session *mcp.ClientSession
}

func (r *realRig) agentSession(name string, console bool) *consoleSession {
	r.t.Helper()
	kind := agent.KindWorker
	if console {
		kind = agent.KindConsole
	}
	created, err := r.agents.CreateKind(r.ctx, name, kind)
	require.NoError(r.t, err)
	return r.connectAs(name, created.ID)
}

// connectAs opens an MCP session signed in as an existing agent.
func (r *realRig) connectAs(name string, agentID uuid.UUID) *consoleSession {
	r.t.Helper()
	token := "token-" + name
	r.tokens.add(token, agentID)
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0.0.1"}, nil)
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint: r.url, HTTPClient: &http.Client{Transport: bearer{token: token}},
	}, nil)
	require.NoError(r.t, err)
	r.t.Cleanup(func() { _ = session.Close() })
	return &consoleSession{agentID: agentID, session: session}
}

func (c *consoleSession) call(t *testing.T, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	res, err := c.session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	require.NoError(t, err)
	return res
}

func (r *realRig) forceStatus(status ticket.Status) *ticket.Ticket {
	r.t.Helper()
	created := r.openTicket("A ticket")
	_, err := r.database.Exec(r.ctx, `UPDATE tickets SET status = $2 WHERE id = $1`, created.ID, string(status))
	require.NoError(r.t, err)
	return created
}

func (r *realRig) proposalCount() int {
	r.t.Helper()
	var n int
	require.NoError(r.t, r.database.QueryRow(r.ctx, `SELECT count(*) FROM pending_actions`).Scan(&n))
	return n
}

// consoleToolCalls is one valid call of every tool only a console may use.
// Every tool the server lists with a console-tool name must be here, so a
// new one cannot be added without its refusal being tested.
func consoleToolCalls(ticketID int64) map[string]map[string]any {
	return map[string]map[string]any{
		"propose_discussion_answer": {"ticket_id": ticketID, "body": "go ahead"},
		"propose_ready":             {"ticket_id": ticketID},
		"propose_status":            {"ticket_id": ticketID, "status": "parked", "reason": "later"},
		"propose_review_decision":   {"ticket_id": ticketID, "decision": "approve"},
		"propose_ticket_edit":       {"ticket_id": ticketID, "base_revision": 0, "body": "new"},
		"propose_manifest_edit":     {"base_revision": 0, "repos": []map[string]any{{"path": "app", "remote": "github.com/acme/app"}}},
		"propose_close_merged":      {"ticket_id": ticketID, "reason": "merged by hand"},
		"list_proposals":            {},
		"withdraw_proposal":         {"id": uuid.NewString()},
		"working_on":                {"ticket_id": ticketID},
	}
}

func isConsoleToolName(name string) bool {
	return strings.HasPrefix(name, "propose_") || name == "list_proposals" || name == "withdraw_proposal" || name == "working_on"
}

// A worker holding a console's tools by name gets refused at the handler,
// and nothing is created: hiding the tool from tools/list is not the control.
func TestConsoleTools_RefuseAnAgentThatIsNotAConsole(t *testing.T) {
	r := newRealRig(t)
	tk := r.forceStatus(ticket.StatusPreparing)
	worker := r.agentSession("worker", false)
	calls := consoleToolCalls(tk.ID)

	listed, err := worker.session.ListTools(context.Background(), nil)
	require.NoError(t, err)
	var names []string
	for _, tool := range listed.Tools {
		if isConsoleToolName(tool.Name) {
			names = append(names, tool.Name)
		}
	}
	sort.Strings(names)
	require.NotEmpty(t, names)
	for _, name := range names {
		require.Contains(t, calls, name, "a console tool with no refusal test")
	}
	assert.Len(t, names, len(calls), "every tool in the table is registered")

	for _, name := range names {
		res := worker.call(t, name, calls[name])
		assert.True(t, res.IsError, name)
		assert.Contains(t, resultText(t, res), "only a console agent", name)
	}
	assert.Zero(t, r.proposalCount())
	assert.Equal(t, len(names), r.events.Count(audit.ConsoleToolRefused), "each refusal is on record")
	got, err := r.tickets.Get(r.ctx, tk.ID)
	require.NoError(t, err)
	assert.Equal(t, ticket.StatusPreparing, got.Status)
}

func TestConsoleTools_ADisabledConsoleIsRefused(t *testing.T) {
	r := newRealRig(t)
	tk := r.forceStatus(ticket.StatusPreparing)
	c := r.agentSession("console", true)
	_, err := r.database.Exec(r.ctx, `UPDATE agents SET disabled_at = now() WHERE id = $1`, c.agentID)
	require.NoError(t, err)

	res := c.call(t, "list_proposals", map[string]any{})
	assert.True(t, res.IsError)
	assert.Contains(t, resultText(t, res), "only a console agent")
	res = c.call(t, "propose_ready", map[string]any{"ticket_id": tk.ID})
	assert.True(t, res.IsError)
	assert.Zero(t, r.proposalCount())
}

func TestConsoleTools_NoToolApproves(t *testing.T) {
	r := newRealRig(t)
	c := r.agentSession("console", true)
	listed, err := c.session.ListTools(context.Background(), nil)
	require.NoError(t, err)
	for _, tool := range listed.Tools {
		assert.NotContains(t, tool.Name, "approve", "a tool that approves would let an agent take the human's action")
		assert.NotContains(t, tool.Name, "reject")
	}
}

func TestConsoleTools_ProposeCreatesAPendingProposalAndChangesNothing(t *testing.T) {
	r := newRealRig(t)
	c := r.agentSession("console", true)

	newTitle, highPriority, normalPriority := "new title", model.PriorityHigh, model.PriorityNormal
	cases := []struct {
		tool   string
		status ticket.Status
		args   map[string]any
		kind   pendingaction.Kind
		want   pendingaction.Payload
	}{
		{"propose_discussion_answer", ticket.StatusDiscussion, map[string]any{"body": "go with B"},
			pendingaction.KindDiscussionAnswer, pendingaction.Payload{Body: "go with B"}},
		{"propose_ready", ticket.StatusPreparing, map[string]any{},
			pendingaction.KindReady, pendingaction.Payload{}},
		{"propose_status", ticket.StatusOpen, map[string]any{"status": "cancelled", "reason": "obsolete"},
			pendingaction.KindStatus, pendingaction.Payload{Status: ticket.StatusCancelled, Body: "obsolete"}},
		{"propose_review_decision", ticket.StatusReview, map[string]any{"decision": "request_changes", "body": "add tests"},
			pendingaction.KindReviewDecision, pendingaction.Payload{Decision: pendingaction.DecisionRequestChanges, Body: "add tests"}},
		{"propose_ticket_edit", ticket.StatusPreparing, map[string]any{"base_revision": 0, "title": "new title", "paths": []string{"./a/b/"}, "priority": "high"},
			pendingaction.KindEdit, pendingaction.Payload{Edit: &pendingaction.EditPayload{
				Title: &newTitle, Paths: []string{"a/b/"}, Priority: &highPriority, BasePriority: &normalPriority}}},
		{"propose_ticket_edit", ticket.StatusPreparing, map[string]any{"base_revision": 0, "title": "new title", "open": true},
			pendingaction.KindEdit, pendingaction.Payload{Edit: &pendingaction.EditPayload{Title: &newTitle, Open: true}}},
	}
	for _, tc := range cases {
		t.Run(tc.tool, func(t *testing.T) {
			tk := r.forceStatus(tc.status)
			args := map[string]any{"ticket_id": tk.ID}
			for k, v := range tc.args {
				args[k] = v
			}
			res := c.call(t, tc.tool, args)
			require.False(t, res.IsError, resultText(t, res))
			var out struct {
				ID       string `json:"id"`
				TicketID int64  `json:"ticket_id"`
				Kind     string `json:"kind"`
				State    string `json:"state"`
				URL      string `json:"url"`
			}
			structured, err := json.Marshal(res.StructuredContent)
			require.NoError(t, err)
			require.NoError(t, json.Unmarshal(structured, &out))
			assert.Equal(t, tk.ID, out.TicketID)
			assert.Equal(t, string(tc.kind), out.Kind)
			assert.Equal(t, "pending", out.State)
			assert.Equal(t, testBoardURL+"/approvals?tenant="+r.tenantID.String(), out.URL)
			body := resultText(t, res)
			assert.Contains(t, body, "cannot approve")
			assert.Contains(t, body, testBoardURL+"/approvals?tenant="+r.tenantID.String())

			stored, err := r.pending.List(r.ctx, &pendingaction.ListFilter{TicketID: &tk.ID})
			require.NoError(t, err)
			require.Len(t, stored, 1)
			assert.Equal(t, tc.kind, stored[0].Kind)
			assert.Equal(t, tc.want, stored[0].Payload)
			assert.Equal(t, c.agentID, stored[0].AgentID, "recorded as the authenticated agent")
			assert.Equal(t, pendingaction.StatePending, stored[0].State)
			got, err := r.tickets.Get(r.ctx, tk.ID)
			require.NoError(t, err)
			assert.Equal(t, tc.status, got.Status, "nothing on the ticket changed")
		})
	}
}

func TestConsoleTools_ListShowsOnlyTheCallersOwnAndWithdrawIsTheirsToo(t *testing.T) {
	r := newRealRig(t)
	mine, other := r.agentSession("mine", true), r.agentSession("other", true)
	tk, theirs := r.forceStatus(ticket.StatusPreparing), r.forceStatus(ticket.StatusPreparing)
	require.False(t, mine.call(t, "propose_ready", map[string]any{"ticket_id": tk.ID}).IsError)
	require.False(t, other.call(t, "propose_ready", map[string]any{"ticket_id": theirs.ID}).IsError)

	listed := mine.call(t, "list_proposals", map[string]any{})
	require.False(t, listed.IsError, resultText(t, listed))
	assert.Contains(t, resultText(t, listed), "#"+strconv.FormatInt(tk.ID, 10))
	assert.NotContains(t, resultText(t, listed), "#"+strconv.FormatInt(theirs.ID, 10))

	otherOwn, err := r.pending.List(r.ctx, &pendingaction.ListFilter{AgentID: &other.agentID})
	require.NoError(t, err)
	require.Len(t, otherOwn, 1)
	stolen := mine.call(t, "withdraw_proposal", map[string]any{"id": otherOwn[0].ID.String()})
	assert.True(t, stolen.IsError, "another console's proposal is not mine to withdraw")

	ownList, err := r.pending.List(r.ctx, &pendingaction.ListFilter{AgentID: &mine.agentID})
	require.NoError(t, err)
	withdrawn := mine.call(t, "withdraw_proposal", map[string]any{"id": ownList[0].ID.String()})
	require.False(t, withdrawn.IsError, resultText(t, withdrawn))
	again := mine.call(t, "list_proposals", map[string]any{"status": "pending"})
	assert.Contains(t, resultText(t, again), "No proposals")
	all := mine.call(t, "list_proposals", map[string]any{"status": "withdrawn"})
	assert.Contains(t, resultText(t, all), "withdrawn")
	assert.Contains(t, resultText(t, all), "#"+strconv.FormatInt(tk.ID, 10))
}

func TestConsoleTools_ListFiltersByState(t *testing.T) {
	r := newRealRig(t)
	c := r.agentSession("console", true)
	pendingTicket, withdrawnTicket := r.forceStatus(ticket.StatusPreparing), r.forceStatus(ticket.StatusPreparing)
	require.False(t, c.call(t, "propose_ready", map[string]any{"ticket_id": pendingTicket.ID}).IsError)
	require.False(t, c.call(t, "propose_ready", map[string]any{"ticket_id": withdrawnTicket.ID}).IsError)
	toWithdraw, err := r.pending.List(r.ctx, &pendingaction.ListFilter{TicketID: &withdrawnTicket.ID})
	require.NoError(t, err)
	require.False(t, c.call(t, "withdraw_proposal", map[string]any{"id": toWithdraw[0].ID.String()}).IsError)

	pending := resultText(t, c.call(t, "list_proposals", map[string]any{"status": "pending"}))
	assert.Contains(t, pending, "#"+strconv.FormatInt(pendingTicket.ID, 10))
	assert.NotContains(t, pending, "#"+strconv.FormatInt(withdrawnTicket.ID, 10))
	everything := resultText(t, c.call(t, "list_proposals", map[string]any{}))
	assert.Contains(t, everything, "#"+strconv.FormatInt(pendingTicket.ID, 10))
	assert.Contains(t, everything, "#"+strconv.FormatInt(withdrawnTicket.ID, 10))
}

func TestConsoleTools_ProposeManifestEditStoresTheWholeManifestAndChangesNothing(t *testing.T) {
	database := dbtest.WithDB(t)
	r := newManifestRigIn(t, database)
	r.declare([]manifest.Repo{{Path: "app", Remote: "github.com/acme/app"}})
	c := r.agentSession("console", true)
	read, _ := c.workspace(t)
	require.EqualValues(t, 1, read.Revision)

	args := map[string]any{"base_revision": read.Revision, "repos": []map[string]any{{
		"path": "app", "remote": "git@github.com:acme/app.git", "base_branch": "slop-haus-acme",
		"deploy_only":      []map[string]any{{"path": "deploy/*.yaml", "lines": `\s*newTag: \S+`}},
		"shared_paths":     []map[string]any{{"path": "gen/", "resolve": "regenerate"}},
		"protected_paths":  []map[string]any{{"path": "infrastructure/", "reason": "prod"}},
		"independent_apps": []string{"gen/"}, "batch_within_apps": true, "skip_retest_across_apps": true,
	}}}
	res := c.call(t, "propose_manifest_edit", args)
	require.False(t, res.IsError, resultText(t, res))
	assert.Contains(t, resultText(t, res), "cannot approve")
	assert.Contains(t, resultText(t, res), "the workspace")

	stored, err := r.pending.List(r.ctx, &pendingaction.ListFilter{AgentID: &c.agentID})
	require.NoError(t, err)
	require.Len(t, stored, 1)
	assert.Equal(t, pendingaction.KindManifestEdit, stored[0].Kind)
	assert.Equal(t, pendingaction.StatePending, stored[0].State)
	assert.Nil(t, stored[0].TicketID)
	want := &pendingaction.ManifestEditPayload{PayloadVersion: 1, BaseRevision: 1, Manifest: []manifest.Repo{{
		Path: "app", Remote: "github.com/acme/app", BaseBranch: "slop-haus-acme",
		DeployOnly:      []manifest.DeployOnlyRule{{Path: "deploy/*.yaml", Lines: `\s*newTag: \S+`}},
		SharedPaths:     []manifest.SharedPathRule{{Path: "gen/", Resolve: "regenerate"}},
		ProtectedPaths:  []manifest.ProtectedPathRule{{Path: "infrastructure/", Reason: "prod"}},
		IndependentApps: []string{"gen/"}, BatchWithinApps: true, SkipRetestAcrossApps: true,
	}}}
	assert.Equal(t, want, stored[0].Payload.ManifestEdit)
	after, _ := c.workspace(t)
	assert.Empty(t, after.Repos[0].DeployOnly, "the live manifest is untouched")

	stale := c.call(t, "propose_manifest_edit", map[string]any{"base_revision": 0, "repos": args["repos"]})
	assert.True(t, stale.IsError)
	assert.Contains(t, resultText(t, stale), "revision 1")
	invalid := c.call(t, "propose_manifest_edit", map[string]any{"base_revision": 1, "repos": []map[string]any{{"path": "../x", "remote": "github.com/a/b"}}})
	assert.True(t, invalid.IsError)
	badBranch := c.call(t, "propose_manifest_edit", map[string]any{"base_revision": 1, "repos": []map[string]any{{"path": "app", "remote": "github.com/a/b", "base_branch": "ticket-1-a1"}}})
	assert.True(t, badBranch.IsError, "a base branch is validated like a board save")
	assert.Contains(t, resultText(t, badBranch), "collides")
}

func TestConsoleTools_WorkspaceGetGivesTheRevisionToProposeAgainst(t *testing.T) {
	r := newManifestRigIn(t, dbtest.WithDB(t))
	c := r.agentSession("console", true)
	none, text := c.workspace(t)
	assert.False(t, none.Declared)
	assert.Zero(t, none.Revision)
	assert.Contains(t, text, "Revision 0")
	r.declare([]manifest.Repo{{Path: "app", Remote: "github.com/acme/app"}})
	r.declare([]manifest.Repo{{Path: "app", Remote: "github.com/acme/app"}})
	got, _ := c.workspace(t)
	assert.EqualValues(t, 2, got.Revision)
}
