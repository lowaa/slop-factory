package privacy

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/danielgtaylor/huma/v2"

	"github.com/lowaa/slop-factory/go-db/engine"
	"github.com/lowaa/slop-factory/go-privacy/consent"
	"github.com/lowaa/slop-factory/go-privacy/legaldocs"
)

// CookieName is the anonymous visitor's consent cookie, named like the
// session's (slop_haus_session).
const CookieName = "slop_haus_consent"

// Config is what Setup needs from the app.
type Config struct {
	Engine engine.Engine
	// Secret keys the consent cookie's seal, through its own derived key.
	Secret []byte
	// SecureCookies is whether the board is served over https.
	SecureCookies bool
	Logger        slog.Handler
}

// Services is slop-haus's consent and legal-document handlers, ready to
// mount.
type Services struct {
	Consent *consent.Handler
	Docs    *legaldocs.Handler
}

// Setup publishes the embedded legal documents the database lacks and builds
// the handlers over the database. A published version is left as it is (see
// legaldocs.Service.Publish); versions.lock holds the files still in CI.
func Setup(ctx context.Context, cfg *Config) (*Services, error) {
	inv, err := Inventory()
	if err != nil {
		return nil, fmt.Errorf("privacy: inventory: %w", err)
	}
	versions, err := LoadLegalDocuments(inv)
	if err != nil {
		return nil, err
	}
	kinds, err := legaldocs.NewKinds()
	if err != nil {
		return nil, err
	}
	docs, err := legaldocs.NewService(&legaldocs.Config{
		Kinds: kinds, Store: legaldocs.NewPostgresStore(cfg.Engine), Logger: cfg.Logger,
	})
	if err != nil {
		return nil, err
	}
	if err := docs.Publish(ctx, versions); err != nil {
		return nil, err
	}

	registry, err := Registry()
	if err != nil {
		return nil, err
	}
	codec, err := consent.NewCookieCodec(&consent.CookieConfig{Name: CookieName, Secret: cfg.Secret, Secure: cfg.SecureCookies})
	if err != nil {
		return nil, err
	}
	svc, err := consent.NewService(&consent.Config{
		Registry: registry, Documents: docs, Store: consent.NewPostgresStore(cfg.Engine), Cookie: codec, Logger: cfg.Logger,
	})
	if err != nil {
		return nil, err
	}
	return &Services{
		Consent: consent.NewHandler(&consent.HandlerConfig{Service: svc, User: consent.AuthnUser, Logger: cfg.Logger}),
		Docs:    legaldocs.NewHandler(docs),
	}, nil
}

// Middleware puts the effective consent on the context of the consent
// routes, and of nothing else.
//
// It is scoped to them on purpose. Consent here governs a browser's storage,
// and slop-haus's other callers are not browsers: agents and the MCP client
// carry a bearer token, the webhook is GitHub's. Running consent on every
// request would cost each of them a database read to compute something no
// handler uses (slop-haus has no processing purposes to gate), and would put
// the consent tables in the path of every board request. Scoped, a bearer
// client's request never reaches consent logic at all, and an outage in
// these tables costs a visitor their banner and nothing else.
//
// If a route ever needs consent on the context (a processing purpose, or the
// otel proxy's analytics tier), add it here and say why.
func (s *Services) Middleware() func(huma.Context, func(huma.Context)) {
	inner := s.Consent.Middleware()
	return func(ctx huma.Context, next func(huma.Context)) {
		if op := ctx.Operation(); op == nil || op.Path != consent.DefaultPath {
			next(ctx)
			return
		}
		inner(ctx, next)
	}
}
