package main

import (
	"errors"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/apiserver"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost/github"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost/githubapp"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/githubhook"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/secretbox"
)

// githubHookModule is the webhook service, with the door shut unless
// WEBHOOK_PUBLIC_BASE_URL and the credential key are configured: the App's
// webhook secrets are sealed under the credential key.
type githubHookModule struct {
	Service *githubhook.Service
	Handler *githubhook.Handler
}

func newGitHubHookModule(d *moduleDeps) (module, error) {
	host, ok := d.svcs.hosts.Host(github.New().Name())
	if !ok {
		return nil, errors.New("the GitHub host is not registered")
	}
	whCfg := &githubhook.Config{Host: host, Archive: d.tenants}
	if d.cfg.Webhook != nil && d.keyring != nil {
		whCfg.AppSecrets = appWebhookSecrets{d.keyring}
	}
	svc, err := githubhook.NewService(githubhook.NewPostgresStore(d.database), d.svcs.tickets, whCfg, d.logHandler)
	if err != nil {
		return nil, err
	}
	return &githubHookModule{Service: svc, Handler: githubhook.NewHandler(svc, d.logHandler)}, nil
}

func (m *githubHookModule) Routes(r *apiserver.Deps) { r.WebhookHandler = m.Handler }

func (*githubHookModule) Close() {}

// appWebhookSecrets opens the GitHub App webhook secrets the credential
// service sealed.
type appWebhookSecrets struct{ keyring *secretbox.Keyring }

func (a appWebhookSecrets) Open(tenant, credential uuid.UUID, sealed []byte) (string, error) {
	secret, err := githubapp.OpenWebhookSecret(a.keyring, tenant, credential, sealed)
	if err != nil {
		return "", err
	}
	return secret.Reveal(), nil
}
