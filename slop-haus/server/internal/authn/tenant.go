package authn

import (
	"context"
	"log/slog"
	"net/http"
	"slices"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	goauthn "github.com/lowaa/slop-factory/go-auth/authn"
)

// TenantHeader names the tenant the page making the request loaded in. The
// session is httpOnly cookies shared by every tab of the browser, so a
// tenant switch in one tab re-issues them for all the others: without this
// a tab left open on tenant A would act on tenant B, and ticket numbers
// being per tenant, "approve #42" would land on B's #42.
const TenantHeader = "X-Tenant-ID"

// TenantQuery is TenantHeader for a request that cannot set headers: an
// EventSource.
const TenantQuery = "tenant"

// CodeTenantChanged tells the page its session has since moved to another
// tenant.
const CodeTenantChanged = "tenant_changed"

// TenantGuard refuses a browser-session request made by a page that loaded
// in a different tenant than the session is in now, before the handler
// runs. Installed after UserMiddleware, which has resolved the user.
//
// Only cookie sessions are checked: an agent, an MCP client or an OAuth
// client names its credential in an Authorization header, which belongs to
// one tenant and cannot go stale. A cookie write must name its tenant: the
// board's pages send it on every request, so a write without it is not from
// one of them. A cookie read may omit it, as an EventSource and a
// plain navigation cannot set headers; the stream names it in TenantQuery.
//
// exempt are the operations that exist to move between tenants (or to list
// them, which the notice a stale page shows needs), and so run from a page
// that is by definition in the wrong one.
//
// The refusal names neither tenant: the page knows its own, and learns the
// session's through the exempt listing, which only its owner can call.
func TenantGuard(api huma.API, exempt []string, logger *slog.Logger) func(huma.Context, func(huma.Context)) {
	return func(ctx huma.Context, next func(huma.Context)) {
		op := ctx.Operation()
		who, err := goauthn.UserFromContext(ctx.Context())
		if err != nil || goauthn.BearerToken(ctx.Header("Authorization")) != "" || slices.Contains(exempt, op.OperationID) {
			next(ctx)
			return
		}
		claimed := ctx.Header(TenantHeader)
		if claimed == "" {
			claimed = ctx.Query(TenantQuery)
		}
		if claimed != "" && claimed != who.TenantID.String() {
			logger.Info("refused a request from a page in another tenant", "path", op.Path, "http_method", ctx.Method(), "user_id", who.ID)
			writeTenantProblem(api, ctx, http.StatusConflict, CodeTenantChanged, "this session has moved to another tenant since the page loaded")
			return
		}
		next(ctx)
	}
}

// CodeTenantArchived tells the page its session's tenant was archived, so it
// can offer the tenant switcher.
const CodeTenantArchived = "tenant_archived"

// ArchivedChecker reports whether a tenant has been archived.
type ArchivedChecker func(ctx context.Context, tenantID uuid.UUID) (bool, error)

// ArchivedTenantGuard refuses every request authenticated as a user of an
// archived tenant, cookie or bearer, before the handler runs. Installed
// after UserMiddleware and before TenantGuard: a page left on an archived
// tenant is told that, rather than that its tenant changed.
//
// Fails closed: a lookup that errors refuses the request. exempt are the
// operations that exist to leave the tenant (see TenantGuard).
func ArchivedTenantGuard(api huma.API, archived ArchivedChecker, exempt []string, logger *slog.Logger) func(huma.Context, func(huma.Context)) {
	return func(ctx huma.Context, next func(huma.Context)) {
		op := ctx.Operation()
		who, err := goauthn.UserFromContext(ctx.Context())
		if err != nil || slices.Contains(exempt, op.OperationID) {
			next(ctx)
			return
		}
		gone, err := archived(ctx.Context(), who.TenantID)
		if err != nil {
			logger.Error("check tenant archived", "err", err, "tenant", who.TenantID)
			_ = huma.WriteErr(api, ctx, http.StatusInternalServerError, "internal server error")
			return
		}
		if gone {
			logger.Info("refused a request to an archived tenant", "path", op.Path, "http_method", ctx.Method(), "user_id", who.ID, "tenant", who.TenantID)
			writeTenantProblem(api, ctx, http.StatusForbidden, CodeTenantArchived, "this tenant was archived")
			return
		}
		next(ctx)
	}
}

func writeTenantProblem(api huma.API, ctx huma.Context, status int, code, detail string) {
	p := NewProblem(status, code, detail)
	ctx.SetHeader("Content-Type", "application/problem+json")
	ctx.SetStatus(status)
	_ = api.Marshal(ctx.BodyWriter(), "application/json", p)
}
