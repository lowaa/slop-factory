package authn

import (
	"fmt"
	"log/slog"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
)

// Marker is operation metadata saying who may call an operation. Every
// operation carries exactly one (see Policy.DefaultDeny).
type Marker struct {
	key  string
	name string
}

// NewMarker is a marker stored under the metadata key key, named name in
// MarkerName. The key must be unique among a Policy's markers; prefix it
// with the app's name.
func NewMarker(key, name string) *Marker {
	return &Marker{key: key, name: name}
}

// Metadata is the operation metadata that marks an operation with m.
func (m *Marker) Metadata() map[string]any {
	return map[string]any{m.key: true}
}

// On reports whether op is marked with m.
func (m *Marker) On(op *huma.Operation) bool {
	return op != nil && op.Metadata[m.key] == true
}

// Name is m's name, as MarkerName reports it.
func (m *Marker) Name() string {
	return m.name
}

var (
	publicMarker = NewMarker("go-auth:public", "public")
	userMarker   = NewMarker("go-auth:requires-user", "user")

	optionalUserMarker = NewMarker("go-auth:optional-user", "optional-user")
)

// Public marks an operation as open to anyone: signing in, signing up,
// and the emailed-link exchanges that come before a session exists.
func Public() map[string]any {
	return publicMarker.Metadata()
}

// RequireUser marks an operation as needing a signed-in user.
func RequireUser() map[string]any {
	return userMarker.Metadata()
}

// OptionalUser marks an operation that is for anyone but uses the signed-in
// user when there is one: a consent banner's state, a telemetry proxy.
// UserMiddleware resolves the user as for RequireUser and carries on
// anonymous when there is none (see UserMiddleware for a credential that is
// refused). It is its own marker, never implied by Public or RequireUser,
// so no route gains it without saying so.
func OptionalUser() map[string]any {
	return optionalUserMarker.Metadata()
}

// Policy is the set of markers an app's operations choose from: Public,
// RequireUser, OptionalUser, and any of the app's own.
type Policy struct {
	markers []*Marker
}

// NewPolicy is a policy of Public, RequireUser, OptionalUser and the app's own markers.
// It panics on two markers sharing a key: the configuration is the
// program's own, and one marker quietly counting as another would be a
// hole found in production rather than at startup.
func NewPolicy(own ...*Marker) *Policy {
	markers := append([]*Marker{publicMarker, userMarker, optionalUserMarker}, own...)
	seen := map[string]bool{}
	for _, m := range markers {
		if seen[m.key] {
			panic(fmt.Sprintf("authn: two markers share the metadata key %q", m.key))
		}
		seen[m.key] = true
	}
	return &Policy{markers: markers}
}

// Markers counts the policy's markers on an operation. Exactly one is the
// only valid answer: none is a route whose author never decided, and two
// is one whose author decided twice, differently.
func (p *Policy) Markers(op *huma.Operation) int {
	n := 0
	for _, m := range p.markers {
		if m.On(op) {
			n++
		}
	}
	return n
}

// MarkerName reports which marker an operation carries, by name. Empty
// when none is set; "multiple" when more than one is -- both are states
// Markers already flags as invalid, so this is only for tests that want to
// name the marker a route carries, not just confirm it carries exactly one.
func (p *Policy) MarkerName(op *huma.Operation) string {
	found := ""
	for _, m := range p.markers {
		if m.On(op) {
			if found != "" {
				return "multiple"
			}
			found = m.name
		}
	}
	return found
}

// DefaultDeny refuses any operation not marked with exactly one of the
// policy's markers, so a route added without a decision is closed rather
// than open. Installed before the middlewares that act on the markers.
//
// A 500 rather than a 401 or 403: nothing the caller does can fix it, and
// a test over the route table catches it long before a caller can.
func (p *Policy) DefaultDeny(api huma.API, logHandler slog.Handler) func(huma.Context, func(huma.Context)) {
	logger := slog.New(logHandler)
	return func(ctx huma.Context, next func(huma.Context)) {
		op := ctx.Operation()
		if op == nil || p.Markers(op) != 1 {
			path := ""
			if op != nil {
				path = op.Method + " " + op.Path
			}
			logger.Error("refused an operation without exactly one auth marker", "operation", path)
			_ = huma.WriteErr(api, ctx, http.StatusInternalServerError, "internal server error")
			return
		}
		next(ctx)
	}
}
