package cli

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-bot/internal/daemon"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/fake"
)

// preTool runs `slop-bot hook pre-tool` as the agent's harness would before
// running cmd in its shell: a Claude Code PreToolUse payload on stdin, with
// the agent's environment. It returns the refusal's reason, or "" when the
// hook allowed the call.
func (e *e2e) preTool(s *fake.Session, cmd string) string {
	e.t.Helper()
	payload, err := json.Marshal(map[string]any{
		"hook_event_name": "PreToolUse", "tool_name": "Bash", "cwd": "/",
		"tool_input": map[string]any{"command": cmd},
	})
	require.NoError(e.t, err)
	r, err := s.Do(e.ctx(), fake.Command{Cmd: fake.CmdRun, Argv: []string{
		"/usr/bin/env", fakeReexec + "=", cliReexec + "=1",
		"/bin/sh", "-c", `printf '%s' "$1" | "$0" hook pre-tool`, e.self, string(payload),
	}})
	require.NoError(e.t, err)
	require.Equal(e.t, 0, r.Code, "the hook always exits 0: %s%s", r.Stdout, r.Stderr)
	if r.Stdout == "" {
		return ""
	}
	var out struct {
		HookSpecificOutput struct {
			PermissionDecision       string `json:"permissionDecision"`
			PermissionDecisionReason string `json:"permissionDecisionReason"`
		} `json:"hookSpecificOutput"`
	}
	require.NoError(e.t, json.Unmarshal([]byte(r.Stdout), &out), r.Stdout)
	require.Equal(e.t, "deny", out.HookSpecificOutput.PermissionDecision)
	return out.HookSpecificOutput.PermissionDecisionReason
}

func TestTheMergeGuardRefusesUntilTheUserAuthorises(t *testing.T) {
	t.Parallel()
	e := newE2E(t)
	s, tk := e.started()

	assert.Empty(t, e.preTool(s, "go test ./..."), "ordinary work runs")
	reason := e.preTool(s, "gh pr merge 12 --squash")
	assert.Contains(t, reason, "gh pr merge")
	assert.Contains(t, reason, "ticket "+tk.ID+" has no AutoMerge")
	refused := e.await(daemon.EventMergeRefused, nil)
	assert.Equal(t, tk.ID, refused.Ticket)
	agent := refused.Agent

	r := e.agentRun(s, "tell", agent, "--merge")
	assert.Equal(t, 1, r.Code, "an agent can't authorise its own merge")
	assert.Contains(t, r.Stderr, "only the user")
	assert.NotEmpty(t, e.preTool(s, "gh pr merge 12"))

	assert.Contains(t, e.h.ok(t, "tell", agent, "--merge"), "told 1")
	told, err := s.Expect(e.ctx(), fake.RepUser)
	require.NoError(t, err)
	assert.Contains(t, told.Text, "authorised to merge")
	assert.Empty(t, e.preTool(s, "gh pr merge 12 --squash"), "authorised, the merge runs")
	assert.Empty(t, e.preTool(s, "git push origin HEAD:main"))
}

func TestABareMergeTellAuthorises(t *testing.T) {
	t.Parallel()
	e := newE2E(t)
	s, tk := e.started()
	require.NotEmpty(t, e.preTool(s, "gh pr merge"))

	e.h.ok(t, "tell", tk.ID, "don't merge yet")
	_, err := s.Expect(e.ctx(), fake.RepUser)
	require.NoError(t, err)
	assert.NotEmpty(t, e.preTool(s, "gh pr merge"), "a message that mentions merging isn't the go-ahead")

	e.h.ok(t, "tell", tk.ID, "Merge.")
	_, err = s.Expect(e.ctx(), fake.RepUser)
	require.NoError(t, err)
	assert.Empty(t, e.preTool(s, "gh pr merge"))
}

func TestTheMergeGuardLetsAnAutoMergeTicketMerge(t *testing.T) {
	t.Parallel()
	e := newE2E(t)
	s, _ := e.started("--auto-merge")
	assert.Empty(t, e.preTool(s, "gh pr merge 3"))
}

func TestTheMergeGuardFailsSafeWithoutTheDaemon(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	run := func(cmd string) (int, string) {
		payload, err := json.Marshal(map[string]any{"tool_name": "Bash", "cwd": "/", "tool_input": map[string]any{"command": cmd}})
		require.NoError(t, err)
		h.out.Reset()
		h.deps.Stdin = bytes.NewReader(payload)
		h.deps.Token = "a-token-no-daemon-knows"
		return Execute(t.Context(), h.deps, []string{"hook", "pre-tool"}), h.out.String()
	}
	code, out := run("gh pr merge 1")
	assert.Equal(t, 0, code)
	assert.Contains(t, out, `"permissionDecision":"deny"`)
	assert.Contains(t, out, "couldn't be asked")
	code, out = run("ls")
	assert.Equal(t, 0, code)
	assert.Empty(t, out, "with no daemon, anything but a merge runs")
}

func TestTellMergeNeedsOneAgent(t *testing.T) {
	t.Parallel()
	e := newE2E(t)
	e.started()
	assert.Equal(t, 1, e.h.run(t, "tell", "--all", "--merge", "go"))
	assert.Contains(t, e.h.err.String(), "not --all")
	assert.Equal(t, 1, e.h.run(t, "tell", "nobody", "--merge"))
}
