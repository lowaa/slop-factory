package usagemetrics

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/lowaa/slop-factory/go-auth/ratelimit"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
)

const (
	// MaxUIBatch is how many events one request may carry; the page sends
	// batches of at most this many.
	MaxUIBatch = 50
	// MaxUIBodyBytes caps the request body: fifty events of a few short
	// attributes each are a few kilobytes.
	MaxUIBodyBytes = 32 << 10
)

// UIHandler takes the board's browser usage events.
type UIHandler struct {
	ui      *UI
	limiter *ratelimit.Limiter
}

func NewUIHandler(ui *UI, limit *ratelimit.Config) *UIHandler {
	return &UIHandler{ui: ui, limiter: ratelimit.New(limit)}
}

type UIEventBody struct {
	Name  string         `json:"name" maxLength:"64"`
	Attrs map[string]any `json:"attrs,omitempty" maxProperties:"8"`
}

type UIEventsRequest struct {
	Body struct {
		Events []UIEventBody `json:"events" maxItems:"50"`
	}
}

// RecordEvents counts the batch. The user is known only to rate limit by:
// the limiter's key is held in memory and never reaches a metric or a log.
// Events that fail the allowlist are dropped without an error, so a client
// out of step with the server (an older page) is not shown a failure.
func (h *UIHandler) RecordEvents(ctx context.Context, in *UIEventsRequest) (*struct{}, error) {
	u, err := authn.UserFromContext(ctx)
	if err != nil {
		return nil, huma.Error401Unauthorized("sign in to continue")
	}
	if !h.limiter.Allow(u.ID.String()) {
		return nil, huma.Error429TooManyRequests("too many usage reports")
	}
	events := make([]UIEvent, len(in.Body.Events))
	for i, e := range in.Body.Events {
		events[i] = UIEvent{Name: e.Name, Attrs: e.Attrs}
	}
	h.ui.Record(events)
	return nil, nil
}

// RegisterUIRoutes mounts the route. RequireUser turns away anonymous callers
// and agent credentials, and the session's CSRF token is checked as for the
// board's other writes.
func RegisterUIRoutes(api huma.API, h *UIHandler) {
	huma.Register(api, huma.Operation{
		Metadata:      authn.RequireUser(),
		OperationID:   "recordUiUsage",
		Method:        http.MethodPost,
		Path:          "/api/usage/events",
		Summary:       "Count the board UI usage events a signed-in browser reports",
		Description:   "Events are checked against a fixed allowlist and only counted in aggregate; unknown events and attributes are dropped. 429 past the per-user rate limit.",
		DefaultStatus: http.StatusNoContent,
		MaxBodyBytes:  MaxUIBodyBytes,
	}, h.RecordEvents)
}
