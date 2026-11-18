package consent

import (
	"context"
	"log/slog"
	"strings"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/go-auth/authn"
)

// UserResolver finds the signed-in user on a request's context, as the
// app's authn middleware left it. nil means anonymous.
type UserResolver func(ctx context.Context) *authn.User

// AuthnUser is the UserResolver for go-auth's authn middleware.
func AuthnUser(ctx context.Context) *authn.User {
	u, err := authn.UserFromContext(ctx)
	if err != nil {
		return nil
	}
	return u
}

// HandlerConfig configures a Handler.
type HandlerConfig struct {
	Service *Service
	// User is how the handler finds the signed-in user. It runs after the
	// app's authn middleware, so the handler's middleware has to be
	// installed after it.
	User UserResolver
	// AfterDecide, when set, runs after a signed-in user's decision has been
	// recorded and before the response is sent. Its error fails the request
	// (the decision stays recorded; the client may repeat it), so an app can
	// make a reaction to a withdrawal part of the withdrawal itself.
	AfterDecide AfterDecide
	Logger      slog.Handler
}

// AfterDecide is a HandlerConfig.AfterDecide hook. in is what the request
// asked for and res what is now in force for userID.
type AfterDecide func(ctx context.Context, userID uuid.UUID, in *DecideInput, res *Resolved) error

// Handler carries consent to the rest of a request, as middleware, and
// serves the consent routes.
type Handler struct {
	svc         *Service
	user        UserResolver
	afterDecide AfterDecide
	logger      *slog.Logger
}

func NewHandler(cfg *HandlerConfig) *Handler {
	if cfg.Service == nil || cfg.User == nil {
		panic("consent: a handler needs a service and a user resolver")
	}
	logHandler := cfg.Logger
	if logHandler == nil {
		logHandler = slog.DiscardHandler
	}
	return &Handler{svc: cfg.Service, user: cfg.User, afterDecide: cfg.AfterDecide, logger: slog.New(logHandler)}
}

type requestKey struct{}

// request is what the middleware worked out, for the routes.
type request struct {
	subject *Subject
	failed  bool
}

// GPCHeader is the request header carrying the Global Privacy Control
// signal. The spec's only valid value is "1".
const GPCHeader = "Sec-GPC"

// Middleware puts the effective consent on the request's context
// (FromContext), and records a signed-in user's GPC opt-out.
//
// A request that carries an Authorization header is a script's, not a
// browser's: it has no banner, never has its GPC recorded and doesn't
// read the consent cookie. Processing purposes still apply to it, from the
// user's record.
//
// If consent can't be worked out the request carries on with nothing
// granted: failing closed costs the person a banner, failing open costs
// their consent. Nothing about the person is logged.
func (h *Handler) Middleware() func(huma.Context, func(huma.Context)) {
	return func(ctx huma.Context, next func(huma.Context)) {
		now := h.svc.cfg.Clock()
		sub := &Subject{}
		if u := h.user(ctx.Context()); u != nil {
			sub.HasUser, sub.UserID, sub.SessionID = true, u.ID, u.SessionID
		}
		script := ctx.Header("Authorization") != ""
		if !script {
			sub.GPC = strings.TrimSpace(ctx.Header(GPCHeader)) == "1"
			if ck, err := huma.ReadCookie(ctx, h.svc.cfg.Cookie.Name()); err == nil {
				sub.Cookie = h.svc.cfg.Cookie.OpenValue(ck.Value, now)
			}
		}

		rq := &request{subject: sub}
		res, err := h.svc.Resolve(ctx.Context(), sub, false)
		if err != nil {
			h.logger.Error("consent unavailable for a request; nothing granted", "op", opOf(ctx), "err", errText(err))
			rq.failed = true
			res = &Resolved{Effective: &Effective{GPC: sub.GPC, SignedIn: sub.HasUser, byID: map[string]*PurposeState{}}}
		}
		c := context.WithValue(ctx.Context(), requestKey{}, rq)
		next(huma.WithContext(ctx, WithEffective(c, res.Effective)))
	}
}

func opOf(ctx huma.Context) string {
	if op := ctx.Operation(); op != nil {
		return op.Method + " " + op.Path
	}
	return ""
}

func requestFrom(ctx context.Context) *request {
	rq, _ := ctx.Value(requestKey{}).(*request)
	return rq
}
