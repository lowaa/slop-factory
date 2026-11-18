package login

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// OIDCOptions configure an OIDCAuthProvider.
type OIDCOptions struct {
	IssuerURL    string
	ClientID     string
	ClientSecret string
	CallbackURL  string
	// Name is the identity namespace subjects are stored under; see
	// AuthProvider.Name. It must only ever be used with this one issuer:
	// subjects are unique per issuer, not globally.
	Name        string
	DisplayName string
	// HTTPClient is used for discovery, the JWKS and the token exchange.
	// Defaults to a client with a 15s timeout.
	HTTPClient *http.Client
	// RetryMin and RetryMax bound the backoff between discovery attempts
	// made by Start. Default 1s and 1m.
	RetryMin, RetryMax time.Duration
}

// OIDCAuthProvider logs in with any OpenID Connect issuer (Dex, Keycloak,
// ...). Discovery needs the issuer up, which it may not be when the server
// starts (in docker compose the IdP can start after the api), so the
// provider starts unready, and Start keeps discovering in the background;
// until it succeeds, logins get ErrProviderUnavailable rather than the
// server refusing to start at all.
type OIDCAuthProvider struct {
	opts   *OIDCOptions
	client *http.Client
	logger *slog.Logger

	mu    sync.RWMutex
	ready *oidcEndpoints

	cancel context.CancelFunc
	done   chan struct{}
}

// oidcEndpoints is what discovery yields.
type oidcEndpoints struct {
	oauth    *oauth2.Config
	verifier *oidc.IDTokenVerifier
}

func NewOIDCAuthProvider(opts *OIDCOptions, logHandler slog.Handler) *OIDCAuthProvider {
	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	return &OIDCAuthProvider{opts: opts, client: client, logger: slog.New(logHandler)}
}

func (p *OIDCAuthProvider) Name() string { return p.opts.Name }

func (p *OIDCAuthProvider) DisplayName() string { return p.opts.DisplayName }

// Ready reports whether discovery has succeeded.
func (p *OIDCAuthProvider) Ready() bool {
	return p.endpoints() != nil
}

// Discover makes one discovery attempt. It is a no-op once one has
// succeeded.
func (p *OIDCAuthProvider) Discover(ctx context.Context) error {
	if p.Ready() {
		return nil
	}
	provider, err := oidc.NewProvider(oidc.ClientContext(ctx, p.client), p.opts.IssuerURL)
	if err != nil {
		return fmt.Errorf("auth: oidc: discover %s: %w", p.opts.IssuerURL, err)
	}
	ep := &oidcEndpoints{
		oauth: &oauth2.Config{
			ClientID:     p.opts.ClientID,
			ClientSecret: p.opts.ClientSecret,
			Endpoint:     provider.Endpoint(),
			RedirectURL:  p.opts.CallbackURL,
			Scopes:       []string{oidc.ScopeOpenID, "profile", "email"},
		},
		// Checks the signature against the issuer's JWKS, the issuer, that
		// the audience includes our client ID, and expiry.
		verifier: provider.Verifier(&oidc.Config{ClientID: p.opts.ClientID}),
	}
	p.mu.Lock()
	p.ready = ep
	p.mu.Unlock()
	return nil
}

// Start discovers in the background, retrying with backoff until it
// succeeds or Close is called. Call Close to stop it.
func (p *OIDCAuthProvider) Start() {
	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel
	p.done = make(chan struct{})
	go func() {
		defer close(p.done)
		p.discoverUntilReady(ctx)
	}()
}

// Close stops a Start'ed discovery loop and waits for it to exit.
func (p *OIDCAuthProvider) Close() {
	if p.cancel == nil {
		return
	}
	p.cancel()
	<-p.done
}

func (p *OIDCAuthProvider) discoverUntilReady(ctx context.Context) {
	wait, maxWait := p.opts.RetryMin, p.opts.RetryMax
	if wait <= 0 {
		wait = time.Second
	}
	if maxWait <= 0 {
		maxWait = time.Minute
	}
	for {
		err := p.Discover(ctx)
		if err == nil {
			p.logger.Info("oidc discovery succeeded", "issuer", p.opts.IssuerURL)
			return
		}
		p.logger.Warn("oidc discovery failed; logins are unavailable until it succeeds", "issuer", p.opts.IssuerURL, "retry_in", wait.String(), "err", err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		wait = min(wait*2, maxWait)
	}
}

func (p *OIDCAuthProvider) endpoints() *oidcEndpoints {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.ready
}

func (p *OIDCAuthProvider) AuthCodeURL(a *LoginAttempt) (string, error) {
	ep := p.endpoints()
	if ep == nil {
		return "", ErrProviderUnavailable
	}
	return ep.oauth.AuthCodeURL(a.State, oauth2.S256ChallengeOption(a.Verifier), oidc.Nonce(a.Nonce)), nil
}

// oidcClaims are the standard claims mapped onto Identity.
type oidcClaims struct {
	Name              string   `json:"name"`
	PreferredUsername string   `json:"preferred_username"`
	Email             string   `json:"email"`
	EmailVerified     flexBool `json:"email_verified"`
}

func (p *OIDCAuthProvider) Exchange(ctx context.Context, code string, a *LoginAttempt) (*Identity, error) {
	ep := p.endpoints()
	if ep == nil {
		return nil, ErrProviderUnavailable
	}
	ctx = oidc.ClientContext(ctx, p.client)
	tok, err := ep.oauth.Exchange(ctx, code, oauth2.VerifierOption(a.Verifier))
	if err != nil {
		return nil, fmt.Errorf("auth: oidc: exchange code: %w", err)
	}
	rawIDToken, ok := tok.Extra("id_token").(string)
	if !ok || rawIDToken == "" {
		return nil, errors.New("auth: oidc: token response has no id_token")
	}
	idToken, err := ep.verifier.Verify(ctx, rawIDToken)
	if err != nil {
		return nil, fmt.Errorf("auth: oidc: verify id token: %w", err)
	}
	// go-oidc leaves the nonce to the caller. Without this check an ID token
	// issued for some other login (say, one an attacker completed) would be
	// accepted for this one.
	if subtle.ConstantTimeCompare([]byte(idToken.Nonce), []byte(a.Nonce)) != 1 || a.Nonce == "" {
		return nil, errors.New("auth: oidc: id token nonce does not match this login")
	}
	var c oidcClaims
	if err := idToken.Claims(&c); err != nil {
		return nil, fmt.Errorf("auth: oidc: decode claims: %w", err)
	}
	if idToken.Subject == "" {
		return nil, errors.New("auth: oidc: id token has no subject")
	}

	login := firstNonEmpty(c.PreferredUsername, c.Email, idToken.Subject)
	return &Identity{
		Provider:      p.Name(),
		Subject:       idToken.Subject,
		Login:         login,
		DisplayName:   firstNonEmpty(c.Name, login),
		Email:         c.Email,
		EmailVerified: bool(c.EmailVerified) && c.Email != "",
	}, nil
}

// flexBool accepts a JSON boolean or the strings "true"/"false": some IdPs
// send email_verified as a string. Anything else is false -- an unverified
// email is the safe reading.
type flexBool bool

func (b *flexBool) UnmarshalJSON(raw []byte) error {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return err
	}
	switch t := v.(type) {
	case bool:
		*b = flexBool(t)
	case string:
		*b = flexBool(t == "true")
	default:
		*b = false
	}
	return nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
