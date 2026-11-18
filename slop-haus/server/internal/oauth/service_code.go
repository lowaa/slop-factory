package oauth

import (
	"context"
	"errors"
	"net/url"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
)

// AuthorizeParams are /oauth/authorize's query parameters.
type AuthorizeParams struct {
	ResponseType        string
	ClientID            string
	RedirectURI         string
	CodeChallenge       string
	CodeChallengeMethod string
	State               string
	Resource            string
}

// ErrUntrustedRedirect means the client or redirect URI could not be
// verified, so the error must be shown to the user rather than sent to the
// redirect URI (RFC 6749 section 4.1.2.1): redirecting to an unverified URI
// would make this server an open redirector.
var ErrUntrustedRedirect = errors.New("oauth: unknown client or unregistered redirect URI")

// RedirectError is an authorization request's error that can be reported
// to the client: Location is the client's registered redirect URI with the
// error and state on it. Only Authorize makes one, and only once the
// redirect URI is verified, so it is the one error the authorize endpoint
// redirects for.
type RedirectError struct {
	Location string
	Err      *Error
}

func (e *RedirectError) Error() string { return e.Err.Error() }

func (e *RedirectError) Unwrap() error { return e.Err }

// Authorize validates an authorization request and stores it for the
// consent page. It returns the consent page's URL; a *RedirectError for an
// invalid request from a verified client and redirect URI; or
// ErrUntrustedRedirect, which must be shown to the user and not redirected.
func (s *Service) Authorize(ctx context.Context, p *AuthorizeParams) (consentURL string, err error) {
	client, err := s.store.GetClient(ctx, p.ClientID)
	if err != nil {
		return "", ErrUntrustedRedirect
	}
	redirectURI, ok := client.registeredRedirect(p.RedirectURI)
	if !ok {
		return "", ErrUntrustedRedirect
	}
	consentURL, err = s.authorizeFor(ctx, client, redirectURI, p)
	var oe *Error
	if errors.As(err, &oe) {
		return "", &RedirectError{Location: ErrorRedirect(redirectURI, p.State, oe), Err: oe}
	}
	return consentURL, err
}

// authorizeFor is Authorize once the client and redirectURI, its
// registered value, are verified.
func (s *Service) authorizeFor(ctx context.Context, client *Client, redirectURI string, p *AuthorizeParams) (string, error) {
	if p.ResponseType != "code" {
		return "", oauthErr(400, "unsupported_response_type", "only the code response type is supported")
	}
	if !client.allows(GrantAuthorizationCode) {
		return "", errUnauthorizedGrant
	}
	// OAuth 2.1: PKCE is required, and only S256 -- plain would put the
	// secret in the front channel.
	if p.CodeChallengeMethod != "S256" || len(p.CodeChallenge) < 43 {
		return "", oauthErr(400, "invalid_request", "PKCE with code_challenge_method S256 is required")
	}
	resource, err := s.checkResource(p.Resource)
	if err != nil {
		return "", err
	}
	req := &AuthRequest{
		ID:            uuid.New(),
		ClientID:      client.ID,
		RedirectURI:   redirectURI,
		CodeChallenge: p.CodeChallenge,
		State:         p.State,
		Resource:      resource,
		ExpiresAt:     s.clock.Now().Add(s.cfg.AuthRequestTTL),
	}
	if err := s.store.CreateAuthRequest(ctx, req); err != nil {
		return "", err
	}
	return s.cfg.Issuer + "/authorize?request=" + req.ID.String(), nil
}

// ErrorRedirect is where to send the browser to report err to the client.
func ErrorRedirect(redirectURI, state string, err *Error) string {
	return withQuery(redirectURI, map[string]string{"error": err.Code, "error_description": err.Description, "state": state})
}

func withQuery(base string, params map[string]string) string {
	u, err := url.Parse(base)
	if err != nil {
		return base
	}
	q := u.Query()
	for k, v := range params {
		if v != "" {
			q.Set(k, v)
		}
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// PendingRequest is what the consent page shows.
type PendingRequest struct {
	ClientName   string
	RedirectHost string
}

// PendingAuthRequest describes a request for the consent page.
func (s *Service) PendingAuthRequest(ctx context.Context, id uuid.UUID) (*PendingRequest, error) {
	req, client, err := s.store.GetAuthRequest(ctx, id)
	if err != nil {
		return nil, err
	}
	host := req.RedirectURI
	if u, err := url.Parse(req.RedirectURI); err == nil {
		host = u.Host
	}
	return &PendingRequest{ClientName: client.Name, RedirectHost: host}, nil
}

// Decision is the user's answer on a consent page: which agent the client
// may act as -- an existing one or a new one by name -- or a refusal.
type Decision struct {
	Approve   bool
	AgentID   *uuid.UUID
	AgentName string
	// Kind is the kind of a new agent: "worker", "console" or "supervisor", ""
	// for worker. An existing agent keeps its own.
	Kind string
	// SlotCap is a new supervisor's cap; set for that kind and no other.
	SlotCap *int
	// ReplacesAgentID, with AgentName, makes the new agent take over from an
	// existing one.
	ReplacesAgentID *uuid.UUID
}

// DecideAuthRequest records the user's decision and returns where the
// browser goes next: back to the client with a code, or with an error.
func (s *Service) DecideAuthRequest(ctx context.Context, who *authn.User, id uuid.UUID, d *Decision) (string, error) {
	if !d.Approve {
		req, err := s.store.DenyAuthRequest(ctx, id)
		if err != nil {
			return "", err
		}
		s.events.Record(ctx, audit.AuthorizationDenied, "user_id", who.ID, "tenant", who.TenantID, "client_id", req.ClientID)
		return ErrorRedirect(req.RedirectURI, req.State, errDenied), nil
	}
	pending, _, err := s.store.GetAuthRequest(ctx, id)
	if err != nil {
		return "", err
	}
	g, err := s.newGrant(ctx, who, pending.ClientID, pending.Resource, d)
	if err != nil {
		return "", err
	}
	code, codeHash, err := newSecret()
	if err != nil {
		return "", err
	}
	req, err := s.store.ApproveAuthRequest(ctx, id, g, codeHash, s.clock.Now().Add(s.cfg.CodeTTL))
	if err != nil {
		return "", err
	}
	s.events.Record(ctx, audit.AuthorizationApproved, "user_id", who.ID, "tenant", who.TenantID, "client_id", req.ClientID,
		"agent_id", g.AgentID, "grant_id", g.ID)
	// iss (RFC 9207) lets a client that talks to several servers tell whose
	// response this is.
	return withQuery(req.RedirectURI, map[string]string{"code": code, "state": req.State, "iss": s.cfg.Issuer}), nil
}

func (s *Service) newGrant(ctx context.Context, who *authn.User, clientID, resource string, d *Decision) (*Grant, error) {
	var agentID uuid.UUID
	var err error
	switch {
	case d.Kind == supervisorKind:
		// The cap is the body's to have checked (decision), so this refuses a
		// caller that built a Decision by hand.
		if d.SlotCap == nil || d.AgentID != nil || d.ReplacesAgentID != nil {
			return nil, ErrAgentKindInvalid
		}
		agentID, err = s.agents.ChooseSupervisor(ctx, who, d.AgentName, *d.SlotCap)
	case d.SlotCap != nil:
		return nil, ErrAgentKindInvalid
	case d.ReplacesAgentID != nil:
		agentID, err = s.agents.ChooseReplacing(ctx, who, *d.ReplacesAgentID, d.AgentName, d.Kind)
	default:
		agentID, err = s.agents.Choose(ctx, who, d.AgentID, d.AgentName, d.Kind)
	}
	if err != nil {
		return nil, err
	}
	approvedBy := who.ID
	return &Grant{
		ID:         uuid.New(),
		TenantID:   who.TenantID,
		AgentID:    agentID,
		ClientID:   clientID,
		ApprovedBy: &approvedBy,
		Resource:   resource,
	}, nil
}
