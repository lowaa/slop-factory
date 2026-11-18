package oauth

import (
	"context"
	"crypto/subtle"
	"errors"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/agent"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
)

// TokenRequest is /oauth/token's form.
type TokenRequest struct {
	GrantType    string
	ClientID     string
	Code         string
	RedirectURI  string
	CodeVerifier string
	RefreshToken string
	DeviceCode   string
	Resource     string
	// ClientSecret authenticates a confidential client (the client
	// credentials grant); every other grant's client is public.
	ClientSecret string
}

// TokenResponse is a successful token response (RFC 6749 section 5.1).
type TokenResponse = agent.TokenResponse

// Token runs the token endpoint.
func (s *Service) Token(ctx context.Context, req *TokenRequest) (*TokenResponse, error) {
	client, err := s.store.GetClient(ctx, req.ClientID)
	if err != nil {
		return nil, errInvalidClient
	}
	if !client.allows(req.GrantType) {
		if req.GrantType != GrantAuthorizationCode && req.GrantType != GrantRefreshToken && req.GrantType != GrantDeviceCode &&
			req.GrantType != GrantClientCredentials {
			return nil, oauthErr(400, "unsupported_grant_type", "unsupported grant type")
		}
		return nil, errUnauthorizedGrant
	}
	if _, err := s.checkResource(req.Resource); err != nil {
		return nil, err
	}
	if req.GrantType == GrantClientCredentials {
		return s.clientCredentials(ctx, client, req)
	}

	// The replacement tokens are made first and handed to the spend, which
	// commits them together with it (see TokenPair).
	resp, pair, err := s.newTokenPair()
	if err != nil {
		return nil, err
	}
	switch req.GrantType {
	case GrantAuthorizationCode:
		err = s.exchangeCode(ctx, client, req, pair)
	case GrantRefreshToken:
		var replayed *TokenResponse
		replayed, err = s.exchangeRefresh(ctx, client, req, resp, pair)
		if err == nil && replayed != nil {
			return replayed, nil
		}
	case GrantDeviceCode:
		_, err = s.store.PollDeviceCode(ctx, hashSecret(req.DeviceCode), client.ID, s.cfg.PollInterval, pair)
	}
	var reused *ReusedError
	if errors.As(err, &reused) {
		s.events.Record(ctx, audit.TokenReuse, "kind", reused.Kind, "grant_id", reused.GrantID, "client_id", client.ID)
		return nil, errInvalidGrant
	}
	if err != nil {
		return nil, err
	}
	return resp, nil
}

func (s *Service) exchangeCode(ctx context.Context, client *Client, req *TokenRequest, pair *TokenPair) error {
	// The code is spent whether or not these hold: a failed check is a
	// stolen or mangled code, and it must not be tried again.
	_, err := s.store.SpendCode(ctx, hashSecret(req.Code), pair, func(g *Grant, redirectURI, challenge string) bool {
		return g.ClientID == client.ID && redirectURI == req.RedirectURI && verifyPKCE(req.CodeVerifier, challenge)
	})
	return err
}

// exchangeRefresh rotates a refresh token. A token rotated out moments ago
// is answered from the sealed copy of what that rotation issued, returned
// as replayed; every other path leaves resp, now stored, to be sent.
func (s *Service) exchangeRefresh(ctx context.Context, client *Client, req *TokenRequest, resp *TokenResponse, pair *TokenPair) (replayed *TokenResponse, err error) {
	refreshHash := hashSecret(req.RefreshToken)
	g, err := s.store.GrantForRefresh(ctx, refreshHash)
	if errors.Is(err, errInvalidGrant) {
		return s.replayOrRevoke(ctx, client, req.RefreshToken, refreshHash)
	}
	if err != nil {
		return nil, err
	}
	if g.ClientID != client.ID {
		return nil, errInvalidGrant
	}
	if g.TenantArchived {
		return nil, errTenantArchived
	}
	// A disabled agent's session ends here, but its grant survives:
	// re-enabling the agent should not need the workspace set up again.
	enabled, err := s.agents.Enabled(authn.NewContext(ctx, grantIdentity{g}), g.AgentID)
	if err != nil {
		return nil, err
	}
	if !enabled {
		return nil, errInvalidGrant
	}
	if pair.Replay, err = sealResponse(resp, req.RefreshToken, refreshHash); err != nil {
		return nil, err
	}
	_, err = s.store.SpendRefresh(ctx, refreshHash, pair)
	if errors.Is(err, errRefreshSpent) {
		// Lost a race with a request that read the token live too: that is
		// the winner's retry arriving, not a copy.
		return s.replayOrRevoke(ctx, client, req.RefreshToken, refreshHash)
	}
	return nil, err
}

// replayOrRevoke settles a refresh token that is not live: answered with
// the pair its rotation issued if that was moments ago, otherwise a reuse.
func (s *Service) replayOrRevoke(ctx context.Context, client *Client, refreshToken, refreshHash string) (*TokenResponse, error) {
	replayed, err := s.replayRefresh(ctx, client, refreshToken, refreshHash)
	if err != nil || replayed != nil {
		return replayed, err
	}
	return nil, s.store.RevokeReused(ctx, refreshHash)
}

// replayRefresh answers a refresh token presented again within the reuse
// grace window with the pair its rotation already issued: the client's
// first response was probably lost, and minting a second pair or revoking
// would each cost it the grant. Nothing new is issued, so a copy of the
// token used inside the window gets only what the real holder was sent.
// nil, nil means the token is not replayable and is a reuse.
func (s *Service) replayRefresh(ctx context.Context, client *Client, refreshToken, refreshHash string) (*TokenResponse, error) {
	if s.reuseGrace <= 0 {
		return nil, nil
	}
	g, blob, err := s.store.ReplayRefresh(ctx, refreshHash, s.reuseGrace)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if g.ClientID != client.ID {
		return nil, errInvalidGrant
	}
	if g.TenantArchived {
		return nil, errTenantArchived
	}
	enabled, err := s.agents.Enabled(authn.NewContext(ctx, grantIdentity{g}), g.AgentID)
	if err != nil {
		return nil, err
	}
	if !enabled {
		return nil, errInvalidGrant
	}
	resp, err := openResponse(blob, refreshToken, refreshHash)
	if err != nil {
		// Not openable means not the pair this token was issued: treat as
		// reuse rather than trust it.
		s.logger.Warn("oauth: refresh replay unreadable", "grant_id", g.ID, "error", err)
		return nil, nil
	}
	s.logger.InfoContext(ctx, "oauth: refresh token replayed within grace", "grant_id", g.ID, "client_id", client.ID)
	return resp, nil
}

// clientCredentials issues a read-only client an access token for its one
// grant, on its secret (RFC 6749 section 4.4). No refresh token: the secret
// is the long-lived credential, so there is nothing to rotate -- and so
// nothing for a stateless caller like CI to fail to store back.
func (s *Service) clientCredentials(ctx context.Context, client *Client, req *TokenRequest) (*TokenResponse, error) {
	// A client with no secret is a public one: a client credentials
	// request naming it is refused as though the secret were wrong. The
	// comparison is of hashes, in constant time, so a wrong guess learns
	// nothing from how long the refusal took.
	if client.SecretHash == "" || req.ClientSecret == "" ||
		subtle.ConstantTimeCompare([]byte(hashSecret(req.ClientSecret)), []byte(client.SecretHash)) != 1 {
		s.events.Record(ctx, audit.ClientSecretRefused, "client_id", client.ID)
		return nil, errInvalidClient
	}
	g, err := s.store.ClientGrant(ctx, client.ID)
	if errors.Is(err, ErrNotFound) {
		return nil, errInvalidGrant
	}
	if err != nil {
		return nil, err
	}
	if g.TenantArchived {
		return nil, errTenantArchived
	}
	// Only ever read-only: a secret is a credential that sits in CI, not
	// one a person approved, so it must never reach a full agent's tokens,
	// whatever grant a client ends up holding.
	switch g.Scope {
	case ScopeRead:
	default:
		s.logger.Error("client credentials client holds a grant that isn't read-only", "client_id", client.ID, "scope", g.Scope)
		return nil, errInvalidGrant
	}
	enabled, err := s.agents.Enabled(authn.NewContext(ctx, grantIdentity{g}), g.AgentID)
	if err != nil {
		return nil, err
	}
	if !enabled {
		return nil, errInvalidGrant
	}
	access, accessHash, err := newSecret()
	if err != nil {
		return nil, err
	}
	err = s.store.IssueAccess(ctx, g.ID, &IssuedToken{Hash: accessHash, ExpiresAt: s.clock.Now().Add(s.cfg.AccessTTL)})
	if errors.Is(err, ErrNotFound) {
		return nil, errInvalidGrant
	}
	if err != nil {
		return nil, err
	}
	return &TokenResponse{AccessToken: access, TokenType: "Bearer", ExpiresIn: int(s.cfg.AccessTTL.Seconds())}, nil
}

// newTokenPair makes an access and a refresh token: the response carrying
// them, and the hashes the store keeps.
func (s *Service) newTokenPair() (*TokenResponse, *TokenPair, error) {
	access, accessHash, err := newSecret()
	if err != nil {
		return nil, nil, err
	}
	refresh, refreshHash, err := newSecret()
	if err != nil {
		return nil, nil, err
	}
	now := s.clock.Now()
	return &TokenResponse{
			AccessToken:  access,
			TokenType:    "Bearer",
			ExpiresIn:    int(s.cfg.AccessTTL.Seconds()),
			RefreshToken: refresh,
		}, &TokenPair{
			Access:  &IssuedToken{Hash: accessHash, ExpiresAt: now.Add(s.cfg.AccessTTL)},
			Refresh: &IssuedToken{Hash: refreshHash, ExpiresAt: now.Add(s.cfg.RefreshTTL)},
		}, nil
}
