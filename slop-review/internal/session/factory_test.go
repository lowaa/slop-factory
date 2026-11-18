package session

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-review/internal/llm"
)

func TestSlopReviewManagedFactory_BuildsASessionFromTheSpecAndItsOwnConfig(t *testing.T) {
	prose := llm.CompletionResponse{Message: llm.Message{Role: llm.RoleAssistant, Content: "prose"}}
	provider := &stubProvider{errAt: -1, responses: []llm.CompletionResponse{prose, prose, prose}}
	f := &SlopReviewManagedFactory{Provider: provider, Model: "the-model", MaxTurns: 2}

	s := f.New(&Spec{SystemPrompt: "sys", UserPrompt: "usr", Tools: &stubDispatcher{}})
	_, err := s.Run(context.Background())

	assert.ErrorContains(t, err, "exceeded max turns")
	assert.Equal(t, 2, s.Stats().Turns, "MaxTurns is the factory's")
	require.NotEmpty(t, provider.requests)
	req := provider.requests[0]
	assert.Equal(t, "the-model", req.Model)
	require.Len(t, req.Messages, 2)
	assert.Equal(t, "sys", req.Messages[0].Content)
	assert.Equal(t, "usr", req.Messages[1].Content)
}
