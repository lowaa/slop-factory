package mcpserver

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/workspace"
)

// NewHTTPHandler builds the /mcp endpoint: one MCP server, served over the
// streamable HTTP transport, behind the same bearer-token auth as every
// other agent route.
//
// Auth is go-sdk's own bearer middleware rather than huma's, because the
// MCP handler is mounted directly on the mux -- MCP has its own request
// framing and is not an OpenAPI operation. It resolves the same OAuth
// access tokens as the rest of the agent API.
//
// resourceMetadataURL goes in the 401's challenge: it is where an MCP
// client finds out how to authorize (RFC 9728), which is how Claude Code
// discovers that this server wants OAuth at all.
func NewHTTPHandler(deps Deps, authenticate authn.Authenticator, resourceMetadataURL string, logger *slog.Logger) http.Handler {
	server := mcp.NewServer(&mcp.Implementation{
		Name:    "slop-haus",
		Version: "1.0.0",
	}, nil)
	h := NewHandler(deps, logger.Handler())
	Register(server, h)
	server.AddReceivingMiddleware(h.toolUsageGuard, h.readOnlyGuard, h.lockToolGuard)

	mcpHandler := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return server }, nil)

	// The SDK runs a stateful session's tool calls on a context derived from
	// its initialize request, not from the request that carried the call, so
	// the identity tools read is always the initializer's. Binding the session
	// to that agent through TokenInfo.UserID -- the SDK's own anti-hijacking
	// check -- is what makes that safe: a request whose token names any other
	// agent is refused with a 403 before it reaches the session. It binds the
	// agent rather than the token, so a client that refreshes its token
	// keeps its session.
	withIdentity := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		found, ok := identityFromTokenInfo(auth.TokenInfoFromContext(r.Context()))
		if !ok {
			// Only reachable if the verifier below stops recording the
			// identity: fail closed rather than serve an anonymous caller.
			logger.Error("mcp request passed auth without an identity")
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if r.Method == http.MethodPost {
			allowLongPoll(w, logger)
		}
		mcpHandler.ServeHTTP(w, r.WithContext(authn.NewContext(r.Context(), found)))
	})

	return auth.RequireBearerToken(func(ctx context.Context, token string, _ *http.Request) (*auth.TokenInfo, error) {
		found, err := authenticate(ctx, token)
		if err != nil {
			logger.Warn("rejected unauthenticated mcp request", "err", err)
			// The SDK writes the error text into the 401's body; the
			// underlying reason is for the log, not the caller.
			return nil, auth.ErrInvalidToken
		}
		// None of the MCP tools is a supervisor's: it creates workers and
		// mints their credentials, and does nothing a model does. Refused
		// here, with the token check, so no tool needs to know about it.
		if authn.IsSupervisor(found) {
			logger.Warn("rejected a supervisor's mcp request", "agent_id", found.AgentID())
			return nil, auth.ErrInvalidToken
		}
		return &auth.TokenInfo{
			UserID: sessionOwner(found),
			Extra:  map[string]any{identityKey: found},
		}, nil
	}, &auth.RequireBearerTokenOptions{
		ResourceMetadataURL: resourceMetadataURL,
		// Our access tokens' lifetime is enforced by authenticate on every
		// request (expiry, revocation, a disabled agent), not advertised here.
		AllowMissingExpiration: true,
	})(withIdentity)
}

// sessionOwner is who a session is bound to: the agent, and whether the
// credential is read-only. Both, so a read-only credential can't carry on a
// session its agent's full credential opened (acting with the full one's
// identity, which is what tools read), nor the other way round.
func sessionOwner(found authn.Identity) string {
	if found.ReadOnly() {
		return found.AgentID().String() + "/read-only"
	}
	return found.AgentID().String()
}

// identityKey is where the verifier leaves the resolved agent in
// TokenInfo.Extra, for the handler behind it to put on the request context.
const identityKey = "slop-haus.identity"

func identityFromTokenInfo(info *auth.TokenInfo) (authn.Identity, bool) {
	if info == nil {
		return nil, false
	}
	found, ok := info.Extra[identityKey].(authn.Identity)
	return found, ok && found != nil
}

// allowLongPoll lifts the server-wide write timeout (seconds; see
// internal/server) for one authenticated MCP request, so that a long tool
// call can run its course instead of having its response refused long before
// it ends. Only for a POST, which is what carries a tool call: the standalone
// GET stream has nothing that needs it, and each one would otherwise be a
// connection an agent could hold for half an hour. The read timeout is left
// alone: net/http clears the read deadline once the body has been read, so it
// bounds only how long the client may take to send its request.
func allowLongPoll(w http.ResponseWriter, logger *slog.Logger) {
	if err := http.NewResponseController(w).SetWriteDeadline(time.Now().Add(workspace.ToolCallTimeout)); err != nil {
		// Only a writer that does not support deadlines fails here; the
		// request still works, just not for a long wait.
		logger.Warn("could not extend the mcp write deadline", "err", err)
	}
}
