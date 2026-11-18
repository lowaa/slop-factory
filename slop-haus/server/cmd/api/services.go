package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/go-auth/auth"
	"github.com/lowaa/slop-factory/go-auth/email"
	"github.com/lowaa/slop-factory/go-auth/login"
	"github.com/lowaa/slop-factory/go-auth/ratelimit"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/agent"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/attachment"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost/github"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/config"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/fleetinstructions"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/harnessconfig"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/landing"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/leaseconfig"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/logging"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/manifest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/metrics"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/oauth"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/pendingaction"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/reviewreport"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/settings"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/steward"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/steward/assist"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/steward/discussionanswer"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/steward/discussionapply"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/steward/discussionlabel"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/stewardwire"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/tenantstate"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/user"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/usersettings"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/variant"
)

// limiters builds each rate limiter's config from its policy, tracked by
// the metrics and logged through the api's handler.
type limiters struct {
	telemetry  *metrics.Metrics
	logHandler slog.Handler
}

func (l *limiters) limit(p config.RateLimit) *ratelimit.Config {
	l.telemetry.TrackLimiter(p.Name)
	return &ratelimit.Config{
		Name: p.Name, Limit: p.Limit, Window: p.Window, MaxKeys: config.LimiterMaxKeys,
		Logger: l.logHandler, Observer: l.telemetry,
	}
}

// services are the board's and the agents' services: everything the
// transports serve, bar the people signing in (see newUsers). None of them
// holds anything that needs closing.
type services struct {
	// hosts is the code hosts' registry. Its operations are set in board.go,
	// once the credential service they need exists.
	hosts              *codehost.Registry
	settings           *settings.Service
	stewardStore       *steward.Store
	userSettings       *usersettings.Service
	userTenantSettings *usersettings.TenantService
	tickets            *ticket.Service
	// ticketStore is the container's store, with the workspace and settled
	// hook. Landing and server merge claim through it: a bare store ignores
	// the manifest's shared paths and blocks on overlaps a worker may hold.
	ticketStore        *ticket.PostgresStore
	agents             *agent.Service
	conductors         *agent.ConductorService
	oauth              *oauth.Service
	harness            *harnessconfig.Service
	pending            *pendingaction.Service
	steward            *steward.Service
	stewardControls    *steward.Controls
	stewardAssist      *assist.Service
	discussionAnswers  *discussionanswer.Service
	discussionApply    *discussionapply.Service
	discussionLabeller *stewardwire.DiscussionSubmitter
	stewardDrafts      *assist.Store
	// mergeHook is told of approvals when the server merges (see
	// startServerMerge); nil when it does not. The runner it is bound to is
	// built after the services, so it is checked by serverCells, not
	// c.cells.
	mergeHook *Late[ticket.ApprovalHook]
	// queueHook enqueues approvals in the landing queue (see startLanding); nil
	// unless MERGE_QUEUE_APPROVAL_ENABLED is on.
	queueHook *Late[ticket.ApprovalHook]
	// setupCheck is the onboarding service, which needs the code host module
	// built after the ticket service.
	setupCheck *Late[ticket.SetupCheck]
	// discussionHook labels every discussion round; the steward is built after
	// the ticket service.
	discussionHook *Late[ticket.DiscussionHook]
	// provisionImport is the provisioning service, which is built from the
	// settings service.
	provisionImport *Late[settings.ProvisionEnabledHook]
	serverCells     lateCells
	// modules are the domains wired on top of the services above.
	modules *modules
}

func newServices(ctx context.Context, c *container, seams *serveSeams) (*services, error) {
	cfg, database, events, limits, users, logHandler := c.cfg, c.database, c.events, c.limits, c.users, c.logHandler
	proposalClock := seams.proposalClock
	s := &services{}
	// Settings is constructed first: the claim path and the Stop hook are
	// both gated on it, so it is a dependency of the ticket service rather
	// than a peer of it.
	if err := registerSettings(); err != nil {
		return nil, err
	}
	s.stewardStore = steward.NewStore(database)
	s.provisionImport = newLate[settings.ProvisionEnabledHook](&s.serverCells, "provisioning import")
	s.settings = settings.NewService(settings.NewPostgresStore(database), events, logHandler, settings.WithOwners(users),
		settings.WithProvisionEnabledHook(lateProvisionImport{s.provisionImport}))
	c.tenantCreated.Bind(settings.NewTenantStartingSettings(s.settings))
	s.userSettings = usersettings.NewService(usersettings.NewPostgresStore(database), usersettings.DefaultRegistry(), logHandler)
	s.userTenantSettings = usersettings.NewTenantService(usersettings.NewPostgresTenantStore(database), usersettings.DefaultRegistry(), logHandler)
	hosts := codeHosts(c.hostChanges)
	s.hosts = hosts
	ticketOpts := []ticket.ServiceOption{ticket.WithBulkMax(cfg.BulkMaxTickets), ticket.WithResumeAffinity(cfg.ResumeAffinity), ticket.WithCooldownPolicy(cfg.Cooldown),
		ticket.WithWorkspace(c.workspace), ticket.WithCodeHosts(hosts), ticket.WithEvents(events), ticket.WithTenantOwners(users), ticket.WithUserNames(userNameAdapter{users})}
	// The runners need the ticket service these hooks are built into, so each
	// hook is bound once its runner exists. Both are told of every approval and
	// the routing (landing.ApprovalRouting) leaves it to one of them.
	var approvalHooks fanoutApprovalHook
	if cfg.ServerMergeEnabled {
		s.mergeHook = newLate[ticket.ApprovalHook](&s.serverCells, "server merge approval hook")
		approvalHooks = append(approvalHooks, lateApprovalHook{s.mergeHook})
	}
	if cfg.MergeQueueEnabled && cfg.MergeQueueApprovalEnabled {
		s.queueHook = newLate[ticket.ApprovalHook](&s.serverCells, "landing queue approval hook")
		approvalHooks = append(approvalHooks, lateApprovalHook{s.queueHook})
	}
	if len(approvalHooks) > 0 {
		ticketOpts = append(ticketOpts, ticket.WithApprovalHook(approvalHooks))
	}
	s.discussionHook = newLate[ticket.DiscussionHook](&s.serverCells, "discussion label hook")
	ticketOpts = append(ticketOpts, ticket.WithDiscussionHook(lateDiscussionHook{s.discussionHook}))
	s.setupCheck = newLate[ticket.SetupCheck](&s.serverCells, "onboarding setup check")
	ticketOpts = append(ticketOpts, ticket.WithSetupCheck(lateSetupCheck{s.setupCheck}))
	// The ticket service resolves an agent's owner with a resolver built from
	// the agent service, which is built from this one (see lateDeps).
	late := newLateDeps()
	s.harness = harnessconfig.NewService(harnessconfig.NewPostgresStore(database), events, logHandler,
		harnessconfig.WithVariants(variant.NewPostgresStore(database, agent.VariantWriter{})),
		harnessconfig.WithOverrideGate(&late.gate))
	ticketOpts = append(ticketOpts, ticket.WithOwnerResolver(&late.resolver),
		ticket.WithConfigResolver(leaseconfig.NewResolver(s.harness, fleetinstructions.NewPostgresStore(database))))
	s.ticketStore = c.ticketStore
	s.tickets = ticket.NewService(c.ticketStore, logHandler, ticketOpts...)
	stewardSettings := steward.NewTenantSettings(s.settings)

	// The agents, OAuth, proposal and ticket services depend on each other in
	// circles (see lateDeps), so each is handed an adapter that is bound once
	// the service it forwards to exists.
	s.stewardDrafts = assist.NewStore(database)
	pendingOpts := []pendingaction.Option{
		pendingaction.WithApproval(s.tickets, &late.approval),
		pendingaction.WithEvents(events),
		pendingaction.WithRoles(users),
		pendingaction.WithMergeCloser(s.tickets),
		pendingaction.WithManifests(manifest.NewEditor(c.manifestStore)),
		pendingaction.WithAutoApprove(&late.resolver, newAutoApproveSettings(s.userTenantSettings)),
		pendingaction.WithStewardOwner(newStewardOwner(database)),
		pendingaction.WithDecisionRecorder(steward.NewDecisionRecorder(s.stewardStore)),
		pendingaction.WithAssessments(newAssessmentReader(s.stewardStore, stewardSettings)),
		pendingaction.WithDrafts(&draftReader{store: s.stewardDrafts}),
	}
	if proposalClock != nil {
		pendingOpts = append(pendingOpts, pendingaction.WithClock(proposalClock))
	}
	s.pending = pendingaction.NewService(pendingaction.NewPostgresStore(database), cfg.Proposals, logHandler, pendingOpts...)
	s.agents = agent.NewService(agent.NewPostgresStore(database), s.tickets, &late.grants, logHandler, agent.WithProposalVoider(s.pending), agent.WithMembers(users), agent.WithSlotCredentials(&late.grants))
	stewardDeps := &stewardwire.Deps{Database: database, Logger: logHandler, Pending: s.pending, Tickets: s.tickets,
		Agents: s.agents, Hosts: hosts, Settings: stewardSettings, Store: s.stewardStore,
		Instructions:  fleetinstructions.NewPostgresStore(database),
		HarnessConfig: s.harness,
		Tenants:       tenantstate.Active(database.Global())}
	s.steward = stewardwire.NewService(stewardDeps)
	s.stewardAssist = stewardwire.NewAssist(stewardDeps, s.stewardDrafts)
	discussionLabels := discussionlabel.NewStore(database)
	// The label service is built after the apply service it informs, which
	// needs the answers: assessing now reaches it through this cell.
	assessNow := &lateLabeller{}
	s.discussionAnswers = discussionanswer.NewService(logHandler, &discussionanswer.Deps{
		Labels:      discussionLabels,
		Discussions: stewardwire.NewDiscussionReader(s.tickets, discussionLabels, stewardwire.NewDiscussionHeads(stewardDeps)),
		Assessor:    stewardwire.NewDiscussionAssessor(stewardDeps),
		Settings:    stewardSettings,
		Labeller:    assessNow,
		Decisions:   discussionanswer.NewStore(database),
		Poster:      pendingaction.NewDiscussionPoster(s.tickets).WithServerOwners(newStewardOwner(database)),
	})
	s.discussionApply = discussionapply.NewService(logHandler, &discussionapply.Deps{
		Settings:  stewardSettings,
		Schedules: discussionapply.NewStore(database),
		Answers:   s.discussionAnswers,
		Decisions: discussionanswer.NewStore(database),
		Labels:    discussionLabels,
		Roster:    stewardDeps.Tenants,
	})
	// The steward agent is the classifier, and it runs asynchronously: the
	// service queues each round as pending and the agent completes it through
	// steward_label_discussion. The apply service hears of every completed
	// label, so a routine one is scheduled as soon as it is made.
	discussionRounds := stewardwire.NewDiscussionReader(s.tickets, discussionLabels, stewardwire.NewDiscussionHeads(stewardDeps))
	labelService := discussionlabel.NewService(logHandler, nil, stewardwire.NewDiscussionAssessor(stewardDeps), stewardSettings, discussionLabels, nil).
		WithObserver(s.discussionApply).WithRoster(stewardDeps.Tenants)
	discussionQueue := stewardwire.NewDiscussionQueue(discussionRounds, labelService)
	labelService.WithRounds(discussionQueue)
	s.discussionApply.Expirer = labelSweeper{labelService}
	assessNow.svc = labelService
	s.discussionLabeller = stewardwire.NewDiscussionSubmitter(discussionRounds, labelService)
	s.discussionHook.Bind(discussionQueue)
	stewardComment := &stewardComments{tickets: s.tickets}
	s.steward.Applier().WithComments(stewardComment, stewardLinks(cfg.PublicBaseURL))
	s.stewardControls = steward.NewControls(logHandler, s.stewardStore, users, &stewardRevert{pending: s.pending}, stewardComment, events, nil)
	// users is built first (agents need it for members), so archiving's
	// switch-off of a tenant's agents is bound here.
	c.tenantAgents.Bind(s.agents)
	s.conductors = agent.NewConductorService(s.agents, users, logHandler)
	late.gate.cell.Bind(s.conductors)
	s.oauth = oauth.NewService(oauth.NewPostgresStore(database), s.agents, agentOAuthConfig(cfg, limits), events, logHandler, oauth.WithGrantsEnded(s.agents))
	late.oauth.Bind(s.oauth)
	// Owner-bound sessions edit blockers on any ticket.
	late.owners.Bind(agent.NewOwnerResolver(s.agents, s.conductors, users, s.oauth, users))
	if err := late.verify(); err != nil {
		return nil, err
	}

	var err error
	if s.modules, err = newModules(ctx, c, s, seams.changes); err != nil {
		return nil, err
	}
	if err := c.cells.verifyAll(); err != nil {
		return nil, err
	}
	return s, nil
}

// agentOAuthConfig is the agents' OAuth server's config: its policy (see
// config.AgentOAuth), issued as the board.
func agentOAuthConfig(cfg *config.Config, limits *limiters) *oauth.Config {
	policy := config.AgentOAuth()
	return &oauth.Config{
		Issuer:              cfg.PublicBaseURL,
		AccessTTL:           policy.AccessTTL,
		RefreshTTL:          policy.RefreshTTL,
		CodeTTL:             policy.CodeTTL,
		AuthRequestTTL:      policy.AuthRequestTTL,
		DeviceTTL:           policy.DeviceTTL,
		PollInterval:        policy.PollInterval,
		RegisterLimit:       limits.limit(policy.RegisterLimit),
		DeviceLookupLimit:   limits.limit(policy.DeviceLookupLimit),
		DeviceStartLimit:    limits.limit(policy.DeviceStartLimit),
		SlotCredentialLimit: limits.limit(policy.SlotCredentialLimit),
		MaxUnclaimedClients: policy.MaxUnclaimedClients,
	}
}

type usersDeps struct {
	cfg      *config.Config
	database *db.DB
	events   *audit.Recorder
	limits   *limiters
	late     *userLate
}

// userLate is what the user service waits for, built before it so the
// container can bind it once the agent service exists. The user service is
// built first because the agent service needs it for members.
type userLate struct {
	cells        *lateCells
	tenantAgents *Late[user.TenantAgents]
	// tenantCreated starts a new tenant's settings; the settings service is
	// built from the user service.
	tenantCreated *Late[user.TenantCreatedHook]
}

func newUserLate() *userLate {
	cells := &lateCells{}
	return &userLate{
		cells:         cells,
		tenantAgents:  newLate[user.TenantAgents](cells, "tenant agents"),
		tenantCreated: newLate[user.TenantCreatedHook](cells, "tenant created hook"),
	}
}

// newUsers is the service people sign up and sign in through, with the
// mail outbox and the login providers behind it, each pushed onto cl. They
// are closed only once the servers have stopped taking requests, so a
// sign-up in the last moments still gets its link.
func newUsers(ctx context.Context, deps *usersDeps, cl *closers, logHandler slog.Handler) (*user.Service, error) {
	cfg := deps.cfg
	outbox := user.NewOutbox(newMailer(cfg, logHandler, logging.DevMailHandler(os.Stderr)), outboxConfig(), logHandler)
	cl.push("outbox", outbox.Close)

	providers, closeProviders, err := loginProviders(ctx, cfg, login.NewIssuerBindings(deps.database.Global()))
	if err != nil {
		return nil, err
	}
	cl.push("login providers", closeProviders)

	// Keyed from the JWT secret, so there is no second secret to manage --
	// but through the codec's own HKDF label, so the key it MACs with is
	// not the key tokens are signed with.
	loginStates, err := login.NewLoginStateCodec([]byte(cfg.JWTSecret))
	if err != nil {
		return nil, err
	}
	// The same for the sessions' CSRF tokens, under a label of their own:
	// a CSRF token is a MAC a page is handed, and must be worth nothing as
	// a login state's MAC or a token signature.
	csrfTokens, err := auth.NewCSRFTokens([]byte(cfg.JWTSecret))
	if err != nil {
		return nil, err
	}

	svc := user.NewService(&user.Deps{
		Store:       user.NewPostgresStore(deps.database, user.WithTenantCreatedHook(lateTenantCreated{deps.late.tenantCreated}, logHandler)),
		Signer:      auth.NewSigner([]byte(cfg.JWTSecret), cfg.AccessTokenTTL, cfg.RefreshTokenTTL),
		Mail:        outbox,
		Providers:   providers,
		LoginStates: loginStates,
		CSRF:        csrfTokens,
		Audit:       deps.events,
		Agents:      lateTenantAgents{deps.late.tenantAgents},
	}, usersConfig(cfg, deps.limits), logHandler)
	return svc, nil
}

// usersConfig is the user service's config: the sign-in settings, and the
// sign-in limits (see config.SignInLimits).
func usersConfig(cfg *config.Config, limits *limiters) *user.Config {
	signIn := cfg.SignInLimits()
	return &user.Config{
		PublicBaseURL:          cfg.PublicBaseURL,
		ActivationTTL:          cfg.ActivationTTL,
		ResetTTL:               cfg.ResetTTL,
		InviteTTL:              cfg.InviteTTL,
		RegistrationOpen:       cfg.RegistrationOpen,
		PasswordSignInDisabled: !cfg.PasswordSignIn,
		LoginLimit:             limits.limit(signIn.Login),
		MailLimit:              limits.limit(signIn.Mail),
		LoginClientLimit:       limits.limit(signIn.LoginClient),
		MailClientLimit:        limits.limit(signIn.MailClient),
		InviteLimit:            limits.limit(signIn.Invite),
		TenantCreateLimit:      limits.limit(signIn.TenantCreate),
		TenantOwnerLimit:       cfg.TenantOwnerLimit,
		TenantOwnerTotalLimit:  cfg.TenantOwnerTotalLimit,
	}
}

func outboxConfig() *user.OutboxConfig {
	policy := config.MailOutbox()
	return &user.OutboxConfig{QueueSize: policy.QueueSize, SendTimeout: policy.SendTimeout}
}

// signInMethods are the method label's values: password, and each
// provider configured (see metrics.Options).
func signInMethods(cfg *config.Config) []string {
	methods := []string{config.PasswordMethod}
	if cfg.GitHub != nil {
		methods = append(methods, login.GitHubProviderName)
	}
	if cfg.OIDC != nil {
		methods = append(methods, cfg.OIDC.ProviderName)
	}
	return methods
}

// loginProviders builds the login providers people may sign in with besides
// a password, binding each one's name to its issuer first: a name that
// already belongs to another issuer refuses to start rather than letting
// the new issuer's subjects sign in as the old one's (see
// login.IssuerBindings).
// The returned func stops the OIDC provider's background discovery.
func loginProviders(ctx context.Context, cfg *config.Config, bindings *login.IssuerBindings) (*login.Providers, func(), error) {
	var list []login.AuthProvider
	closeFn := func() {}
	if g := cfg.GitHub; g != nil {
		if err := bindings.Register(ctx, login.GitHubProviderName, login.GitHubIssuer); err != nil {
			return nil, nil, err
		}
		list = append(list, login.NewGitHubAuthProvider(g.ClientID, g.ClientSecret,
			cfg.PublicBaseURL+user.CallbackPath(login.GitHubProviderName), login.DefaultGitHubEndpoints))
	}
	if o := cfg.OIDC; o != nil {
		if err := bindings.Register(ctx, o.ProviderName, login.NormalizeIssuer(o.IssuerURL)); err != nil {
			return nil, nil, err
		}
		// Discovery runs in the background: the issuer may start after the
		// api (Dex in a compose stack), and sign-ins with it answer
		// "unavailable" until it is up rather than the api refusing to start.
		oidc := login.NewOIDCAuthProvider(&login.OIDCOptions{
			IssuerURL:    o.IssuerURL,
			ClientID:     o.ClientID,
			ClientSecret: o.ClientSecret,
			CallbackURL:  cfg.PublicBaseURL + user.CallbackPath(o.ProviderName),
			Name:         o.ProviderName,
			DisplayName:  o.DisplayName,
		}, slog.Default().Handler())
		oidc.Start()
		closeFn = oidc.Close
		list = append(list, oidc)
	}
	providers := login.NewProviders(list...)
	slog.Info("sign-in methods", "password", cfg.PasswordSignIn, "providers", providers.Names())
	return providers, closeFn, nil
}

// newMailer is the configured email.Sender. The log backend writes whole
// messages, which carry links that sign someone in, so it gets the dev
// sink's handler rather than the policed one, and says so through the
// policed one at startup.
func newMailer(cfg *config.Config, logHandler, devSink slog.Handler) email.Sender {
	if cfg.Email.Backend == "smtp" {
		return email.New(&cfg.Email, slog.New(logHandler))
	}
	slog.New(logHandler).Warn("EMAIL_BACKEND=log: emails, and the links in them, are written to stderr; for local development only")
	return email.New(&cfg.Email, slog.New(devSink))
}

// lateDeps are the services' circular dependencies, each a Late cell bound
// once the service it forwards to exists:
//   - the agent service checks tokens and revokes grants with the OAuth
//     service, which is built from the agent service (consent picks agents);
//     (one cell serves the agent and pending services, which both use it);
//   - a proposal's approval asks the OAuth service whether its proposer still
//     has a grant, and the agent service voids proposals with the pending
//     one, so the pending service is built before OAuth;
//   - the ticket service resolves an agent's owner with a resolver built from
//     the agent service, which releases leases through the ticket service.
//
// cells.verifyAll, called once wiring is done, refuses to start if one was
// never bound: an adapter left unbound would otherwise fail only at the first
// request that reaches it -- and the owner one not at all, as the ticket
// service would quietly treat every agent as a worker.
type lateDeps struct {
	cells    lateCells
	oauth    *Late[oauthGrants]
	owners   *Late[ticket.OwnerResolver]
	grants   agentGrants
	approval approvalGrants
	resolver ownerResolver
	gate     overrideGate
}

func newLateDeps() *lateDeps {
	l := &lateDeps{}
	l.oauth = newLate[oauthGrants](&l.cells, "oauth service")
	l.owners = newLate[ticket.OwnerResolver](&l.cells, "owner resolver")
	l.grants.oauth = l.oauth
	l.approval.oauth = l.oauth
	l.resolver.resolver = l.owners
	l.gate.cell = newLate[harnessconfig.OverrideGate](&l.cells, "harness override gate")
	return l
}

// oauthGrants is what the agent and pending services use of the OAuth
// service, which they are built before.
type oauthGrants interface {
	agent.Grants
	agent.SlotCredentials
	pendingaction.Grants
}

func (l *lateDeps) verify() error { return l.cells.verifyAll() }

// agentGrants is agent.Grants and agent.SlotCredentials for an OAuth service
// not yet built.
type agentGrants struct{ oauth *Late[oauthGrants] }

func (g *agentGrants) Authenticate(ctx context.Context, accessToken string) (*agent.Access, error) {
	return g.oauth.Get().Authenticate(ctx, accessToken)
}

func (g *agentGrants) RevokeAgentGrants(ctx context.Context, tenantID, agentID uuid.UUID) error {
	return g.oauth.Get().RevokeAgentGrants(ctx, tenantID, agentID)
}

func (g *agentGrants) IssueSlotCredential(ctx context.Context, tenantID, supervisorID, slotID uuid.UUID) (*agent.SlotCredential, error) {
	return g.oauth.Get().IssueSlotCredential(ctx, tenantID, supervisorID, slotID)
}

func (g *agentGrants) SlotApprover(ctx context.Context, tenantID, supervisorID uuid.UUID) (*uuid.UUID, error) {
	return g.oauth.Get().SlotApprover(ctx, tenantID, supervisorID)
}

func (g *agentGrants) RevokeAgentCredential(ctx context.Context, tenantID, agentID uuid.UUID, refreshToken string) error {
	return g.oauth.Get().RevokeAgentCredential(ctx, tenantID, agentID, refreshToken)
}

// approvalGrants is pendingaction.Grants for an OAuth service not yet built.
type approvalGrants struct{ oauth *Late[oauthGrants] }

func (g *approvalGrants) HasFullAccessGrant(ctx context.Context, tenantID, agentID uuid.UUID) (bool, error) {
	return g.oauth.Get().HasFullAccessGrant(ctx, tenantID, agentID)
}

// overrideGate is harnessconfig.OverrideGate for a conductor service not yet
// built: the harness service is built first, as the lease config resolver
// needs it, and the conductors are built from the agents the tickets reach.
type overrideGate struct {
	cell *Late[harnessconfig.OverrideGate]
}

func (g *overrideGate) MayChangeConfig(ctx context.Context, who *authn.User, agentID uuid.UUID) (bool, error) {
	return g.cell.Get().MayChangeConfig(ctx, who, agentID)
}

// ownerResolver is ticket.OwnerResolver for a resolver not yet built.
type ownerResolver struct{ resolver *Late[ticket.OwnerResolver] }

func (o *ownerResolver) ResolveOwner(ctx context.Context, agentID uuid.UUID) (*ticket.OwnerBound, error) {
	return o.resolver.Get().ResolveOwner(ctx, agentID)
}

// codeHosts is the hosts this server understands the changes of. GitHub's
// operations (codehost.Changes) are bound where the credential service exists
// (see board.go).
func codeHosts(githubChanges *Late[codehost.Changes]) *codehost.Registry {
	hosts := codehost.NewRegistry()
	hosts.RegisterLate(github.New(), githubChanges.Get)
	return hosts
}

// lateApprovalHook is ticket.ApprovalHook for a hook not built yet.
type lateApprovalHook struct{ hook *Late[ticket.ApprovalHook] }

func (l lateApprovalHook) Approved(ctx context.Context, a *ticket.Approval) error {
	return l.hook.Get().Approved(ctx, a)
}

// fanoutApprovalHook tells every hook of an approval; one failing does not
// stop the rest.
type fanoutApprovalHook []ticket.ApprovalHook

func (f fanoutApprovalHook) Approved(ctx context.Context, a *ticket.Approval) error {
	var errs []error
	for _, h := range f {
		errs = append(errs, h.Approved(ctx, a))
	}
	return errors.Join(errs...)
}

// lateDiscussionHook is ticket.DiscussionHook for a steward not yet built.
type lateDiscussionHook struct{ hook *Late[ticket.DiscussionHook] }

func (l lateDiscussionHook) Entered(ctx context.Context, e *ticket.DiscussionEntry) error {
	return l.hook.Get().Entered(ctx, e)
}

// lateSetupCheck is ticket.SetupCheck for an onboarding service not yet built.
type lateSetupCheck struct{ check *Late[ticket.SetupCheck] }

func (l lateSetupCheck) SetupGap(ctx context.Context) string { return l.check.Get().SetupGap(ctx) }

// lateTenantCreated is user.TenantCreatedHook for a settings service not yet built.
type lateTenantCreated struct{ hook *Late[user.TenantCreatedHook] }

func (l lateTenantCreated) TenantCreated(ctx context.Context, tenantID, ownerID uuid.UUID) error {
	return l.hook.Get().TenantCreated(ctx, tenantID, ownerID)
}

// lateTenantAgents is user.TenantAgents for an agent service not yet built.
type lateTenantAgents struct{ agents *Late[user.TenantAgents] }

func (l lateTenantAgents) DisableTenantAgents(ctx context.Context, tenantID uuid.UUID) error {
	return l.agents.Get().DisableTenantAgents(ctx, tenantID)
}

var registerSettingsOnce = sync.OnceValue(func() error {
	if err := settings.Register(steward.SettingSpecs()...); err != nil {
		return fmt.Errorf("register steward settings: %w", err)
	}
	if err := settings.Register(landing.SettingSpecs()...); err != nil {
		return fmt.Errorf("register landing settings: %w", err)
	}
	if err := settings.Register(attachment.SettingSpecs()...); err != nil {
		return fmt.Errorf("register attachment settings: %w", err)
	}
	if err := settings.Register(reviewreport.SettingSpecs()...); err != nil {
		return fmt.Errorf("register review report settings: %w", err)
	}
	return nil
})

// registerSettings declares the settings other packages own. The registry is
// process-wide, and a process may build the services more than once (tests).
func registerSettings() error { return registerSettingsOnce() }

// lateProvisionImport is settings.ProvisionEnabledHook for a provisioning
// service not yet built.
type lateProvisionImport struct {
	h *Late[settings.ProvisionEnabledHook]
}

func (l lateProvisionImport) ImportAll(ctx context.Context) (int, error) {
	return l.h.Get().ImportAll(ctx)
}

// lateLabeller is the label service, bound once it exists.
// labelSweeper is what the periodic sweep runs for labels: the timeout of
// pending ones, then the queueing of rounds that never got one.
type labelSweeper struct{ svc *discussionlabel.Service }

func (l labelSweeper) ExpireOnce(ctx context.Context) error {
	return errors.Join(l.svc.ExpireOnce(ctx), l.svc.SweepUnlabelled(ctx))
}

type lateLabeller struct{ svc *discussionlabel.Service }

func (l *lateLabeller) AssessNow(ctx context.Context, q *discussionlabel.Question) (*discussionlabel.Record, error) {
	return l.svc.AssessNow(ctx, q)
}
