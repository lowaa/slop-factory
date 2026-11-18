package main

import (
	"net/http"
	"net/netip"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/apiserver"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/oauth"
)

// oauthModule is the agents' OAuth endpoints. The service stays in
// newServices: the agent and ticket services reach it through lateDeps.
type oauthModule struct {
	Service *oauth.Service
	Handler *oauth.Handler
}

func newOAuthModule(d *moduleDeps) (module, error) {
	return &oauthModule{Service: d.svcs.oauth, Handler: oauth.NewHandler(d.svcs.oauth, d.logHandler)}, nil
}

func (m *oauthModule) Routes(r *apiserver.Deps) { r.OAuthHandler = m.Handler }

// Mount serves the OAuth endpoints and discovery documents on the mux rather
// than as huma operations: see internal/oauth/http.go.
func (m *oauthModule) Mount(mux *http.ServeMux, clientAddr func(*http.Request) netip.Addr) {
	m.Service.Mount(mux, clientAddr)
}

func (*oauthModule) Close() {}
