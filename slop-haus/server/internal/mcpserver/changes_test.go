package mcpserver_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/change"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// fakeChanges answers what the test gives it and records who it was asked
// for: the tenant and agent a call acted for are the point of these tests.
type fakeChanges struct {
	mu sync.Mutex

	opened  *change.Opened
	report  *change.Report
	merged  *change.Merged
	failure error

	tenants  []uuid.UUID
	agents   []uuid.UUID
	opens    []change.OpenInput
	expects  []string
	merges   []change.MergeInput
	reviews  []ticket.ReviewParams
	review   *ticket.Ticket
	noChange []string
	// narrows are the agents asked to narrow; kept apart from note so a
	// hand-off's narrowing does not count as a change call.
	narrows   []uuid.UUID
	narrowErr error
}

func (f *fakeChanges) note(tenant, agent uuid.UUID) {
	f.tenants = append(f.tenants, tenant)
	f.agents = append(f.agents, agent)
}

func (f *fakeChanges) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.tenants)
}

func (f *fakeChanges) Open(_ context.Context, tenant, agent uuid.UUID, in *change.OpenInput) (*change.Opened, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.note(tenant, agent)
	f.opens = append(f.opens, *in)
	return f.opened, f.failure
}

func (f *fakeChanges) Checks(_ context.Context, tenant, agent uuid.UUID, expected string) (*change.Report, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.note(tenant, agent)
	f.expects = append(f.expects, expected)
	return f.report, f.failure
}

func (f *fakeChanges) Merge(_ context.Context, tenant, agent uuid.UUID, in *change.MergeInput) (*change.Merged, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.note(tenant, agent)
	f.merges = append(f.merges, *in)
	return f.merged, f.failure
}

func (f *fakeChanges) Review(_ context.Context, tenant, agent uuid.UUID, params *ticket.ReviewParams) (*ticket.Ticket, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.note(tenant, agent)
	f.reviews = append(f.reviews, *params)
	return f.review, f.failure
}

func (f *fakeChanges) FinishWithoutChange(_ context.Context, tenant, agent uuid.UUID, reason string) (*ticket.Ticket, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.note(tenant, agent)
	f.noChange = append(f.noChange, reason)
	return f.review, f.failure
}

func (f *fakeChanges) NarrowScopeToChange(_ context.Context, _, agent uuid.UUID) (*ticket.NarrowResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.narrows = append(f.narrows, agent)
	return nil, f.narrowErr
}

func newChangesRig(t *testing.T, changes *fakeChanges) *rig {
	t.Helper()
	return newRigWith(t, &rigOptions{changes: changes})
}

func TestChangeTools_AreOnlyOfferedWhenChangesAreWired(t *testing.T) {
	without, err := newRig(t).session.ListTools(context.Background(), nil)
	require.NoError(t, err)
	with, err := newChangesRig(t, &fakeChanges{}).session.ListTools(context.Background(), nil)
	require.NoError(t, err)

	names := func(tools []*mcp.Tool) []string {
		var out []string
		for _, tool := range tools {
			out = append(out, tool.Name)
		}
		return out
	}
	for _, name := range []string{"change_open", "change_checks", "change_merge"} {
		assert.NotContains(t, names(without.Tools), name)
		assert.Contains(t, names(with.Tools), name)
	}
}

func TestChangeOpen_ActsForTheAuthenticatedAgentAndItsTenant(t *testing.T) {
	changes := &fakeChanges{opened: &change.Opened{URL: "https://git.example/acme/app/pull/7", Number: 7, Draft: true}}
	r := newChangesRig(t, changes)

	res, err := r.session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "change_open", Arguments: map[string]any{"branch": "b", "title": "t", "body": "text", "draft": true},
	})

	require.NoError(t, err)
	require.False(t, res.IsError, resultText(t, res))
	assert.Contains(t, resultText(t, res), "https://git.example/acme/app/pull/7")
	assert.Equal(t, []uuid.UUID{testTenantID}, changes.tenants, "the tenant is the authenticated agent's")
	assert.Equal(t, []uuid.UUID{r.agentID}, changes.agents)
	assert.Equal(t, []change.OpenInput{{Branch: "b", Title: "t", Body: "text", Draft: true}}, changes.opens)
	assert.Equal(t, map[string]any{"url": "https://git.example/acme/app/pull/7", "number": float64(7), "draft": true}, res.StructuredContent)
	assert.Equal(t, 1, r.sessions.progress, "a change opened counts as progress")
}

func TestChangeOpen_NoArgumentNamesATenantOrARepo(t *testing.T) {
	tools, err := newChangesRig(t, &fakeChanges{}).session.ListTools(context.Background(), nil)
	require.NoError(t, err)

	for _, tool := range tools.Tools {
		if tool.Name != "change_open" && tool.Name != "change_checks" && tool.Name != "change_merge" {
			continue
		}
		props := tool.InputSchema.(map[string]any)["properties"].(map[string]any)
		for name := range props {
			assert.NotContains(t, []string{"tenant", "tenant_id", "repo", "repository", "base", "remote", "url", "pr_url", "host", "token"}, name,
				"%s takes %s, which the server decides", tool.Name, name)
		}
	}
}

func TestChangeChecks_ReportsTheHeadAndWhetherItIsTheNamedOne(t *testing.T) {
	no := false
	changes := &fakeChanges{report: &change.Report{
		URL: "https://git.example/acme/app/pull/7", HeadSHA: "abc", State: "none", ForExpectedHead: &no,
	}}
	r := newChangesRig(t, changes)

	res, err := r.session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "change_checks", Arguments: map[string]any{"head_sha": "def"},
	})

	require.NoError(t, err)
	text := resultText(t, res)
	assert.Contains(t, text, "checks on abc are none")
	assert.Contains(t, text, "not a pass")
	assert.Contains(t, text, "NOT for the commit you named")
	assert.Equal(t, []string{"def"}, changes.expects)
	assert.Equal(t, []uuid.UUID{testTenantID}, changes.tenants)
	assert.Equal(t, map[string]any{
		"url": "https://git.example/acme/app/pull/7", "head_sha": "abc", "state": "none", "passed": false, "for_your_head": false,
		"note": "nothing has reported on this commit: not a pass\nThese are NOT for the commit you named: the change's head is different.",
	}, res.StructuredContent)
	assert.Zero(t, r.sessions.progress, "a read is not progress")
}

func TestChangeMerge_PassesTheNamedHeadAndSaysWhatToDoNext(t *testing.T) {
	changes := &fakeChanges{merged: &change.Merged{URL: "https://git.example/acme/app/pull/7", MergeSHA: "mc", Closed: true}}
	r := newChangesRig(t, changes)

	res, err := r.session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "change_merge", Arguments: map[string]any{"head_sha": "abc"},
	})

	require.NoError(t, err)
	assert.Contains(t, resultText(t, res), "Nothing more to do")
	assert.Equal(t, []change.MergeInput{{ExpectedSHA: "abc"}}, changes.merges)
	assert.Equal(t, map[string]any{
		"url": "https://git.example/acme/app/pull/7", "merge_sha": "mc",
		"guidance": "Merged https://git.example/acme/app/pull/7 (merge commit mc); the ticket is done and the lock released. Nothing more to do for it.",
	}, res.StructuredContent)
	assert.Equal(t, 1, r.sessions.progress)
	assert.Equal(t, []uuid.UUID{testTenantID}, changes.tenants)
	assert.Equal(t, []uuid.UUID{r.agentID}, changes.agents)
}

func TestChangeTools_AReasonForARefusalReachesTheAgentAndIsNotProgress(t *testing.T) {
	changes := &fakeChanges{failure: errors.New("change: you do not hold the integration lock")}
	r := newChangesRig(t, changes)

	for _, name := range []string{"change_open", "change_checks", "change_merge"} {
		args := map[string]any{}
		if name == "change_open" {
			args = map[string]any{"branch": "b", "title": "t"}
		}
		res, err := r.session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})

		if err == nil {
			require.True(t, res.IsError, name)
			assert.Contains(t, resultText(t, res), "integration lock", name)
		} else {
			assert.Contains(t, err.Error(), "integration lock", name)
		}
	}
	assert.Zero(t, r.sessions.progress, "a refusal changed nothing")
}

// Every change tool needs the ticket the agent holds: a revoked lease fails
// the call with the notice, and nothing reaches the code host.
func TestChangeTools_ARevokedLeaseFailsTheCallBeforeAnythingIsDone(t *testing.T) {
	for tool, args := range map[string]map[string]any{
		"change_open":   {"branch": "b", "title": "t"},
		"change_checks": {},
		"change_merge":  {},
	} {
		t.Run(tool, func(t *testing.T) {
			changes := &fakeChanges{opened: &change.Opened{}, report: &change.Report{}, merged: &change.Merged{}}
			r := newChangesRig(t, changes)
			r.tickets.lease = &ticket.Lease{TicketID: 12}
			r.tickets.heartbeatErrs = []error{&ticket.LeaseRevokedError{TicketID: 12, Status: ticket.StatusCancelled}}

			res, err := r.session.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})

			require.NoError(t, err)
			assert.True(t, res.IsError)
			assert.Contains(t, resultText(t, res), "#12 was cancelled by a human")
			assert.Zero(t, changes.calls())
		})
	}
}

// Where the change tools are on, the hand-over goes through the change flow,
// which records the commit under review, and for the authenticated agent's
// tenant; the ticket service is not asked directly.
func TestTicketFinish_ReviewGoesThroughTheChangeFlowWhereChangesAreWired(t *testing.T) {
	changes := &fakeChanges{review: &ticket.Ticket{ID: 5}}
	r := newChangesRig(t, changes)
	r.tickets.lease = &ticket.Lease{TicketID: 5}

	res, err := r.session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "ticket_finish", Arguments: map[string]any{"pr_url": testPR, "handoff": "h", "request_review": true, "reason": "a person should read it"},
	})

	require.NoError(t, err)
	require.False(t, res.IsError, resultText(t, res))
	assert.Equal(t, []ticket.ReviewParams{{PRURL: testPR, Handoff: "h", Reason: "a person should read it"}}, changes.reviews)
	assert.Equal(t, []uuid.UUID{testTenantID}, changes.tenants)
	assert.Equal(t, []uuid.UUID{r.agentID}, changes.agents)
	assert.Nil(t, r.tickets.reviewed, "the ticket service is reached through the change flow")
	assert.Positive(t, r.sessions.progress)
}

func TestChangeMerge_AFailedCloseTellsTheAgentToFinishItself(t *testing.T) {
	changes := &fakeChanges{merged: &change.Merged{URL: "https://git.example/acme/app/pull/7", MergeSHA: "mc"}}
	r := newChangesRig(t, changes)

	res, err := r.session.CallTool(context.Background(), &mcp.CallToolParams{Name: "change_merge", Arguments: map[string]any{}})

	require.NoError(t, err)
	assert.Contains(t, resultText(t, res), "could not close the ticket")
	assert.Contains(t, resultText(t, res), "ticket_finish")
}
