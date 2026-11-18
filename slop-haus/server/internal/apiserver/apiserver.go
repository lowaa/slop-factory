// Package apiserver is the single place that describes slop-haus's full API
// surface: the huma.Config it's served under and the list of operations
// mounted on it. cmd/api (the real server) and cmd/generate_openapi_spec
// (whose output feeds openapi-typescript) both build against this package
// instead of each duplicating route registration, so the served API and the
// generated client can never drift from each other.
package apiserver

import (
	"log/slog"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"

	"github.com/lowaa/slop-factory/go-privacy/consent"
	"github.com/lowaa/slop-factory/go-privacy/legaldocs"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/agent"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/agentmsg"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/attachment"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/attemptverdict"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost/githubapp"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/conductor"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/evalset"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/fleetinstructions"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/githubhook"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/harnessconfig"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/hookserver"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/intake"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/landing"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/llmusage"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/manifest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/oauth"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/onboarding"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/otelproxy"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/pendingaction"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/reports"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/reviewreport"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/settings"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/steward"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/steward/discussionanswer"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/supervisorpkg"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/supervisorwatch"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticketusage"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/usagemetrics"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/user"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/usersettings"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/variant"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/variantcompare"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/wireschema"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/worker"
)

// NewConfig returns the huma.Config shared by cmd/api and
// cmd/generate_openapi_spec.
//
// CreateHooks is cleared because huma.DefaultConfig registers a hook, at
// API-construction time, that adds its SchemaLinkTransformer -- clearing
// Transformers directly here wouldn't stick, since that hook repopulates it
// when humago.New runs. The transformer stamps a "$schema" field onto every
// response body that has a named schema, which is only useful to a generic
// JSON-Schema-aware client; this API's frontend isn't one, and the field
// would otherwise show up in every generated TS response interface.
//
// NonNilCollections is what lets RegisterAll's spec say a response's slices
// and maps are never null: it sends a nil one as empty.
//
// withoutErrorValues keeps a request's secrets out of the validation errors
// that answer it, for every route at once.
func NewConfig(title, version string) huma.Config {
	config := huma.DefaultConfig(title, version)
	config.CreateHooks = nil
	config.Transformers = append(config.Transformers, withoutErrorValues, wireschema.NonNilCollections)
	return config
}

// Deps are the handler instances RegisterAll mounts against api.
type Deps struct {
	TicketHandler            *ticket.Handler
	BoardStream              *ticket.BoardStream
	AgentHandler             *agent.Handler
	ConductorHandler         *conductor.Handler
	WorkerHandler            *worker.Handler
	SupervisorHandler        *supervisorwatch.Handler
	SupervisorPackageHandler *supervisorpkg.Handler
	ManifestHandler          *manifest.Handler
	OnboardingHandler        *onboarding.Handler
	FleetHandler             *fleetinstructions.Handler
	VariantHandler           *variant.Handler
	EvalSetHandler           *evalset.Handler
	HookHandler              *hookserver.Handler
	MessageHandler           *agentmsg.Handler
	ProposalHandler          *pendingaction.Handler
	StewardDigestHandler     *steward.DigestHandler
	DiscussionAnswerHandler  *discussionanswer.Handler
	SettingsHandler          *settings.Handler
	// LandingHandler is nil with the merge queue's routes not served.
	LandingHandler            *landing.Handler
	UserSettingsHandler       *usersettings.Handler
	UserTenantSettingsHandler *usersettings.TenantHandler
	UserHandler               *user.Handler
	OAuthHandler              *oauth.Handler
	WebhookHandler            *githubhook.Handler
	IntakeHandler             *intake.Handler
	GitHubAppHandler          *githubapp.Handler
	HarnessHandler            *harnessconfig.Handler
	UsageHandler              *llmusage.Handler
	ReviewReportHandler       *reviewreport.Handler
	ReportsHandler            *reports.Handler
	TicketUsageHandler        *ticketusage.Handler
	AttemptVerdictHandler     *attemptverdict.Handler
	VariantCompareHandler     *variantcompare.Handler
	OtelProxy                 *otelproxy.Handler
	UIUsageHandler            *usagemetrics.UIHandler
	ConsentHandler            *consent.Handler
	LegalDocs                 *legaldocs.Handler
	AttachmentHandler         *attachment.Handler
}

// RegisterAll mounts every operation slop-haus's API exposes, then makes the
// spec's response schemas say exactly where the server sends null.
func RegisterAll(api huma.API, deps Deps) {
	ticket.RegisterRoutes(api, deps.TicketHandler)
	ticket.RegisterBoardStream(api, deps.BoardStream)
	agent.RegisterRoutes(api, deps.AgentHandler)
	conductor.RegisterRoutes(api, deps.ConductorHandler)
	worker.RegisterRoutes(api, deps.WorkerHandler)
	supervisorwatch.RegisterRoutes(api, deps.SupervisorHandler)
	if deps.SupervisorPackageHandler != nil {
		supervisorpkg.RegisterRoutes(api, deps.SupervisorPackageHandler, huma.Middlewares{agent.BaseURLMiddleware})
	}
	manifest.RegisterRoutes(api, deps.ManifestHandler)
	onboarding.RegisterRoutes(api, deps.OnboardingHandler)
	fleetinstructions.RegisterRoutes(api, deps.FleetHandler)
	variant.RegisterRoutes(api, deps.VariantHandler)
	evalset.RegisterRoutes(api, deps.EvalSetHandler)
	hookserver.RegisterRoutes(api, deps.HookHandler)
	agentmsg.RegisterRoutes(api, deps.MessageHandler)
	pendingaction.RegisterRoutes(api, deps.ProposalHandler)
	steward.RegisterDigestRoutes(api, deps.StewardDigestHandler)
	discussionanswer.RegisterRoutes(api, deps.DiscussionAnswerHandler)
	settings.RegisterRoutes(api, deps.SettingsHandler)
	if deps.LandingHandler != nil {
		landing.RegisterRoutes(api, deps.LandingHandler)
	}
	usersettings.RegisterRoutes(api, deps.UserSettingsHandler)
	usersettings.RegisterTenantRoutes(api, deps.UserTenantSettingsHandler)
	user.RegisterRoutes(api, deps.UserHandler)
	oauth.RegisterRoutes(api, deps.OAuthHandler)
	githubhook.RegisterRoutes(api, deps.WebhookHandler)
	intake.RegisterRoutes(api, deps.IntakeHandler)
	githubapp.RegisterRoutes(api, deps.GitHubAppHandler)
	harnessconfig.RegisterRoutes(api, deps.HarnessHandler)
	llmusage.RegisterRoutes(api, deps.UsageHandler)
	reviewreport.RegisterRoutes(api, deps.ReviewReportHandler)
	reports.RegisterRoutes(api, deps.ReportsHandler)
	ticketusage.RegisterRoutes(api, deps.TicketUsageHandler)
	attemptverdict.RegisterRoutes(api, deps.AttemptVerdictHandler)
	variantcompare.RegisterRoutes(api, deps.VariantCompareHandler)
	otelproxy.RegisterRoutes(api, deps.OtelProxy)
	usagemetrics.RegisterUIRoutes(api, deps.UIUsageHandler)
	attachment.RegisterRoutes(api, deps.AttachmentHandler)
	attachment.RegisterAgentRoutes(api, deps.AttachmentHandler)
	registerPrivacy(api, deps)
	wireschema.ExactResponses(api.OpenAPI())
}

// registerPrivacy mounts go-privacy's consent and legal document routes.
// Consent is for anyone, signed in or not, and uses the signed-in user when
// there is one (OptionalUser, which also refuses a write made on a rejected
// credential rather than recording it as the anonymous visitor's). The
// documents are public text.
func registerPrivacy(api huma.API, deps Deps) {
	if err := deps.ConsentHandler.RegisterRoutes(api, "", &consent.Operations{
		Read: authn.OptionalUser(), Write: authn.OptionalUser(),
	}); err != nil {
		panic(err)
	}
	if err := deps.LegalDocs.RegisterRoutes(api, "", &legaldocs.Operations{Read: authn.Public()}); err != nil {
		panic(err)
	}
}

// BuildOpenAPI registers the full route set against a Handler with no
// Store behind it -- registering an operation only needs Go types and
// metadata, it never invokes the handler -- and returns the resulting
// OpenAPI document. Used by cmd/generate_openapi_spec and its tests,
// neither of which starts a real server or touches a database.
func BuildOpenAPI() *huma.OpenAPI {
	mux := http.NewServeMux()
	api := humago.New(mux, NewConfig("slop-haus API", "1.0.0"))

	RegisterAll(api, Deps{
		TicketHandler:             ticket.NewHandler(nil, slog.DiscardHandler),
		BoardStream:               ticket.NewBoardStream(nil, nil, slog.DiscardHandler),
		AgentHandler:              agent.NewHandler(&agent.HandlerDeps{}, slog.DiscardHandler),
		ConductorHandler:          conductor.NewHandler(nil, nil, nil, slog.DiscardHandler),
		WorkerHandler:             worker.NewHandler(nil, nil, nil, nil, slog.DiscardHandler),
		SupervisorHandler:         supervisorwatch.NewHandler(nil, slog.DiscardHandler),
		SupervisorPackageHandler:  supervisorpkg.NewHandler(nil, agent.BaseURLFrom, nil, slog.New(slog.DiscardHandler)),
		ManifestHandler:           manifest.NewHandler(nil, slog.DiscardHandler),
		OnboardingHandler:         onboarding.NewHandler(nil, slog.DiscardHandler),
		FleetHandler:              fleetinstructions.NewHandler(nil, nil, slog.DiscardHandler),
		VariantHandler:            variant.NewHandler(nil, slog.DiscardHandler),
		EvalSetHandler:            evalset.NewHandler(nil, slog.DiscardHandler),
		HookHandler:               hookserver.NewHandler(hookserver.Deps{}, slog.DiscardHandler),
		MessageHandler:            agentmsg.NewHandler(nil, slog.DiscardHandler),
		ProposalHandler:           pendingaction.NewHandler(nil, slog.DiscardHandler),
		StewardDigestHandler:      steward.NewDigestHandler(nil, slog.DiscardHandler),
		DiscussionAnswerHandler:   discussionanswer.NewHandler(nil, nil, slog.DiscardHandler),
		SettingsHandler:           settings.NewHandler(nil, slog.DiscardHandler),
		LandingHandler:            landing.NewHandler(nil, slog.DiscardHandler),
		UserSettingsHandler:       usersettings.NewHandler(nil, slog.DiscardHandler),
		UserTenantSettingsHandler: usersettings.NewTenantHandler(nil, slog.DiscardHandler),
		UserHandler:               user.NewHandler(nil, &user.CookieConfig{}, slog.DiscardHandler),
		OAuthHandler:              oauth.NewHandler(nil, slog.DiscardHandler),
		WebhookHandler:            githubhook.NewHandler(nil, slog.DiscardHandler),
		IntakeHandler:             intake.NewHandler(nil, slog.DiscardHandler),
		GitHubAppHandler:          githubapp.NewHandler(nil, slog.DiscardHandler),
		HarnessHandler:            harnessconfig.NewHandler(nil, slog.DiscardHandler),
		UsageHandler:              llmusage.NewHandler(nil, slog.DiscardHandler),
		ReviewReportHandler:       reviewreport.NewHandler(nil, slog.DiscardHandler),
		ReportsHandler:            reports.NewHandler(nil, slog.DiscardHandler),
		TicketUsageHandler:        ticketusage.NewHandler(nil, slog.DiscardHandler),
		AttemptVerdictHandler:     attemptverdict.NewHandler(nil, slog.DiscardHandler),
		VariantCompareHandler:     variantcompare.NewHandler(nil, slog.DiscardHandler),
		OtelProxy:                 new(otelproxy.Handler),
		ConsentHandler:            new(consent.Handler),
		LegalDocs:                 new(legaldocs.Handler),
		AttachmentHandler:         attachment.NewHandler(nil, slog.DiscardHandler),
	})
	return api.OpenAPI()
}
