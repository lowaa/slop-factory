package authn

import (
	"log/slog"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	goauthn "github.com/lowaa/slop-factory/go-auth/authn"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
)

// MetadataKey marks an operation as requiring an authenticated agent.
// Routes opt in explicitly:
//
//	huma.Register(api, huma.Operation{..., Metadata: authn.RequireAgent()}, h.Claim)
//
// Every operation carries exactly one of RequireAgent, RequireUser or
// Public; DefaultDeny refuses any that does not, so forgetting to choose
// closes a route rather than opening it.
const MetadataKey = "slop-haus:requires-agent"

var agentMarker = goauthn.NewMarker(MetadataKey, "agent")

// RequireAgent returns the operation metadata marking a route as
// agent-authenticated.
func RequireAgent() map[string]any {
	return agentMarker.Metadata()
}

// HumaMiddleware enforces bearer-token auth on operations marked with
// RequireAgent, and puts the resolved agent on the request context where
// FromContext finds it.
//
// A route that asks for an agent and doesn't get one never reaches its
// handler, so handlers can treat FromContext's error as a wiring bug rather
// than an auth failure.
//
// A read-only credential is refused every route here: the agent REST and
// hook routes all claim, write or record the agent's activity, and
// refusing the whole surface means a new route is closed to it by default
// rather than open until someone remembers. Its one use is MCP's reads.
func HumaMiddleware(api huma.API, authenticate Authenticator, events *audit.Recorder, logger *slog.Logger) func(huma.Context, func(huma.Context)) {
	return func(ctx huma.Context, next func(huma.Context)) {
		op := ctx.Operation()
		if !agentMarker.On(op) {
			next(ctx)
			return
		}

		token := goauthn.BearerToken(ctx.Header("Authorization"))
		found, err := authenticate(ctx.Context(), token)
		if err != nil {
			// Logged with the real reason, answered with a bare 401:
			// telling a caller whether a token was unknown or merely
			// disabled tells it which tokens are real.
			logger.Warn("rejected unauthenticated request",
				"path", op.Path, "method", op.Method, "err", err)
			ctx.SetHeader("WWW-Authenticate", `Bearer realm="slop-haus"`)
			_ = huma.WriteErr(api, ctx, 401, "unauthorized")
			return
		}
		if found.ReadOnly() {
			events.Record(ctx.Context(), audit.ReadOnlyRefused,
				"agent_id", found.AgentID(), "tenant", found.TenantID(), "path", op.Path, "http_method", op.Method)
			_ = huma.WriteErr(api, ctx, http.StatusForbidden, "forbidden")
			return
		}
		if !SupervisorAllowed(op, IsSupervisor(found)) {
			events.Record(ctx.Context(), audit.SupervisorRouteRefused,
				"agent_id", found.AgentID(), "tenant", found.TenantID(), "path", op.Path, "http_method", op.Method)
			_ = huma.WriteErr(api, ctx, http.StatusForbidden, "forbidden")
			return
		}
		next(huma.WithContext(ctx, NewContext(ctx.Context(), found)))
	}
}
