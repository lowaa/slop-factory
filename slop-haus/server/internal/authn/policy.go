package authn

import (
	"log/slog"

	"github.com/danielgtaylor/huma/v2"

	goauthn "github.com/lowaa/slop-factory/go-auth/authn"
)

// policy is every marker a slop-haus operation may carry: go-auth's Public,
// RequireUser and OptionalUser, and slop-haus's RequireAgent and RequireAdmin.
var policy = goauthn.NewPolicy(agentMarker, adminMarker)

// Public marks an operation as open to anyone: signing in, signing up,
// and the emailed-link exchanges that come before a session exists.
func Public() map[string]any {
	return goauthn.Public()
}

// Markers counts the auth markers on an operation. Exactly one is the
// only valid answer: none is a route whose author never decided, and two
// is one whose author decided twice, differently.
func Markers(op *huma.Operation) int {
	return policy.Markers(op)
}

// MarkerName reports which auth marker an operation carries: "agent",
// "user", "admin" or "public". Empty when none is set; "multiple" when more
// than one is -- both are states Markers already flags as invalid, so this
// is only for tests that want to name the marker a route carries, not just
// confirm it carries exactly one.
func MarkerName(op *huma.Operation) string {
	return policy.MarkerName(op)
}

// DefaultDeny refuses any operation not marked with exactly one of
// RequireAgent, RequireUser, OptionalUser, RequireAdmin or Public, so a route added without a
// decision is closed rather than open. Installed before the middlewares
// that act on the markers.
func DefaultDeny(api huma.API, logger *slog.Logger) func(huma.Context, func(huma.Context)) {
	return policy.DefaultDeny(api, logger.Handler())
}
