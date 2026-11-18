package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/netip"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/go-auth/clientip"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/agent"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/apiserver"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/change"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost/github"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost/githubapp"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/githubhook"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/landing"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/manifest"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/mcpserver"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/metrics"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/reqtrace"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/supervisorpkg"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/usagemetrics"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/user"
)

// board is what the board's listener serves -- the board's API, the agent
// REST and hook routes, OAuth and MCP -- and the waits its shutdown has to
type board struct {
	handler http.Handler
	// waits are the worker-poll waiter and the conductors' polls and streams.
	waits drain
	// changes is the change flow, which the closing rule's reconciler runs on.
	changes *change.Service
	// codeHost is the tenants' GitHub App credentials, whose webhooks are
	// reconciled at startup.
	codeHost *githubapp.Service
	// promotions are promotions still being carried to the code host after
	// their response; shutdown waits for them (each is time-bounded) so a
	// deploy does not leave pull requests half retargeted.
	promotions interface{ WaitForCarries() }
	// telemetry is the server's metrics; nil where none is kept.
	telemetry *metrics.Metrics
}

// drain ends every wait held open, at shutdown.
type drain []interface{ Close() }

func (d drain) Close() {
	for _, w := range d {
		w.Close()
	}
}

type boardDeps struct {
	*container
	svcs *services
}

func newBoard(ctx context.Context, deps *boardDeps, logHandler slog.Handler) (*board, error) {
	cfg, svcs, users, events := deps.cfg, deps.svcs, deps.users, deps.events
	logger := slog.New(logHandler)
	// One authenticator for every agent transport -- the huma middleware
	// (hooks, agent REST) and the MCP endpoint -- so an OAuth access token
	// means the same thing wherever it is presented.
	authenticate := func(ctx context.Context, token string) (authn.Identity, error) {
		return svcs.agents.Authenticate(ctx, token)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", healthzHandler(deps.database))
	// Beside /healthz rather than a huma operation: it is for deploys, not
	// the frontend's generated client. Public, like the page itself, and
	// says nothing the served JavaScript doesn't already.
	mux.HandleFunc("GET /api/version", versionHandler(cfg.BuildTag))

	api := humago.New(mux, apiserver.NewConfig("slop-haus API", "1.0.0"))
	usage := usagemetrics.New()
	if telemetry := boardTelemetry(deps); telemetry != nil {
		if err := telemetry.Register(usage.Collectors()...); err != nil {
			return nil, err
		}
	}
	// First, so a request that a later middleware refuses is still
	// attributed to the operation it was for.
	api.UseMiddleware(usagemetrics.Operation)

	// Auth is installed before any route is registered, so a route is
	// refused before its handler runs and a handler never decides whether
	// an unauthenticated caller is allowed.
	//
	// In order: the client's address (the sign-in limits count per address
	// and client, and must see requests the auth middlewares go on to
	// refuse); DefaultDeny, so a route that chose no marker is closed
	// rather than open; the Origin check on public writes; then the agent,
	// user and admin checks the markers ask for -- the user check with the
	// session writes' Origin and CSRF token checks.
	resolver, err := clientip.NewResolver(cfg.TrustedProxies)
	if err != nil {
		return nil, err
	}
	browserGuard, err := authn.NewBrowserGuard(cfg.PublicBaseURL, users.ValidCSRF, events)
	if err != nil {
		return nil, err
	}
	api.UseMiddleware(resolver.Middleware())
	api.UseMiddleware(authn.DefaultDeny(api, logger))
	api.UseMiddleware(browserGuard.PublicWrites(api))
	api.UseMiddleware(authn.HumaMiddleware(api, authenticate, events, logger))
	api.UseMiddleware(authn.UserMiddleware(api, users.VerifyAccess, browserGuard, logger))
	// Archived first, then the tenant check: see authn.ArchivedTenantGuard.
	api.UseMiddleware(authn.ArchivedTenantGuard(api, users.TenantArchived, user.TenantMovingOperations, logger))
	api.UseMiddleware(authn.TenantGuard(api, user.TenantMovingOperations, logger))
	api.UseMiddleware(authn.AdminMiddleware(api, users.IsAdmin, events, logger))
	// After the user check, which resolves the user it reads. Scoped to the
	// consent routes: see privacy.Services.Middleware.
	api.UseMiddleware(lookup[*privacyModule](svcs.modules).Middleware())

	mods := svcs.modules
	workspace, codeHostCredentials, changes := lookup[*workspaceModule](mods).Service, lookup[*codeHostModule](mods).Credentials, lookup[*codeHostModule](mods).Changes
	routes := apiserver.Deps{}
	mods.Routes(&routes)
	apiserver.RegisterAll(api, routes)
	operations := []string{}
	for _, item := range api.OpenAPI().Paths {
		for _, op := range []*huma.Operation{item.Get, item.Put, item.Post, item.Delete, item.Options, item.Head, item.Patch, item.Trace} {
			if op != nil && op.OperationID != "" {
				operations = append(operations, op.OperationID)
			}
		}
	}
	usage.SetOperations(operations)

	// The OAuth endpoints and their discovery documents, on the mux rather
	// than as huma operations: see internal/oauth/http.go.
	lookup[*oauthModule](mods).Mount(mux, func(r *http.Request) netip.Addr {
		return resolver.Resolve(r.RemoteAddr, r.Header.Values("X-Forwarded-For"))
	})

	// MCP is mounted directly on the mux rather than registered as a huma
	// operation: it has its own request framing and is not part of the
	// OpenAPI surface the frontend is generated from.
	//
	// One handler, mounted at both spellings. Two would be two independent
	// mcp.Servers with separate session state, so a client that started on
	// /mcp and was then redirected (or normalized) to /mcp/ would present a
	// session id the other handler has never seen.
	landed := lookup[*landingModule](mods)
	// A nil *Metrics inside the interface would not be a nil Refusals.
	var refusals mcpserver.Refusals
	if deps.limits != nil && deps.limits.telemetry != nil {
		refusals = deps.limits.telemetry
	}
	// A nil *Service inside the interface would not be a nil Attachments.
	var attachments mcpserver.Attachments
	if svc := lookup[*attachmentModule](mods).Handler.Service(); svc != nil {
		attachments = svc
	}
	mcpHandler := mcpserver.NewHTTPHandler(mcpserver.Deps{
		Tickets:     svcs.tickets,
		Messages:    lookup[*messagesModule](mods).Service,
		Sessions:    lookup[*sessionModule](mods).Service,
		Agents:      svcs.agents,
		Roles:       lookup[*agentModule](mods).Roles,
		Proposers:   svcs.agents,
		Stewards:    svcs.agents,
		Assessor:    svcs.steward,
		Drafter:     svcs.stewardAssist,
		Labeller:    svcs.discussionLabeller,
		Drafts:      &draftReader{store: svcs.stewardDrafts},
		Workspace:   workspace,
		Manifests:   workspace,
		Batches:     landing.NewBatchStore(deps.database),
		Proposals:   svcs.pending,
		Usage:       lookup[*llmUsageModule](mods).Service,
		Changes:     changes,
		Lander:      landed.Lander(),
		QueueMode:   landed.QueueMode(),
		Events:      events,
		Refusals:    refusals,
		Attachments: attachments,
		Reviews:     lookup[*reviewReportModule](mods).Service,
		ToolUsage:   usage,
		BoardURL:    cfg.PublicBaseURL,

		Discussions: ticket.NewPostgresAttemptDiscussions(deps.database),
	}, authenticate, svcs.oauth.ResourceMetadataURL(), logger)
	mux.Handle("/mcp", mcpHandler)
	mux.Handle("/mcp/", mcpHandler)

	// Outermost on the board, so every route continues the browser's trace.
	return &board{handler: usage.Middleware(reqtrace.Middleware(mux, logHandler)), waits: drain{mods}, changes: changes, codeHost: codeHostCredentials, promotions: lookup[*ticketModule](mods).Handler, telemetry: boardTelemetry(deps)}, nil
}

// versionHandler reports the build tag. Uncached: a deploy polls it to see
// the new version take over, which a cached answer would hide.
func versionHandler(buildTag string) http.HandlerFunc {
	body, _ := json.Marshal(map[string]string{"version": buildTag})
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(body)
	}
}

func healthzHandler(database *db.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := database.Ping(r.Context()); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}
}

// manifestRemotes is the workspace manifest's repositories, for the GitHub
// App's verify to compare with what the installation can see.
type manifestRemotes struct{ svc *manifest.Service }

func (m manifestRemotes) Remotes(ctx context.Context) ([]string, error) {
	declared, err := m.svc.Get(ctx)
	if err != nil || declared == nil {
		return nil, err
	}
	remotes := make([]string, 0, len(declared.Repos))
	for _, r := range declared.Repos {
		remotes = append(remotes, r.Remote)
	}
	return remotes, nil
}

// githubAppCredentials is the tenants' GitHub App credentials: off (no
// keyring, nothing stored) unless CREDENTIAL_ENCRYPTION_KEY is set.
func githubAppCredentials(c *container, workspace *manifest.Service) (*githubapp.Service, *githubapp.Client, error) {
	cfg, database := c.cfg, c.database
	client, err := githubapp.NewClient(&githubapp.ClientOptions{BaseURL: cfg.GitHubAPIBaseURL})
	if err != nil {
		return nil, nil, err
	}
	store := githubapp.NewPostgresStore(database)
	// The App's webhook needs both the door to point GitHub at and the key to
	// seal its secret with.
	var webhook *githubapp.WebhookConfigurer
	if cfg.Webhook != nil && c.keyring != nil {
		if webhook, err = githubapp.NewWebhookConfigurer(&githubapp.WebhookOptions{
			Store: store, Keyring: c.keyring, Client: client, Logger: c.logHandler, PublicBaseURL: cfg.Webhook.PublicBaseURL,
		}); err != nil {
			return nil, nil, err
		}
	}
	svc, err := githubapp.NewService(&githubapp.Options{
		Store: store, Keyring: c.keyring, Client: client, Owners: c.users,
		Manifest: manifestRemotes{workspace}, Events: c.events, Logger: c.logHandler,
		WebBaseURL: cfg.GitHubWebBaseURL, BoardBaseURL: cfg.PublicBaseURL,
		Webhook: webhook, Deliveries: appDeliveries{githubhook.NewPostgresStore(database)},
	})
	if err != nil {
		return nil, nil, err
	}
	return svc, client, nil
}

// changeService is the change flow: slop-haus opening, checking and merging
// a ticket's change on GitHub with the tenant's App credential. GitHub's
// operations are given to the registry here, the one place that holds both
// the client and the credential service; everything else sees only
// codehost.Changes.
func changeService(c *container, svcs *services, client *githubapp.Client, creds codehost.Credentials, changes codehost.Changes) (*change.Service, error) {
	cfg, database, events := c.cfg, c.database, c.events
	if changes == nil {
		var err error
		changes, err = github.NewChanges(&github.ChangesOptions{Client: client, Credentials: creds})
		if err != nil {
			return nil, err
		}
	}
	c.hostChanges.Bind(changes)
	return change.NewService(&change.Options{
		Hosts:     svcs.hosts,
		Tickets:   svcs.tickets,
		Workspace: manifest.NewPolicy(c.manifestStore),
		Store:     change.NewPostgresStore(database),
		Names:     changeNames{agents: svcs.agents, users: user.NewPostgresStore(database)},
		Proposals: svcs.pending,
		Events:    events,
		BoardURL:  cfg.PublicBaseURL,
		Detect:    codehost.LineNumberChangeDetectionStrategy{},
		Trees:     changes,
		Logger:    c.logHandler,
	})
}

// changeNames names the people in a change's text: agents by their name, people by their display name.
type changeNames struct {
	agents *agent.Service
	users  *user.PostgresStore
}

func (n changeNames) Agent(ctx context.Context, id uuid.UUID) (string, error) {
	return n.agents.Name(ctx, id)
}

func (n changeNames) User(ctx context.Context, id uuid.UUID) (string, error) {
	u, err := n.users.FindByID(ctx, id)
	if err != nil {
		return "", err
	}
	return u.DisplayName, nil
}

// appDeliveries is what the App's webhook delivered, read for the status view.
type appDeliveries struct{ store *githubhook.PostgresStore }

func (a appDeliveries) Latest(ctx context.Context) (*githubapp.AppDelivery, error) {
	return a.latest(ctx, nil)
}

func (a appDeliveries) LatestOf(ctx context.Context, events []string) (*githubapp.AppDelivery, error) {
	return a.latest(ctx, events)
}

func (a appDeliveries) latest(ctx context.Context, events []string) (*githubapp.AppDelivery, error) {
	d, err := a.store.LatestAppDelivery(ctx, events)
	if err != nil || d == nil {
		return nil, err
	}
	return &githubapp.AppDelivery{Event: d.Event, Action: d.Action, Outcome: string(d.Outcome), ReceivedAt: d.ReceivedAt}, nil
}

// loadSupervisorPackage reads the package the server ships. A server that
// has none (a dev stack not built from the image) still starts: the routes
// answer 404.
func loadSupervisorPackage(dir string, logger *slog.Logger) *supervisorpkg.Package {
	pkg, err := supervisorpkg.Load(dir)
	if err != nil {
		logger.Warn("serving no supervisor package", "dir", dir, "error", err)
		return nil
	}
	logger.Info("serving the supervisor package", "dir", dir, "version", pkg.Version)
	return pkg
}

// shippedPackageVersion is the build id of the package the server ships, ""
// when it ships none.
func shippedPackageVersion(dir string) string {
	pkg, err := supervisorpkg.Load(dir)
	if err != nil {
		return ""
	}
	return pkg.Version
}

// tenantNameOf reads the name of the tenant the user is signed in to.
func tenantNameOf(users *user.Service) supervisorpkg.TenantName {
	return func(ctx context.Context, who *authn.User) (string, error) {
		tenants, err := users.ListTenants(ctx, who, false)
		if err != nil {
			return "", err
		}
		for i := range tenants {
			if tenants[i].ID == who.TenantID {
				return tenants[i].Name, nil
			}
		}
		return "", nil
	}
}

func boardTelemetry(deps *boardDeps) *metrics.Metrics {
	if deps.limits == nil {
		return nil
	}
	return deps.limits.telemetry
}
