package session

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-review/internal/claudecli"
	"github.com/lowaa/slop-factory/slop-review/internal/codexcli"
	"github.com/lowaa/slop-factory/slop-review/internal/llm"
)

// A usage limit is what a configured fallback exists for, so a CLI run
// that hit one must say so in the terms the orchestrator classifies by.
func TestCLISessions_AUsageLimitIsReportedAsOne(t *testing.T) {
	other := errors.New("failed: exit status 1")
	for name, tc := range map[string]struct {
		run   func(err error) error
		limit error
	}{
		"codex": {
			run: func(err error) error {
				_, e := newCodexSession(&fakeCodexExecutor{err: err}, codexSpec(&stubDispatcher{})).Run(context.Background())
				return e
			},
			limit: errors.New("codex exec failed: You've hit your usage limit"),
		},
		"claude": {
			run: func(err error) error {
				_, e := (&ClaudeCLIFactory{Exec: &fakeClaudeExecutor{err: err}}).New(codexSpec(&stubDispatcher{})).Run(context.Background())
				return e
			},
			// The runner tells a limit apart (see claudecli's tests).
			limit: fmt.Errorf("%w: You've hit your weekly limit", claudecli.ErrUsageLimit),
		},
	} {
		t.Run(name, func(t *testing.T) {
			assert.ErrorIs(t, tc.run(tc.limit), llm.ErrUsageLimit)
			assert.NotErrorIs(t, tc.run(other), llm.ErrUsageLimit)
			assert.ErrorIs(t, tc.run(other), other)
		})
	}
}

// The CLIs print one token total; it reaches the report as the session's
// usage rather than being dropped.
func TestCLISessions_TokensBecomeTheSessionsUsage(t *testing.T) {
	codex := newCodexSession(&fakeCodexExecutor{res: &codexcli.ExecResult{JSON: []byte(validReviewJSON), TokensUsed: 1234}}, codexSpec(&stubDispatcher{}))
	_, err := codex.Run(context.Background())
	require.NoError(t, err)
	assert.Equal(t, llm.Usage{Requests: 1, InputTokens: 1234}, codex.Stats().Usage)

	claude := (&ClaudeCLIFactory{Exec: &fakeClaudeExecutor{res: &claudecli.ExecResult{JSON: []byte(validReviewJSON), Tokens: 99}}}).New(codexSpec(&stubDispatcher{}))
	_, err = claude.Run(context.Background())
	require.NoError(t, err)
	assert.Equal(t, llm.Usage{Requests: 1, InputTokens: 99}, claude.Stats().Usage)

	none := newCodexSession(&fakeCodexExecutor{res: &codexcli.ExecResult{JSON: []byte(validReviewJSON)}}, codexSpec(&stubDispatcher{}))
	_, err = none.Run(context.Background())
	require.NoError(t, err)
	assert.Equal(t, llm.Usage{}, none.Stats().Usage, "no tokens reported, no usage claimed")
}
