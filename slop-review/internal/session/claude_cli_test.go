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

	"github.com/lowaa/slop-factory/slop-review/internal/claudecli"
	"github.com/lowaa/slop-factory/slop-review/internal/review"
)

type fakeClaudeExecutor struct {
	req *claudecli.ExecRequest
	res *claudecli.ExecResult
	err error
}

func (e *fakeClaudeExecutor) Exec(_ context.Context, req *claudecli.ExecRequest) (*claudecli.ExecResult, error) {
	e.req = req
	return e.res, e.err
}

func claudeResult(models ...string) *claudecli.ExecResult {
	return &claudecli.ExecResult{JSON: []byte(validReviewJSON), Models: models}
}

func newClaudeSession(exec ClaudeExecutor, spec *Spec) Session {
	return (&ClaudeCLIFactory{Exec: exec, Model: "claude-want", ReasoningEffort: "high"}).New(spec)
}

func TestClaudeCLISession_BuildsTheRequest(t *testing.T) {
	exec := &fakeClaudeExecutor{res: claudeResult()}

	_, err := newClaudeSession(exec, codexSpec(&stubDispatcher{})).Run(context.Background())
	require.NoError(t, err)

	assert.Equal(t, "/repo", exec.req.RepoRoot)
	assert.Equal(t, "claude-want", exec.req.Model)
	assert.Equal(t, "high", exec.req.ReasoningEffort)

	system := strings.Index(exec.req.Prompt, "SYSTEM")
	notes := strings.Index(exec.req.Prompt, "There is no record_finding")
	user := strings.Index(exec.req.Prompt, "USER")
	assert.True(t, system >= 0 && system < notes && notes < user, "prompt order was %q", exec.req.Prompt)
	assert.Contains(t, exec.req.Prompt, `cd /repo &&`, "the notes must say where the repository is")

	want, err := json.Marshal(review.SubmitReviewTool.Parameters)
	require.NoError(t, err)
	assert.JSONEq(t, string(want), string(exec.req.Schema))
}

func TestClaudeCLISession_ReturnsTheReview(t *testing.T) {
	exec := &fakeClaudeExecutor{res: &claudecli.ExecResult{JSON: []byte(`{"verdict":"request_changes","summary":"bad","findings":[{"file":"a.go","line":0,"severity":"major","summary":"s","detail":"d"}]}`)}}

	r, err := newClaudeSession(exec, codexSpec(&stubDispatcher{})).Run(context.Background())
	require.NoError(t, err)

	assert.Equal(t, "request_changes", string(r.Verdict))
	require.Len(t, r.Findings, 1)
	assert.Equal(t, "a.go", r.Findings[0].File)
	assert.False(t, r.Incomplete)
}

func TestClaudeCLISession_IncompleteWhenFilesUnseen(t *testing.T) {
	exec := &fakeClaudeExecutor{res: claudeResult()}

	r, err := newClaudeSession(exec, codexSpec(&stubDispatcher{unseen: []string{"big.go"}})).Run(context.Background())
	require.NoError(t, err)

	assert.True(t, r.Incomplete)
}

func TestClaudeCLISession_AppendsDispatcherWarnings(t *testing.T) {
	exec := &fakeClaudeExecutor{res: claudeResult()}

	r, err := newClaudeSession(exec, codexSpec(&stubDispatcher{warnings: []string{"w1"}})).Run(context.Background())
	require.NoError(t, err)

	assert.Equal(t, []string{"w1"}, r.Warnings)
}

func TestClaudeCLISession_WarnsWhenModelDiffers(t *testing.T) {
	tests := []struct {
		name      string
		want      string
		reported  []string
		wantWarns bool
	}{
		{"differs", "claude-want", []string{"claude-other"}, true},
		{"differs among several", "claude-want", []string{"claude-other", "claude-third"}, true},
		{"same", "claude-want", []string{"claude-want"}, false},
		{"one of several matches", "claude-want", []string{"claude-other", "claude-want"}, false},
		{"alias matches resolved model", "opus", []string{"claude-opus-5-5"}, false},
		{"not reported", "claude-want", nil, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			exec := &fakeClaudeExecutor{res: claudeResult(tc.reported...)}
			s := (&ClaudeCLIFactory{Exec: exec, Model: tc.want}).New(codexSpec(&stubDispatcher{}))

			r, err := s.Run(context.Background())
			require.NoError(t, err)

			if tc.wantWarns {
				require.Len(t, r.Warnings, 1)
				assert.Contains(t, r.Warnings[0], tc.want)
				assert.Contains(t, r.Warnings[0], tc.reported[0])
			} else {
				assert.Empty(t, r.Warnings)
			}
		})
	}
}

func TestClaudeCLISession_Errors(t *testing.T) {
	execErr := errors.New("claude fell over")
	tests := []struct {
		name    string
		exec    *fakeClaudeExecutor
		wantErr string
	}{
		{"invalid json", &fakeClaudeExecutor{res: &claudecli.ExecResult{JSON: []byte(`not json`)}}, "decode review"},
		{"fails validation", &fakeClaudeExecutor{res: &claudecli.ExecResult{JSON: []byte(`{"verdict":"maybe","summary":"x","findings":[]}`)}}, "invalid review"},
		{"executor error", &fakeClaudeExecutor{err: execErr}, "claude fell over"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := newClaudeSession(tc.exec, codexSpec(&stubDispatcher{})).Run(context.Background())
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}

	_, err := newClaudeSession(&fakeClaudeExecutor{err: execErr}, codexSpec(&stubDispatcher{})).Run(context.Background())
	assert.ErrorIs(t, err, execErr)
}

func TestClaudeCLISession_Stats(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1000, 0), step: 7 * time.Second}
	exec := &fakeClaudeExecutor{res: claudeResult()}
	s := (&ClaudeCLIFactory{Exec: exec, Now: clock.Now}).New(codexSpec(&stubDispatcher{unseen: []string{"a.go", "b.go"}}))

	assert.Zero(t, s.Stats().Duration, "no duration before Run")

	_, err := s.Run(context.Background())
	require.NoError(t, err)

	stats := s.Stats()
	assert.Equal(t, 7*time.Second, stats.Duration)
	assert.Equal(t, []string{"a.go", "b.go"}, stats.UnseenFiles)
}

func TestClaudeCLIFactory_DefaultsNowToTimeNow(t *testing.T) {
	exec := &fakeClaudeExecutor{res: claudeResult()}
	s := (&ClaudeCLIFactory{Exec: exec}).New(codexSpec(&stubDispatcher{}))

	_, err := s.Run(context.Background())
	require.NoError(t, err)

	assert.GreaterOrEqual(t, s.Stats().Duration, time.Duration(0))
}

func TestClaudeCLISession_ToolNotesFollowRunChecks(t *testing.T) {
	for _, runChecks := range []bool{false, true} {
		exec := &fakeClaudeExecutor{res: &claudecli.ExecResult{JSON: []byte(validReviewJSON)}}
		_, err := (&ClaudeCLIFactory{Exec: exec, RunChecks: runChecks}).New(codexSpec(&stubDispatcher{})).Run(context.Background())
		require.NoError(t, err)

		assert.Equal(t, !runChecks, strings.Contains(exec.req.Prompt, "You cannot modify anything"), "RunChecks=%v", runChecks)
		assert.Equal(t, runChecks, strings.Contains(exec.req.Prompt, "A failing test is not a finding until you have run it on the base commit"), "RunChecks=%v", runChecks)
		assert.Contains(t, exec.req.Prompt, "There is no record_finding")
	}
}

func TestClaudeCLISession_StatsReportTokens(t *testing.T) {
	res := claudeResult()
	res.Tokens = 27394
	s := newClaudeSession(&fakeClaudeExecutor{res: res}, codexSpec(&stubDispatcher{}))
	_, err := s.Run(context.Background())
	require.NoError(t, err)

	assert.Equal(t, 27394, s.Stats().Tokens)
}
