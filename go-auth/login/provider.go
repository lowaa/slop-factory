// Package login is the shared half of a service's login flow: an identity
// provider (GitHub, or any OIDC issuer) vouches that "this provider says
// you are this identity", behind the AuthProvider interface, and that is
// where this package's job ends. Turning an Identity into the service's own
// user, issuing and storing a session, cookies, CSRF -- that stays with
// each service, since it depends on that service's own users and sessions
// in a way this package must not.
package login

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"

	"golang.org/x/oauth2"
)

// AuthProvider is one identity provider's half of the login flow.
type AuthProvider interface {
	// Name is stored as the identity's provider namespace, so it must never
	// change once users have logged in with it.
	Name() string
	// DisplayName names the provider to people, e.g. on the login button.
	DisplayName() string
	// AuthCodeURL is where the browser is sent to log in.
	AuthCodeURL(a *LoginAttempt) (string, error)
	// Exchange turns the code the provider redirected back with into the
	// identity it vouches for. a is the same attempt AuthCodeURL was given.
	Exchange(ctx context.Context, code string, a *LoginAttempt) (*Identity, error)
}

// ErrProviderUnavailable means the provider can't serve logins yet -- an
// OIDC issuer whose discovery hasn't succeeded. It is temporary.
var ErrProviderUnavailable = errors.New("login: provider unavailable")

// LoginAttempt is the per-login secrets the flow generates and keeps in the
// state cookie between login and callback:
//   - State binds the callback to the browser that started the login
//     (login CSRF).
//   - Verifier is the PKCE code verifier: a stolen authorization code is
//     useless without it.
//   - Nonce is echoed back inside an OIDC ID token, binding that token to
//     this attempt so a token minted for another login can't be replayed.
type LoginAttempt struct {
	State    string
	Verifier string
	Nonce    string
}

// NewLoginAttempt starts a fresh attempt with new random secrets.
func NewLoginAttempt() (*LoginAttempt, error) {
	state, err := randomSecret()
	if err != nil {
		return nil, err
	}
	nonce, err := randomSecret()
	if err != nil {
		return nil, err
	}
	return &LoginAttempt{State: state, Verifier: oauth2.GenerateVerifier(), Nonce: nonce}, nil
}

// randomSecret is a 256-bit random value, base64url encoded: plenty for a
// state or nonce nobody needs to guess, and short enough to fit comfortably
// in the state cookie alongside the rest of the attempt.
func randomSecret() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("login: read random: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}
