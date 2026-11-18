package oauth

import (
	"context"
	"errors"
	"log/slog"
	"net/netip"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/go-auth/clientip"
	"github.com/lowaa/slop-factory/go-auth/ratelimit"

	"github.com/lowaa/slop-factory/go-clock"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
)

// Config is the policy the server runs under.
type Config struct {
	// Issuer is this server's public base URL (PUBLIC_BASE_URL): the
	// authorization server's identifier, and the root of every endpoint
	// it advertises.
	Issuer string

	// AccessTTL is short because an access token is a bearer credential
	// sitting in a hook script's environment; the refresh token, which
	// rotates on every use, is what keeps an agent signed in.
	AccessTTL      time.Duration
	RefreshTTL     time.Duration
	CodeTTL        time.Duration
	AuthRequestTTL time.Duration
	DeviceTTL      time.Duration
	PollInterval   time.Duration

	// RegisterLimit bounds client registrations per client address: the
	// endpoint is open, as dynamic registration has to be.
	RegisterLimit *ratelimit.Config
	// DeviceLookupLimit bounds user-code guesses per signed-in user.
	DeviceLookupLimit *ratelimit.Config
	// DeviceStartLimit bounds device authorizations started per client
	// address: the endpoint is open and each one is a stored row, so
	// unbounded it is a way to fill the database.
	DeviceStartLimit *ratelimit.Config
	// SlotCredentialLimit bounds the credentials one supervisor may have
	// issued for its slots. Each one is a grant and a token pair; a runaway
	// supervisor, or a stolen supervisor credential, would otherwise mint them
	// without end.
	SlotCredentialLimit *ratelimit.Config
	// MaxUnclaimedClients caps the registered clients never authorized.
	// RegisterLimit is per client address, so a flood from many addresses
	// would still grow the table until the pruner caught up; past this,
	// registration answers 503 until it does. A real client registers and
	// authorizes within minutes, so the cap only ever holds a flood.
	MaxUnclaimedClients int
}

// Agents is what a grant needs from the agent package: the agent a user
// picks on the consent page, or a new one they name there. Called on a
// context scoped to the user's tenant.
type Agents interface {
	// Choose returns the agent the user is authorizing: agentID when given
	// (it must exist in the tenant and be enabled, and a conductor must be
	// the user's own), otherwise a new agent called name.
	Choose(ctx context.Context, who *authn.User, agentID *uuid.UUID, name, kind string) (uuid.UUID, error)
	// ChooseReplacing creates a new agent called name that takes over from
	// replaces, which is disabled and archived with it. The user must be
	// allowed to disable the replaced agent.
	ChooseReplacing(ctx context.Context, who *authn.User, replaces uuid.UUID, name, kind string) (uuid.UUID, error)
	// ChooseSupervisor creates a new supervisor called name, which may have
	// slotCap workers at once. ErrSlotCapInvalid for a cap the server does not
	// allow.
	ChooseSupervisor(ctx context.Context, who *authn.User, name string, slotCap int) (uuid.UUID, error)
	// Enabled reports whether an agent may still act.
	Enabled(ctx context.Context, agentID uuid.UUID) (bool, error)
}

// GrantsEnded is told when an agent is left with no live grant, so whatever
// it had pending for a human can be dropped. The tenant is passed
// explicitly: the context is not guaranteed to be scoped to it.
type GrantsEnded interface {
	GrantsEnded(ctx context.Context, tenantID, agentID uuid.UUID) error
}

// WithRefreshReuseGrace sets how long a rotated-out refresh token is still
// answered with the pair it was rotated for; zero or less ends the grace
// and makes any reuse a revocation.
func WithRefreshReuseGrace(d time.Duration) Option {
	return func(s *Service) { s.reuseGrace = d }
}

// Option customises a Service.
type Option func(*Service)

// WithGrantsEnded sets who hears that an agent has no grants left.
func WithGrantsEnded(g GrantsEnded) Option {
	return func(s *Service) { s.grantsEnded = g }
}

// WithClock sets the clock that stamps expiries; the default is the wall
// clock.
func WithClock(c clock.Clock) Option {
	return func(s *Service) { s.clock = clock.Or(c) }
}

type Service struct {
	store         Store
	agents        Agents
	grantsEnded   GrantsEnded
	cfg           *Config
	registerLimit *ratelimit.Limiter
	lookupLimit   *ratelimit.Limiter
	startLimit    *ratelimit.Limiter
	slotLimit     *ratelimit.Limiter
	events        *audit.Recorder
	logger        *slog.Logger
	reuseGrace    time.Duration
	clock         clock.Clock
}

// NewService builds the service. events records registrations, consent
// decisions, device authorizations and token reuse; nil logs them to
// logHandler and counts nothing.
func NewService(store Store, agents Agents, cfg *Config, events *audit.Recorder, logHandler slog.Handler, opts ...Option) *Service {
	cfg.Issuer = strings.TrimRight(cfg.Issuer, "/")
	if events == nil {
		events = audit.New(logHandler, nil)
	}
	svc := &Service{
		store:         store,
		agents:        agents,
		cfg:           cfg,
		registerLimit: ratelimit.New(cfg.RegisterLimit),
		lookupLimit:   ratelimit.New(cfg.DeviceLookupLimit),
		startLimit:    ratelimit.New(cfg.DeviceStartLimit),
		events:        events,
		logger:        slog.New(logHandler),
		reuseGrace:    DefaultRefreshReuseGrace,
		clock:         clock.Real{},
	}
	// Without a limit no slot credential is issued, rather than an unlimited
	// one: the limit is what bounds a stolen supervisor credential.
	if cfg.SlotCredentialLimit != nil {
		svc.slotLimit = ratelimit.New(cfg.SlotCredentialLimit)
	}
	for _, opt := range opts {
		opt(svc)
	}
	return svc
}

// Resource is the one resource (RFC 8707) tokens are issued for: the MCP
// endpoint. The hooks and agent REST routes are the same server and accept
// the same tokens; the audience check that matters is that a token minted
// for some other server is never accepted here, and one minted here is
// never usable there.
func (s *Service) Resource() string { return ResourceFor(s.cfg.Issuer) }

// ResourceFor is the resource tokens are issued for by the server at
// issuer.
func ResourceFor(issuer string) string { return strings.TrimRight(issuer, "/") + "/mcp" }

// checkResource accepts an absent resource (a client that does not send
// one) or exactly ours.
func (s *Service) checkResource(resource string) (string, error) {
	if resource == "" || resource == s.Resource() {
		return s.Resource(), nil
	}
	return "", errInvalidTarget
}

// -- Dynamic client registration (RFC 7591) --

// RegistrationRequest is the subset of client metadata this server reads.
type RegistrationRequest struct {
	ClientName              string   `json:"client_name"`
	RedirectURIs            []string `json:"redirect_uris"`
	GrantTypes              []string `json:"grant_types"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
}

// Register records a public client. Only the authorization code grant is
// open to registration -- the device grant belongs to the workspace client
// -- and only with loopback redirects (see validRedirectURI).
func (s *Service) Register(ctx context.Context, clientAddr netip.Addr, req *RegistrationRequest) (*Client, error) {
	if !s.registerLimit.Allow(clientip.RateKey(clientAddr)) {
		return nil, errRateLimited
	}
	if req.TokenEndpointAuthMethod != "" && req.TokenEndpointAuthMethod != "none" {
		return nil, oauthErr(400, "invalid_client_metadata", "only public clients (token_endpoint_auth_method none) are supported")
	}
	grants := req.GrantTypes
	if len(grants) == 0 {
		grants = []string{GrantAuthorizationCode}
	}
	for _, g := range grants {
		if g != GrantAuthorizationCode && g != GrantRefreshToken {
			return nil, oauthErr(400, "invalid_client_metadata", "unsupported grant type "+g)
		}
	}
	if !slices.Contains(grants, GrantRefreshToken) {
		grants = append(grants, GrantRefreshToken)
	}
	if len(req.RedirectURIs) == 0 {
		return nil, oauthErr(400, "invalid_redirect_uri", "at least one redirect URI is required")
	}
	for _, uri := range req.RedirectURIs {
		if !validRedirectURI(uri) {
			return nil, oauthErr(400, "invalid_redirect_uri", "redirect URIs must be http loopback addresses: "+uri)
		}
	}
	name := strings.TrimSpace(req.ClientName)
	if len(name) > 100 {
		name = name[:100]
	}
	c := &Client{ID: "c-" + uuid.NewString(), Name: name, RedirectURIs: req.RedirectURIs, GrantTypes: grants}
	err := s.store.CreateClient(ctx, c, s.cfg.MaxUnclaimedClients)
	if errors.Is(err, ErrTooManyClients) {
		s.events.Record(ctx, audit.ClientRegistrationRefused, "reason", "too many unclaimed clients", "max", s.cfg.MaxUnclaimedClients)
		return nil, errRegistrationFull
	}
	if err != nil {
		return nil, err
	}
	s.events.Record(ctx, audit.ClientRegistered, "client_id", c.ID, "client_name", c.Name)
	return c, nil
}

// -- Using and managing grants --

// ErrInvalidToken means an access token that is unknown, expired, or
// belongs to a revoked grant. One answer for all of them, as elsewhere.
var ErrInvalidToken = errors.New("oauth: invalid access token")

// Authenticate resolves an access token to the tenant and agent it acts
// as, and whether it is read-only. The agent's own state (disabled or not)
// is the caller's to check.
func (s *Service) Authenticate(ctx context.Context, accessToken string) (*Access, error) {
	if accessToken == "" {
		return nil, ErrInvalidToken
	}
	g, err := s.store.LookupAccess(ctx, hashSecret(accessToken))
	if errors.Is(err, ErrNotFound) {
		return nil, ErrInvalidToken
	}
	if err != nil {
		return nil, err
	}
	if g.TenantArchived {
		return nil, ErrInvalidToken
	}
	return &Access{TenantID: g.TenantID, AgentID: g.AgentID, ReadOnly: g.Scope == ScopeRead}, nil
}

// Grants lists the signed-in user's tenant's live grants.
func (s *Service) Grants(ctx context.Context, who *authn.User) ([]Grant, error) {
	return s.store.ListGrants(ctx, who.TenantID)
}

// RevokeGrant ends a grant and every token under it.
func (s *Service) RevokeGrant(ctx context.Context, who *authn.User, grantID uuid.UUID) error {
	// Read first: the revoke does not say whose grant it was, and once it
	// is revoked the live listing no longer has it.
	live, err := s.store.ListGrants(ctx, who.TenantID)
	if err != nil {
		return err
	}
	if err := s.store.RevokeGrant(ctx, who.TenantID, grantID); err != nil {
		return err
	}
	s.events.Record(ctx, audit.GrantRevoked, "user_id", who.ID, "tenant", who.TenantID, "grant_id", grantID)
	for i := range live {
		if live[i].ID == grantID {
			return s.noteGrantsMaybeEnded(ctx, who.TenantID, live[i].AgentID)
		}
	}
	return nil
}

// noteGrantsMaybeEnded tells the GrantsEnded hook when the agent has no live
// full-access grant left (a read-only one cannot propose or act). Checked after the revoke commits, so of two concurrent revokes
// of its last two grants at least one sees none left; both may, and the hook
// is idempotent.
func (s *Service) noteGrantsMaybeEnded(ctx context.Context, tenantID, agentID uuid.UUID) error {
	if s.grantsEnded == nil {
		return nil
	}
	live, err := s.store.ListGrants(ctx, tenantID)
	if err != nil {
		return err
	}
	for i := range live {
		if live[i].AgentID == agentID && live[i].Scope != ScopeRead {
			return nil
		}
	}
	// The revoke has committed, so a failure here is not the caller's to
	// retry (a second attempt finds nothing left to revoke): log it, and
	// leave the approve-time recheck to refuse what could not be voided.
	if err := s.grantsEnded.GrantsEnded(ctx, tenantID, agentID); err != nil {
		s.logger.ErrorContext(ctx, "oauth: ending an agent's proposals after its last grant went", "agent", agentID, "err", err)
	}
	return nil
}

// HasFullAccessGrant reports whether the agent holds a live grant that can
// act, not merely read: what a console needs for its proposals to count.
func (s *Service) HasFullAccessGrant(ctx context.Context, tenantID, agentID uuid.UUID) (bool, error) {
	live, err := s.store.ListGrants(ctx, tenantID)
	if err != nil {
		return false, err
	}
	for i := range live {
		if live[i].AgentID == agentID && live[i].Scope != ScopeRead {
			return true, nil
		}
	}
	return false, nil
}

// FullAccessApprovers is who approved each of the agent's live full-access
// grants, one entry per grant (nil where none is recorded). It is how a
// console is tied to a person: whoever it holds a live grant from.
func (s *Service) FullAccessApprovers(ctx context.Context, tenantID, agentID uuid.UUID) ([]*uuid.UUID, error) {
	live, err := s.store.ListGrants(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	var approvers []*uuid.UUID
	for i := range live {
		if live[i].AgentID == agentID && live[i].Scope != ScopeRead {
			approvers = append(approvers, live[i].ApprovedBy)
		}
	}
	return approvers, nil
}

// RevokeAgentGrants ends every grant acting as an agent, so no workspace
// signed in as it keeps working. The tenant is the caller's to establish:
// this takes it as given rather than from a signed-in user, because the
// agent package calls it with the tenant of an agent it has just read.
func (s *Service) RevokeAgentGrants(ctx context.Context, tenantID, agentID uuid.UUID) error {
	if err := s.store.RevokeAgentGrants(ctx, tenantID, agentID); err != nil {
		return err
	}
	return s.noteGrantsMaybeEnded(ctx, tenantID, agentID)
}

// grantIdentity scopes a context to a grant's tenant for the one check the
// token endpoint makes against tenant data -- whether the agent is still
// enabled -- before any request identity exists.
type grantIdentity struct{ g *Grant }

func (i grantIdentity) AgentID() uuid.UUID  { return i.g.AgentID }
func (i grantIdentity) AgentName() string   { return "" }
func (i grantIdentity) TenantID() uuid.UUID { return i.g.TenantID }
func (i grantIdentity) ReadOnly() bool      { return i.g.Scope == ScopeRead }

// Metadata is this server's authorization server metadata (RFC 8414).
func (s *Service) Metadata() map[string]any {
	return map[string]any{
		"issuer":                                         s.cfg.Issuer,
		"authorization_endpoint":                         s.cfg.Issuer + "/oauth/authorize",
		"token_endpoint":                                 s.cfg.Issuer + "/oauth/token",
		"registration_endpoint":                          s.cfg.Issuer + "/oauth/register",
		"device_authorization_endpoint":                  s.cfg.Issuer + "/oauth/device_authorization",
		"response_types_supported":                       []string{"code"},
		"grant_types_supported":                          []string{GrantAuthorizationCode, GrantRefreshToken, GrantDeviceCode, GrantClientCredentials},
		"code_challenge_methods_supported":               []string{"S256"},
		"token_endpoint_auth_methods_supported":          []string{"none", "client_secret_basic", "client_secret_post"},
		"authorization_response_iss_parameter_supported": true,
	}
}

// ResourceMetadata is the MCP endpoint's protected resource metadata
// (RFC 9728), which is where an MCP client starts.
func (s *Service) ResourceMetadata() map[string]any {
	return map[string]any{
		"resource":                 s.Resource(),
		"authorization_servers":    []string{s.cfg.Issuer},
		"bearer_methods_supported": []string{"header"},
	}
}

// ResourceMetadataURL is where ResourceMetadata is served, for the
// WWW-Authenticate header on a 401 from the MCP endpoint.
func (s *Service) ResourceMetadataURL() string {
	return s.cfg.Issuer + "/.well-known/oauth-protected-resource/mcp"
}
