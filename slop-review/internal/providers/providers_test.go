package providers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	llmusage "github.com/lowaa/slop-factory/go-llm-usage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-review/internal/config"
	"github.com/lowaa/slop-factory/slop-review/internal/llm"
)

func TestNew_OpenAICompatibleProfilesBuildTheChatCompletionsProvider(t *testing.T) {
	for _, name := range []string{"openrouter", "openai"} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("OPENROUTER_API_KEY", "test-key")
			t.Setenv("OPENAI_API_KEY", "test-key")
			cfg, err := config.Load([]string{"--provider=" + name})
			require.NoError(t, err)

			p, err := (&Factory{}).New(context.Background(), &cfg.Route)
			require.NoError(t, err)
			assert.IsType(t, &llm.OpenAIAPILLMProvider{}, p)
		})
	}
}

// fakePi writes a pi stand-in that answers `auth check` with checkJSON.
func fakePi(t *testing.T, checkJSON string, exit int) []string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "pi")
	script := "#!/bin/sh\necho '" + checkJSON + "'\nexit " + strconv.Itoa(exit) + "\n"
	require.NoError(t, os.WriteFile(path, []byte(script), 0o755))
	return []string{path}
}

func TestNew_ChatGPTBuildsTheResponsesProviderWhenLoggedIn(t *testing.T) {
	cfg, err := config.Load([]string{"--provider=chatgpt"})
	require.NoError(t, err)

	f := &Factory{PiCommand: fakePi(t, `{"status":"ready","provider":"openai","authType":"oauth"}`, 0)}
	p, err := f.New(context.Background(), &cfg.Route)
	require.NoError(t, err)
	assert.IsType(t, &llm.ResponsesLLMProvider{}, p)
}

func TestNew_ChatGPTRefusesToStartWithoutALogin(t *testing.T) {
	cfg, err := config.Load([]string{"--provider=chatgpt"})
	require.NoError(t, err)

	for name, pi := range map[string][]string{
		"not logged in": fakePi(t, `{"status":"not_ready","provider":"openai","reason":"credentials_not_configured"}`, 1),
		"api key":       fakePi(t, `{"status":"ready","provider":"openai","authType":"api_key"}`, 0),
		"no pi":         {filepath.Join(t.TempDir(), "missing-pi")},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := (&Factory{PiCommand: pi}).New(context.Background(), &cfg.Route)
			require.ErrorIs(t, err, llm.ErrAuth)
			assert.Contains(t, err.Error(), "/login")
			assert.Contains(t, err.Error(), "SLOP_REVIEW_PROVIDER=openrouter")
		})
	}
}

func TestNew_UnknownKindIsAnError(t *testing.T) {
	_, err := (&Factory{}).New(context.Background(), &config.Route{Provider: "made-up", ProviderKind: "made-up"})
	assert.ErrorContains(t, err, "made-up")
}

// How each provider bills, and whether its reported cost is read, goes
// from the profile through config and the factory to the provider: only
// OpenRouter's usage.cost is ever read.
func TestNew_BillingAndReportedCostFollowTheProfile(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"c","object":"chat.completion","created":0,"model":"m",
			"choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"done"}}],
			"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2,"cost":0.5}}`))
	}))
	t.Cleanup(server.Close)
	half := int64(500_000)
	for name, want := range map[string]struct {
		billing llmusage.BillingMode
		cost    *int64
	}{
		"openrouter": {llmusage.BillingMetered, &half},
		"openai":     {llmusage.BillingMetered, nil},
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("OPENROUTER_API_KEY", "test-key")
			t.Setenv("OPENAI_API_KEY", "test-key")
			cfg, err := config.Load([]string{"--provider=" + name})
			require.NoError(t, err)
			assert.Equal(t, want.billing, cfg.BillingMode)

			p, err := (&Factory{BaseURLs: map[string]string{name: server.URL}}).New(context.Background(), &cfg.Route)
			require.NoError(t, err)
			resp, err := p.Complete(context.Background(), llm.CompletionRequest{Model: "m", Messages: []llm.Message{{Role: llm.RoleUser, Content: "hi"}}})
			require.NoError(t, err)
			assert.Equal(t, want.cost, resp.Usage.ReportedCostMicros)
		})
	}

	cfg, err := config.Load([]string{"--provider=chatgpt"})
	require.NoError(t, err)
	assert.Equal(t, llmusage.BillingSubscription, cfg.BillingMode)
	assert.False(t, cfg.ReportsCost)
}
