package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/lowaa/slop-factory/slop-bot/internal/api"
)

func TestSystemAndNudgeMessagesAreFromSlopBot(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "slop-bot", sender(&api.Message{From: "user", Kind: "system"}))
	assert.Equal(t, "slop-bot", sender(&api.Message{From: "user", Kind: "nudge"}))
	assert.Equal(t, "user", sender(&api.Message{From: "user", Kind: "tell"}))
	assert.Equal(t, "a1", sender(&api.Message{From: "a1", Kind: "tell"}))
}
