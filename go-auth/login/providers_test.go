package login_test

import (
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/lowaa/slop-factory/go-auth/login"
)

func TestProvidersKeepTheirOrderAndAreFoundByName(t *testing.T) {
	github := login.NewGitHubAuthProvider("id", "secret", "https://app.example/cb", login.DefaultGitHubEndpoints)
	dex := login.NewOIDCAuthProvider(&login.OIDCOptions{IssuerURL: "https://dex.example", Name: "dex"}, slog.DiscardHandler)
	p := login.NewProviders(dex, github)

	assert.Equal(t, []login.AuthProvider{dex, github}, p.List(), "in the order the sign-in page offers them")
	assert.Equal(t, []string{"dex", "github"}, p.Names())
	got, ok := p.Get("github")
	assert.True(t, ok)
	assert.Same(t, github, got)
	_, ok = p.Get("gitlab")
	assert.False(t, ok, "a provider not configured here")
}

// A service with no providers configured may hold a nil *Providers.
func TestNilProvidersHaveNone(t *testing.T) {
	var p *login.Providers
	_, ok := p.Get("github")
	assert.False(t, ok)
	assert.Empty(t, p.List())
	assert.Empty(t, p.Names())
}

func TestProvidersRefuseTwoWithOneName(t *testing.T) {
	first := login.NewOIDCAuthProvider(&login.OIDCOptions{IssuerURL: "https://a.example", Name: "sso"}, slog.DiscardHandler)
	second := login.NewOIDCAuthProvider(&login.OIDCOptions{IssuerURL: "https://b.example", Name: "sso"}, slog.DiscardHandler)
	assert.Panics(t, func() { login.NewProviders(first, second) })
}
