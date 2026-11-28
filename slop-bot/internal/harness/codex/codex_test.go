package codex

import (
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-bot/internal/harness"
	"github.com/lowaa/slop-factory/slop-bot/internal/harness/codex/protocol"
)

func TestThreadParams(t *testing.T) {
	t.Parallel()
	start, resume := threadParams(&harness.Workspace{Dir: "/ws", Permissive: true, Model: "gpt-x", Briefing: "brief"})
	for _, p := range []struct {
		cwd, model, brief string
		approval          any
		sandbox           protocol.SandboxMode
	}{
		{start.Cwd, start.Model, start.DeveloperInstructions, start.ApprovalPolicy, start.Sandbox},
		{resume.Cwd, resume.Model, resume.DeveloperInstructions, resume.ApprovalPolicy, resume.Sandbox},
	} {
		assert.Equal(t, "/ws", p.cwd)
		assert.Equal(t, "gpt-x", p.model)
		assert.Equal(t, "brief", p.brief)
		assert.Equal(t, protocol.AskForApprovalNever, p.approval)
		assert.Equal(t, protocol.SandboxModeDangerFullAccess, p.sandbox)
	}

	start, _ = threadParams(&harness.Workspace{Dir: "/ws"})
	assert.Equal(t, protocol.AskForApprovalNever, start.ApprovalPolicy, "nobody to answer an approval, permissive or not")
	assert.Empty(t, start.Sandbox, "the user's configured sandbox")
}

func TestCommonFlags(t *testing.T) {
	t.Parallel()
	h := &Harness{}
	args, err := h.common(&harness.Workspace{})
	require.NoError(t, err)
	assert.Empty(t, args, "the user's own configuration, untouched")

	h = &Harness{
		ModelProvider: &ModelProvider{ID: "scripted", BaseURL: "http://127.0.0.1:9/v1"},
		Config:        []string{`features.goals=false`},
	}
	args, err = h.common(&harness.Workspace{Model: "gpt-x", Effort: "high", ExtraDirs: []string{"/ws/a", "/ws/b"}})
	require.NoError(t, err)
	assert.Equal(t, []string{
		"-c", `model_provider="scripted"`,
		"-c", `model_providers.scripted.name="scripted"`,
		"-c", `model_providers.scripted.base_url="http://127.0.0.1:9/v1"`,
		"-c", `model_providers.scripted.wire_api="responses"`,
		"-c", `features.goals=false`,
		"-c", `model="gpt-x"`,
		"-c", `model_reasoning_effort="high"`,
		"-c", `sandbox_workspace_write.writable_roots=["/ws/a","/ws/b"]`,
	}, args)
}

func TestAModelProviderIDMustBeABareKey(t *testing.T) {
	t.Parallel()
	_, err := (&Harness{ModelProvider: &ModelProvider{ID: "a.b", BaseURL: "x"}}).common(&harness.Workspace{})
	assert.ErrorContains(t, err, "bare key")
}

func TestAttachResumesTheThreadInTheTUI(t *testing.T) {
	t.Parallel()
	common := []string{"-c", `model="gpt-x"`}
	assert.Equal(t, []string{
		"resume", "id-1", "--no-daemon", "--dangerously-bypass-approvals-and-sandbox", "-c", `model="gpt-x"`,
	}, attachArgs("id-1", &harness.Workspace{Permissive: true}, common))
	assert.Equal(t, []string{"resume", "id-1", "--no-daemon", "--ask-for-approval", "never"},
		attachArgs("id-1", &harness.Workspace{}, nil))
}

func TestEnvKeepsTheCodexHomeAndAPIKeysOut(t *testing.T) {
	t.Parallel()
	h := &Harness{Environ: func() []string {
		return []string{"PATH=/bin", "CODEX_HOME=/srv/codex", "OPENAI_API_KEY=sk", "CODEX_API_KEY=ck"}
	}}
	env := h.env(&harness.Workspace{Env: map[string]string{"SLOP_BOT_AGENT_TOKEN": "tok"}})
	assert.Contains(t, env, "CODEX_HOME=/srv/codex", "the user's own Codex home (trust.go)")
	assert.Contains(t, env, "SLOP_BOT_AGENT_TOKEN=tok")
	assert.NotContains(t, env, "OPENAI_API_KEY=sk", "an API key would bill per token instead of the ChatGPT plan")
	assert.NotContains(t, env, "CODEX_API_KEY=ck")
	assert.Contains(t, h.env(&harness.Workspace{APIKeyOptIn: true}), "OPENAI_API_KEY=sk")
}

func TestTOMLStringRoundTrips(t *testing.T) {
	t.Parallel()
	for _, s := range []string{"plain", `quo"te`, `back\slash`, "new\nline\ttab", "ctl\x01\x7f", "ünïcode ✓", ""} {
		var v struct{ K string }
		_, err := toml.Decode("K = "+tomlString(s), &v)
		require.NoError(t, err, s)
		assert.Equal(t, s, v.K)
	}
}

func TestCapabilities(t *testing.T) {
	t.Parallel()
	h := &Harness{}
	assert.Equal(t, "codex", h.Name())
	assert.Equal(t, "app-server", h.Driver())
	assert.Equal(t, harness.Capabilities{MidTurnSteer: true, ChosenIDs: false}, h.Capabilities())
}
