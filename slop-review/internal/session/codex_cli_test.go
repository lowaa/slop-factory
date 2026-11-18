package session

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-review/internal/codexcli"
)

type fakeCodexExecutor struct {
	req *codexcli.ExecRequest
	res *codexcli.ExecResult
	err error
}

func (e *fakeCodexExecutor) Exec(_ context.Context, req *codexcli.ExecRequest) (*codexcli.ExecResult, error) {
	e.req = req
	return e.res, e.err
}

const validReviewJSON = `{"verdict":"approve","summary":"fine","findings":[]}`

func codexSpec(tools ToolDispatcher) *Spec {
	return &Spec{RepoRoot: "/repo", SystemPrompt: "SYSTEM", UserPrompt: "USER", Tools: tools}
}

func newCodexSession(exec CodexExecutor, spec *Spec) Session {
	return (&CodexCLIFactory{Exec: exec, Model: "gpt-want", ReasoningEffort: "high"}).New(spec)
}

func TestCodexCLISession_BuildsTheRequest(t *testing.T) {
	exec := &fakeCodexExecutor{res: &codexcli.ExecResult{JSON: []byte(validReviewJSON)}}

	_, err := newCodexSession(exec, codexSpec(&stubDispatcher{})).Run(context.Background())
	require.NoError(t, err)

	assert.Equal(t, "/repo", exec.req.Cwd)
	assert.Equal(t, "gpt-want", exec.req.Model)
	assert.Equal(t, "high", exec.req.ReasoningEffort)

	system := strings.Index(exec.req.Prompt, "SYSTEM")
	notes := strings.Index(exec.req.Prompt, "There is no record_finding")
	user := strings.Index(exec.req.Prompt, "USER")
	assert.True(t, system >= 0 && system < notes && notes < user, "prompt order was %q", exec.req.Prompt)

	var schema map[string]any
	require.NoError(t, json.Unmarshal(exec.req.Schema, &schema))
	assert.Equal(t, false, schema["additionalProperties"])
}

func TestCodexCLISession_ReturnsTheReview(t *testing.T) {
	exec := &fakeCodexExecutor{res: &codexcli.ExecResult{JSON: []byte(`{"verdict":"request_changes","summary":"bad","findings":[{"file":"a.go","line":0,"severity":"major","summary":"s","detail":"d"}]}`)}}

	r, err := newCodexSession(exec, codexSpec(&stubDispatcher{})).Run(context.Background())
	require.NoError(t, err)

	assert.Equal(t, "request_changes", string(r.Verdict))
	require.Len(t, r.Findings, 1)
	assert.Equal(t, "a.go", r.Findings[0].File)
	assert.False(t, r.Incomplete)
}

func TestCodexCLISession_IncompleteWhenFilesUnseen(t *testing.T) {
	exec := &fakeCodexExecutor{res: &codexcli.ExecResult{JSON: []byte(validReviewJSON)}}

	r, err := newCodexSession(exec, codexSpec(&stubDispatcher{unseen: []string{"big.go"}})).Run(context.Background())
	require.NoError(t, err)

	assert.True(t, r.Incomplete)
}

func TestCodexCLISession_AppendsDispatcherWarnings(t *testing.T) {
	exec := &fakeCodexExecutor{res: &codexcli.ExecResult{JSON: []byte(validReviewJSON)}}

	r, err := newCodexSession(exec, codexSpec(&stubDispatcher{warnings: []string{"w1"}})).Run(context.Background())
	require.NoError(t, err)

	assert.Equal(t, []string{"w1"}, r.Warnings)
}

func TestCodexCLISession_WarnsWhenModelDiffers(t *testing.T) {
	tests := []struct {
		name      string
		reported  string
		wantWarns bool
	}{
		{"differs", "gpt-other", true},
		{"same", "gpt-want", false},
		{"not reported", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			exec := &fakeCodexExecutor{res: &codexcli.ExecResult{JSON: []byte(validReviewJSON), Model: tc.reported}}

			r, err := newCodexSession(exec, codexSpec(&stubDispatcher{})).Run(context.Background())
			require.NoError(t, err)

			if tc.wantWarns {
				require.Len(t, r.Warnings, 1)
				assert.Contains(t, r.Warnings[0], "gpt-other")
			} else {
				assert.Empty(t, r.Warnings)
			}
		})
	}
}

func TestCodexCLISession_ReportsAndChecksEffort(t *testing.T) {
	tests := []struct {
		name      string
		reported  string
		wantWarns bool
	}{
		{"differs", "medium", true},
		{"same", "high", false},
		{"not reported", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			exec := &fakeCodexExecutor{res: &codexcli.ExecResult{JSON: []byte(validReviewJSON), Effort: tc.reported}}

			sess := newCodexSession(exec, codexSpec(&stubDispatcher{}))
			r, err := sess.Run(context.Background())
			require.NoError(t, err)

			assert.Equal(t, tc.reported, sess.Stats().Effort)
			if tc.wantWarns {
				require.Len(t, r.Warnings, 1)
				assert.Contains(t, r.Warnings[0], "reasoning effort high")
				assert.Contains(t, r.Warnings[0], "medium")
			} else {
				assert.Empty(t, r.Warnings)
			}
		})
	}
}

func TestCodexCLISession_Errors(t *testing.T) {
	execErr := errors.New("codex fell over")
	tests := []struct {
		name    string
		exec    *fakeCodexExecutor
		wantErr string
	}{
		{"invalid json", &fakeCodexExecutor{res: &codexcli.ExecResult{JSON: []byte(`not json`)}}, "decode review"},
		{"fails validation", &fakeCodexExecutor{res: &codexcli.ExecResult{JSON: []byte(`{"verdict":"maybe","summary":"x","findings":[]}`)}}, "invalid review"},
		{"executor error", &fakeCodexExecutor{err: execErr}, "codex fell over"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := newCodexSession(tc.exec, codexSpec(&stubDispatcher{})).Run(context.Background())
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}

	_, err := newCodexSession(&fakeCodexExecutor{err: execErr}, codexSpec(&stubDispatcher{})).Run(context.Background())
	assert.ErrorIs(t, err, execErr)
}

// fakeClock advances by step on every call, so durations are exact.
type fakeClock struct {
	now  time.Time
	step time.Duration
}

func (c *fakeClock) Now() time.Time {
	t := c.now
	c.now = c.now.Add(c.step)
	return t
}

func TestCodexCLISession_Stats(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1000, 0), step: 7 * time.Second}
	exec := &fakeCodexExecutor{res: &codexcli.ExecResult{JSON: []byte(validReviewJSON)}}
	f := &CodexCLIFactory{Exec: exec, Now: clock.Now}
	s := f.New(codexSpec(&stubDispatcher{unseen: []string{"a.go", "b.go"}}))

	assert.Zero(t, s.Stats().Duration, "no duration before Run")

	_, err := s.Run(context.Background())
	require.NoError(t, err)

	stats := s.Stats()
	assert.Equal(t, 7*time.Second, stats.Duration)
	assert.Equal(t, []string{"a.go", "b.go"}, stats.UnseenFiles)
}

func TestCodexCLIFactory_DefaultsNowToTimeNow(t *testing.T) {
	exec := &fakeCodexExecutor{res: &codexcli.ExecResult{JSON: []byte(validReviewJSON)}}
	s := (&CodexCLIFactory{Exec: exec}).New(codexSpec(&stubDispatcher{}))

	_, err := s.Run(context.Background())
	require.NoError(t, err)

	assert.GreaterOrEqual(t, s.Stats().Duration, time.Duration(0))
}

func TestCodexCLISession_ToolNotesFollowRunChecks(t *testing.T) {
	for _, runChecks := range []bool{false, true} {
		exec := &fakeCodexExecutor{res: &codexcli.ExecResult{JSON: []byte(validReviewJSON)}}
		_, err := (&CodexCLIFactory{Exec: exec, RunChecks: runChecks}).New(codexSpec(&stubDispatcher{})).Run(context.Background())
		require.NoError(t, err)

		assert.Equal(t, !runChecks, strings.Contains(exec.req.Prompt, "You cannot modify anything"), "RunChecks=%v", runChecks)
		assert.Equal(t, runChecks, strings.Contains(exec.req.Prompt, "A failing test is not a finding until you have run it on the base commit"), "RunChecks=%v", runChecks)
		assert.Equal(t, runChecks, strings.Contains(exec.req.Prompt, "git -C <repo> archive"), "RunChecks=%v", runChecks)
		assert.Equal(t, runChecks, strings.Contains(exec.req.Prompt, "base=$(mktemp -d)"), "a fresh dir per check, RunChecks=%v", runChecks)
		assert.NotContains(t, exec.req.Prompt, "slop-review-base", "a fixed path collides across parallel sessions")
		assert.Contains(t, exec.req.Prompt, "There is no record_finding")
	}
}

func TestCodexCLISession_StatsReportTokensUsed(t *testing.T) {
	exec := &fakeCodexExecutor{res: &codexcli.ExecResult{JSON: []byte(validReviewJSON), TokensUsed: 11720}}
	s := (&CodexCLIFactory{Exec: exec}).New(codexSpec(&stubDispatcher{}))
	_, err := s.Run(context.Background())
	require.NoError(t, err)

	assert.Equal(t, 11720, s.Stats().Tokens)
}
