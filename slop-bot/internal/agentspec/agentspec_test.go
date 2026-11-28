package agentspec

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-bot/internal/config"
	"github.com/lowaa/slop-factory/slop-bot/internal/tickets"
)

func cfg(t *testing.T) *config.Config {
	t.Helper()
	c, err := config.Parse([]byte("harnesses: {claude-code: {}, codex: {}, pi: {}}\n"), "/c", "/h")
	require.NoError(t, err)
	return c
}

func TestResolveFillsOnlyTheHarness(t *testing.T) {
	t.Parallel()
	got := Resolve(tickets.AgentSpec{}, cfg(t))
	assert.Equal(t, tickets.AgentSpec{Harness: config.ClaudeCode}, got)
	assert.Equal(t, tickets.AgentSpec{Harness: "pi", Model: "m"}, Resolve(tickets.AgentSpec{Harness: "pi", Model: "m"}, cfg(t)))
}

func TestValidate(t *testing.T) {
	t.Parallel()
	c := cfg(t)
	cases := map[string]struct {
		spec tickets.AgentSpec
		want string
	}{
		"defaults":              {tickets.AgentSpec{Harness: "claude-code"}, ""},
		"claude effort":         {tickets.AgentSpec{Harness: "claude-code", Effort: "xhigh"}, ""},
		"pi thinking":           {tickets.AgentSpec{Harness: "pi", Effort: "minimal"}, ""},
		"codex effort is later": {tickets.AgentSpec{Harness: "codex", Effort: "whatever"}, ""},
		"unconfigured harness":  {tickets.AgentSpec{Harness: "agy"}, `harness "agy" isn't configured on this machine (configured: claude-code, codex, pi)`},
		"unknown harness":       {tickets.AgentSpec{Harness: "cursor"}, `harness "cursor" isn't configured`},
		"bad effort for claude": {tickets.AgentSpec{Harness: "claude-code", Effort: "minimal"}, `effort "minimal" isn't one claude-code accepts`},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			err := Validate(tc.spec, c)
			if tc.want == "" {
				assert.NoError(t, err)
				return
			}
			assert.ErrorIs(t, err, ErrInvalid)
			assert.ErrorContains(t, err, tc.want)
		})
	}
}
