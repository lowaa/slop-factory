package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/lowaa/slop-factory/slop-bot/internal/api"
)

func TestTheTUIGetsTheAttachingTerminalsVariables(t *testing.T) {
	t.Parallel()
	daemons := &api.Command{Path: "claude", Env: []string{"PATH=/usr/bin", "TERM=dumb", "SLOP_BOT_AGENT_TOKEN=t"}}
	got := withTerminalEnv(daemons, []string{"TERM=xterm-256color", "COLORTERM=truecolor", "LC_ALL=en_GB.UTF-8", "HOME=/home/me", "SECRET=x"})
	assert.Equal(t, []string{"PATH=/usr/bin", "SLOP_BOT_AGENT_TOKEN=t", "COLORTERM=truecolor", "LC_ALL=en_GB.UTF-8", "TERM=xterm-256color"}, got.Env)
	assert.Equal(t, []string{"PATH=/usr/bin", "TERM=dumb", "SLOP_BOT_AGENT_TOKEN=t"}, daemons.Env, "the daemon's command is left alone")
}
