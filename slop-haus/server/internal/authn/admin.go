package authn

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	goauthn "github.com/lowaa/slop-factory/go-auth/authn"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
)

var adminMarker = goauthn.NewMarker("slop-haus:requires-admin", "admin")

// RequireAdmin marks an operation as needing a signed-in server admin:
// someone who may invite people to start tenants of their own. It implies
// RequireUser -- UserMiddleware authenticates these operations too -- and
// replaces it, since an operation carries exactly one marker.
func RequireAdmin() map[string]any {
	return adminMarker.Metadata()
}

// AdminChecker reports whether a user is an admin. Asked of the database
// on every admin request rather than carried in the session token, so
// revoking an admin takes effect at once, not when their token expires.
type AdminChecker func(ctx context.Context, userID uuid.UUID) (bool, error)

// AdminMiddleware refuses a non-admin on operations marked RequireAdmin,
// recording each refusal with events. Installed after UserMiddleware, which
// has already put the user on the context.
func AdminMiddleware(api huma.API, isAdmin AdminChecker, events *audit.Recorder, logger *slog.Logger) func(huma.Context, func(huma.Context)) {
	return func(ctx huma.Context, next func(huma.Context)) {
		op := ctx.Operation()
		if !adminMarker.On(op) {
			next(ctx)
			return
		}
		who, err := UserFromContext(ctx.Context())
		if err != nil {
			logger.Error("admin operation reached without a user; is UserMiddleware installed first?", "path", op.Path)
			_ = huma.WriteErr(api, ctx, http.StatusInternalServerError, "internal server error")
			return
		}
		admin, err := isAdmin(ctx.Context(), who.ID)
		if err != nil {
			logger.Error("check admin", "err", err)
			_ = huma.WriteErr(api, ctx, http.StatusInternalServerError, "internal server error")
			return
		}
		if !admin {
			events.Record(ctx.Context(), audit.AdminRefused, "user_id", who.ID, "path", op.Path, "http_method", op.Method)
			_ = huma.WriteErr(api, ctx, http.StatusForbidden, "forbidden")
			return
		}
		next(ctx)
	}
}
