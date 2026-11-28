package mergeguard_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-bot/internal/api"
	"github.com/lowaa/slop-factory/slop-bot/internal/ident"
	"github.com/lowaa/slop-factory/slop-bot/internal/mergeguard"
	"github.com/lowaa/slop-factory/slop-bot/internal/tickets"
)

// fakeTickets returns the ticket it holds, or its error.
type fakeTickets struct {
	t   *tickets.Ticket
	err error
}

func (f *fakeTickets) Get(_ context.Context, id ident.TicketID) (*tickets.Ticket, error) {
	if f.err != nil {
		return nil, f.err
	}
	if f.t == nil || f.t.ID != id {
		return nil, tickets.ErrNotFound
	}
	return f.t, nil
}

// fakeGrants answers what it's given.
type fakeGrants struct {
	granted bool
	err     error
}

func (f *fakeGrants) Granted(context.Context, ident.TicketID) (bool, error) { return f.granted, f.err }

var agent = ident.AgentPrincipal("a1", "7")

func newGuard(t *tickets.Ticket, g *fakeGrants, terr error) *mergeguard.Guard {
	return mergeguard.New(&mergeguard.Options{
		Classifier: mergeguard.NewClassifier(&fakeGit{}),
		Tickets:    &fakeTickets{t: t, err: terr},
		Grants:     g,
		Logger:     slog.New(slog.DiscardHandler),
	})
}

var (
	merge = &mergeguard.Call{Tool: "Bash", Command: "gh pr merge 3", Cwd: "/wt"}
	build = &mergeguard.Call{Tool: "Bash", Command: "go build ./...", Cwd: "/wt"}
)

func TestGuardChecks(t *testing.T) {
	t.Parallel()
	open := &tickets.Ticket{ID: "7", Status: tickets.StatusInProgress}
	auto := &tickets.Ticket{ID: "7", Status: tickets.StatusInProgress, AutoMerge: true}
	done := &tickets.Ticket{ID: "7", Status: tickets.StatusDone}
	cases := []struct {
		name   string
		ticket *tickets.Ticket
		grants *fakeGrants
		terr   error
		by     ident.Principal
		call   *mergeguard.Call
		allow  bool
		reason string
	}{
		{name: "not a merge", ticket: open, grants: &fakeGrants{}, by: agent, call: build, allow: true},
		{name: "a merge, not authorised", ticket: open, grants: &fakeGrants{}, by: agent, call: merge,
			reason: "ticket 7 has no AutoMerge, and the user hasn't authorised a merge"},
		{name: "a merge, authorised", ticket: open, grants: &fakeGrants{granted: true}, by: agent, call: merge, allow: true},
		{name: "a merge on an AutoMerge ticket", ticket: auto, grants: &fakeGrants{}, by: agent, call: merge, allow: true},
		{name: "a merge on an ended ticket, even authorised", ticket: done, grants: &fakeGrants{granted: true}, by: agent, call: merge,
			reason: "ticket 7 is done"},
		{name: "the ticket can't be read", terr: errors.New("db gone"), grants: &fakeGrants{granted: true}, by: agent, call: merge,
			reason: "couldn't read ticket 7"},
		{name: "the ticket can't be read, not a merge", terr: errors.New("db gone"), grants: &fakeGrants{}, by: agent, call: build, allow: true},
		{name: "the grant can't be read", ticket: open, grants: &fakeGrants{granted: true, err: errors.New("locked")}, by: agent, call: merge,
			reason: "couldn't read whether the user authorised it"},
		{name: "the user's own session", ticket: open, grants: &fakeGrants{}, by: ident.UserPrincipal(), call: merge, allow: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			v := newGuard(tc.ticket, tc.grants, tc.terr).Check(context.Background(), tc.by, tc.call)
			assert.Equal(t, tc.allow, v.Allow)
			if tc.allow {
				return
			}
			require.NotNil(t, v.Finding)
			assert.Contains(t, v.Reason, tc.reason)
			assert.Contains(t, v.Reason, "slop-bot ready-to-merge", "a refusal says what to do instead")
			assert.Contains(t, v.Reason, "slop-bot tell a1 --merge")
		})
	}
}

// runHook runs the hook on a Claude Code PreToolUse payload and returns
// what it wrote.
func runHook(t *testing.T, ask func(context.Context, *api.PreTool) (*api.PreToolVerdict, error), in string) string {
	t.Helper()
	var out, errOut bytes.Buffer
	require.NoError(t, mergeguard.RunHook(context.Background(), &mergeguard.HookOptions{
		Ask: ask, Classifier: mergeguard.NewClassifier(&fakeGit{}),
		Stdin: strings.NewReader(in), Stdout: &out, Stderr: &errOut,
	}))
	return out.String()
}

func bashPayload(cmd string) string {
	b, _ := json.Marshal(map[string]any{
		"hook_event_name": "PreToolUse", "tool_name": "Bash", "cwd": "/wt",
		"tool_input": map[string]any{"command": cmd, "description": "d"},
	})
	return string(b)
}

func decision(t *testing.T, out string) (string, string) {
	t.Helper()
	var v struct {
		HookSpecificOutput struct {
			HookEventName            string `json:"hookEventName"`
			PermissionDecision       string `json:"permissionDecision"`
			PermissionDecisionReason string `json:"permissionDecisionReason"`
		} `json:"hookSpecificOutput"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &v), out)
	assert.Equal(t, "PreToolUse", v.HookSpecificOutput.HookEventName)
	return v.HookSpecificOutput.PermissionDecision, v.HookSpecificOutput.PermissionDecisionReason
}

func TestHookAsksTheDaemon(t *testing.T) {
	t.Parallel()
	var asked *api.PreTool
	verdict := &api.PreToolVerdict{Reason: "no merges here"}
	ask := func(_ context.Context, c *api.PreTool) (*api.PreToolVerdict, error) {
		asked = c
		return verdict, nil
	}
	d, reason := decision(t, runHook(t, ask, bashPayload("gh pr merge")))
	assert.Equal(t, "deny", d)
	assert.Equal(t, "no merges here", reason)
	assert.Equal(t, &api.PreTool{Tool: "Bash", Command: "gh pr merge", Cwd: "/wt"}, asked)

	verdict = &api.PreToolVerdict{Allow: true}
	assert.Empty(t, runHook(t, ask, bashPayload("gh pr merge")),
		"an allow writes nothing, so the harness's own permission rules still apply")
}

func TestHookFailsSafeWithoutTheDaemon(t *testing.T) {
	t.Parallel()
	down := func(context.Context, *api.PreTool) (*api.PreToolVerdict, error) {
		return nil, errors.New("slop-bot isn't running")
	}
	d, reason := decision(t, runHook(t, down, bashPayload("git push origin HEAD:main")))
	assert.Equal(t, "deny", d, "a merge is refused")
	assert.Contains(t, reason, "slop-bot isn't running")
	assert.Empty(t, runHook(t, down, bashPayload("go test ./...")), "anything else runs")
}

func TestHookReadsCodexArgvCommands(t *testing.T) {
	t.Parallel()
	var asked *api.PreTool
	ask := func(_ context.Context, c *api.PreTool) (*api.PreToolVerdict, error) {
		asked = c
		return &api.PreToolVerdict{Allow: true}, nil
	}
	runHook(t, ask, `{"tool_name":"shell","cwd":"/wt","tool_input":{"command":["bash","-lc","gh pr merge 'x y'"]}}`)
	require.NotNil(t, asked)
	assert.Equal(t, `bash -lc "gh pr merge 'x y'"`, asked.Command)
	assert.NotNil(t, mergeguard.NewClassifier(&fakeGit{}).Command(context.Background(), "/wt", asked.Command),
		"the quoted argv still classifies as the merge it is")
}

func TestHookAllowsWhatItCantRead(t *testing.T) {
	t.Parallel()
	ask := func(context.Context, *api.PreTool) (*api.PreToolVerdict, error) {
		require.FailNow(t, "nothing to ask about")
		return nil, nil
	}
	assert.Empty(t, runHook(t, ask, "not json"))
}
