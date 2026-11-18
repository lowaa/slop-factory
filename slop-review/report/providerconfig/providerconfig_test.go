package providerconfig

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-review/report"
)

func valid() *Config {
	return &Config{Revision: 2, Settings: Settings{
		Primary:    Choice{Provider: "chatgpt", Model: "gpt-5.6-luna", ReasoningEffort: "high"},
		Fallback:   &Choice{Provider: "openrouter", Model: "openai/gpt-5.6-luna-pro"},
		FallbackOn: []report.FailureClass{report.FailureUsageLimit, report.FailureAuth},
	}}
}

func TestValidate_AcceptsAFullConfigAndAPrimaryAlone(t *testing.T) {
	require.NoError(t, valid().Validate())
	require.NoError(t, (&Config{Revision: 1, Settings: Settings{Primary: Choice{Provider: "openrouter"}}}).Validate())
}

func TestValidate_RejectsEachMalformation(t *testing.T) {
	cases := map[string]struct {
		mutate func(c *Config)
		want   string
	}{
		"no revision":              {func(c *Config) { c.Revision = 0 }, "revision"},
		"unknown provider":         {func(c *Config) { c.Primary.Provider = "evil" }, `primary.provider "evil"`},
		"no provider":              {func(c *Config) { c.Primary.Provider = "" }, "primary.provider"},
		"unknown fallback":         {func(c *Config) { c.Fallback.Provider = "evil" }, `fallback.provider "evil"`},
		"model with a space":       {func(c *Config) { c.Primary.Model = "gpt 5" }, "not a model slug"},
		"model as a URL":           {func(c *Config) { c.Primary.Model = "https://evil.example/v1" }, "not a model slug"},
		"model with a query":       {func(c *Config) { c.Fallback.Model = "a?b=c" }, "not a model slug"},
		"model leading dash":       {func(c *Config) { c.Primary.Model = "-rf" }, "not a model slug"},
		"oversized model":          {func(c *Config) { c.Primary.Model = strings.Repeat("a", MaxModelBytes+1) }, "longer than"},
		"unknown effort":           {func(c *Config) { c.Primary.ReasoningEffort = "extreme" }, "reasoning_effort"},
		"effort on openrouter":     {func(c *Config) { c.Fallback.ReasoningEffort = "high" }, "selects effort by model slug"},
		"effort claude lacks":      {func(c *Config) { c.Primary = Choice{Provider: "claude", ReasoningEffort: "minimal"} }, "not accepted by"},
		"fallback same provider":   {func(c *Config) { c.Fallback.Provider = "chatgpt" }, "must differ"},
		"no trigger":               {func(c *Config) { c.FallbackOn = nil }, "fallback_on must name"},
		"config trigger":           {func(c *Config) { c.FallbackOn = []report.FailureClass{report.FailureConfig} }, `"config" is not one of`},
		"repeated trigger":         {func(c *Config) { c.FallbackOn = []report.FailureClass{"auth", "auth"} }, "repeated"},
		"trigger with no fallback": {func(c *Config) { c.Fallback = nil }, "fallback_on must be empty"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			c := valid()
			tc.mutate(c)
			err := c.Validate()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

// The wire type is the trust boundary: whatever a server sends, nothing
// in it can name where a request goes or which credential it carries.
func TestWireCarriesNoEndpointOrCredential(t *testing.T) {
	var c Config
	raw := `{"revision":1,"primary":{"provider":"openrouter","model":"m","base_url":"https://evil.example","api_key_env":"HOME","api_key":"k"}}`
	require.NoError(t, json.Unmarshal([]byte(raw), &c))
	out, err := json.Marshal(&c)
	require.NoError(t, err)
	for _, field := range []string{"base_url", "api_key", "evil.example"} {
		assert.NotContains(t, string(out), field)
	}
}

func TestValidate_AcceptsTheCLIProviders(t *testing.T) {
	c := &Config{Revision: 1, Settings: Settings{
		Primary:    Choice{Provider: "codex", ReasoningEffort: "none"},
		Fallback:   &Choice{Provider: "claude", ReasoningEffort: "max"},
		FallbackOn: DefaultTrigger,
	}}
	require.NoError(t, c.Validate())
	assert.Equal(t, "claude-sonnet-5-5", (&Choice{Provider: "claude"}).ModelOrDefault())
	assert.Equal(t, "gpt-5.6-luna", (&Choice{Provider: "codex"}).ModelOrDefault())
}

func TestModelOrDefault(t *testing.T) {
	assert.Equal(t, "openai/gpt-5.6-luna", (&Choice{Provider: "openrouter"}).ModelOrDefault())
	assert.Equal(t, "x", (&Choice{Provider: "openrouter", Model: "x"}).ModelOrDefault())
}

func TestEffortOrDefault(t *testing.T) {
	for name, want := range map[string]string{"claude": "xhigh", "codex": "xhigh", "chatgpt": "xhigh", "openrouter": "", "openai": ""} {
		p := Providers[name]
		assert.Equal(t, want, p.EffortOrDefault(""), name)
	}
	claude := Providers["claude"]
	assert.Equal(t, "low", claude.EffortOrDefault("low"), "a chosen effort is kept")
	lowOnly := Provider{ReasoningEffort: true, Efforts: []string{"low"}}
	assert.Empty(t, lowOnly.EffortOrDefault(""), "a provider that doesn't take xhigh gets none")
}

func TestFallsBackOn(t *testing.T) {
	c := valid()
	assert.True(t, c.FallsBackOn(report.FailureUsageLimit))
	assert.True(t, c.FallsBackOn(report.FailureAuth))
	assert.False(t, c.FallsBackOn(report.FailureBackend))
	c.Fallback = nil
	assert.False(t, c.FallsBackOn(report.FailureUsageLimit))
}
