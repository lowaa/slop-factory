package main

import (
	"github.com/danielgtaylor/huma/v2"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/apiserver"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/config"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/otelproxy"
	"github.com/lowaa/slop-factory/slop-haus/server/privacy"
)

// privacyModule is the consent and legal document endpoints, and the frontend's
// error reports proxy, which go to the same log store as the api's own
// records (LOGS_OTLP_ENDPOINT).
type privacyModule struct {
	services *privacy.Services
	otel     *otelproxy.Handler
}

func newPrivacyModule(d *moduleDeps) (module, error) {
	cfg := d.cfg
	policy, err := otelproxy.NewPolicy()
	if err != nil {
		return nil, err
	}
	otel, err := otelproxy.NewHandler(&otelproxy.Options{
		Endpoint: cfg.LogsOTLPEndpoint,
		Policy:   policy,
		Limit:    d.limits.limit(config.OtelProxyLimit()),
		Logger:   d.logHandler,
	})
	if err != nil {
		return nil, err
	}
	services, err := privacy.Setup(d.ctx, &privacy.Config{
		Engine: d.database.Engine(), Secret: []byte(cfg.JWTSecret), SecureCookies: cfg.SecureCookies(), Logger: d.logHandler,
	})
	if err != nil {
		return nil, err
	}
	return &privacyModule{services: services, otel: otel}, nil
}

func (m *privacyModule) Routes(r *apiserver.Deps) {
	r.ConsentHandler = m.services.Consent
	r.LegalDocs = m.services.Docs
	r.OtelProxy = m.otel
}

// Middleware is applied after the user check, which resolves the user it
// reads. Scoped to the consent routes: see privacy.Services.Middleware.
func (m *privacyModule) Middleware() func(huma.Context, func(huma.Context)) {
	return m.services.Middleware()
}

func (*privacyModule) Close() {}
