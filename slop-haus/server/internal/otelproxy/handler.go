// Package otelproxy is the browser's only path to the log store for the
// frontend's own error reports (js-components/logging's OtelLogger posts
// OTLP/JSON to /api/otel/v1/logs). The proxy itself is
// go-privacy/otelproxy; this package is slop-haus's policy and wiring.
//
// The route is authn.OptionalUser, not Public, so the signed-in user is
// there for the consent check once non-operational tiers exist.
package otelproxy

import (
	"context"
	"log/slog"

	"github.com/danielgtaylor/huma/v2"

	"github.com/lowaa/slop-factory/go-auth/ratelimit"
	"github.com/lowaa/slop-factory/go-privacy/otelproxy"
	"github.com/lowaa/slop-factory/go-privacy/telemetry"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
)

// Path is where the frontend's exporter posts, same-origin.
const Path = "/api/otel/v1/logs"

type Handler = otelproxy.Handler

// Options configure a Handler.
type Options struct {
	// Endpoint is the log store's full OTLP/HTTP logs URL. Empty accepts
	// and discards, as the api's own export does when none is configured.
	Endpoint string
	Policy   *telemetry.Policy
	Limit    *ratelimit.Config
	Logger   slog.Handler
}

// NewHandler forwards to the log store in protobuf, the only encoding
// VictoriaLogs accepts on its OTLP endpoint.
func NewHandler(opts *Options) (*Handler, error) {
	return otelproxy.NewHandler(&otelproxy.Options{
		LogsEndpoint: opts.Endpoint,
		Encoding:     otelproxy.Protobuf,
		Logs:         opts.Policy,
		Limit:        opts.Limit,
		Logger:       opts.Logger,
		Identify:     identify,
	})
}

// identify takes user.id from the verified session, never the payload. A
// refused credential means whoever this is can't be told apart from a
// visitor whose consent we'd otherwise look up (authn.OptionalUser), so a
// record needing consent is dropped rather than judged.
func identify(ctx context.Context) *telemetry.Caller {
	caller := &telemetry.Caller{ConsentUnknown: authn.CredentialRejected(ctx)}
	if u, err := authn.UserFromContext(ctx); err == nil {
		caller.UserID = u.ID.String()
	}
	return caller
}

// RegisterRoutes mounts the export route.
func RegisterRoutes(api huma.API, h *Handler) {
	h.Register(api, &otelproxy.Routes{LogsPath: Path, Metadata: authn.OptionalUser(), Hidden: true})
}

// ServiceName is forced as service.name on everything the proxy forwards.
// Must match the frontend's logger (slop-haus/frontend/src/context/logger_context.tsx).
const ServiceName = "slop-haus-frontend"

// OperationalAttributes are the keys an operational record may carry:
// go-privacy/README.md's route template, error type, message and stack,
// build tag and user agent, and the request
// method, status, server error code and trace id of a failed request
// (the traceparent's random per-request id, which finds the api's logs for it). The frontend's
// own allowlist is the same list; this is the gate that doesn't rest on the
// browser. Never a form value, name, result or filename. The signed-in
// user's id is not here: the proxy adds it from the verified session.
var OperationalAttributes = []string{
	"status",
	"error.type",
	"error.message",
	"error.stack",
	"error.code",
	"http.request.method",
	"http.route",
	"trace_id",
	"build.tag",
	"build.tag.server",
	"user_agent.original",
}

// NewPolicy is the proxy's policy: operational records only. A consented
// tier (analytics) is added here, with the consent check from go-privacy,
// when slop-haus has events to send and the consent routes exist.
func NewPolicy() (*telemetry.Policy, error) {
	return telemetry.New(&telemetry.Config{ServiceName: ServiceName, OperationalAttributes: OperationalAttributes})
}
