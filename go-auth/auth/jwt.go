package auth

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

const (
	tokenTypeAccess  = "access"
	tokenTypeRefresh = "refresh"
)

var ErrInvalidToken = errors.New("auth: invalid token")

// Claims embeds TenantID directly rather than requiring a DB lookup per
// request: callers that assign a user's tenant permanently at some
// activation-like step (fixed thereafter, e.g. by a unique
// owner-per-tenant constraint) can rely on it staying valid for the life of
// the token. Staleness is bounded by the access token TTL.
type Claims struct {
	UserID   uuid.UUID `json:"user_id"`
	TenantID uuid.UUID `json:"tenant_id"`
	Type     string    `json:"type"`
	// AuthTime is when the person last proved who they are interactively
	// (a sign-in or a re-authentication), under OIDC's name for the same
	// idea. Unlike IssuedAt it does not move when a session is refreshed,
	// so a caller can ask for a recent authentication before a sensitive
	// change without a server-side session record. Nil on tokens issued
	// without one.
	AuthTime *jwt.NumericDate `json:"auth_time,omitempty"`
	// SessionID names the interactive browser session the token was issued
	// for (see NewSessionID). Like AuthTime it does not move when the
	// session is refreshed or re-issued, so a caller can bind per-session
	// state -- a CSRF token (see CSRFTokens) -- to it without a server-side
	// session record. Empty on tokens issued without one.
	SessionID string `json:"sid,omitempty"`
	jwt.RegisteredClaims
}

// Session is what a session's tokens carry unchanged from one issue to the
// next: when its person last authenticated interactively, and which browser
// session it is. Either may be zero, and is then left out of the token.
type Session struct {
	AuthTime time.Time
	ID       string
}

// Session is the session c was issued for, to carry into the next token
// issued for it.
func (c *Claims) Session() *Session {
	sess := &Session{ID: c.SessionID}
	if c.AuthTime != nil {
		sess.AuthTime = c.AuthTime.Time
	}
	return sess
}

type Signer struct {
	secret     []byte
	accessTTL  time.Duration
	refreshTTL time.Duration
	now        func() time.Time
}

func NewSigner(secret []byte, accessTTL, refreshTTL time.Duration) *Signer {
	return &Signer{secret: secret, accessTTL: accessTTL, refreshTTL: refreshTTL, now: time.Now}
}

// WithClock makes the signer read the time from now, both when it stamps a
// token and when it checks one's expiry, so a test can cross a second
// boundary without sleeping. It returns s.
func (s *Signer) WithClock(now func() time.Time) *Signer {
	s.now = now
	return s
}

func (s *Signer) IssueAccessToken(userID, tenantID uuid.UUID) (string, error) {
	return s.issue(userID, tenantID, nil, tokenTypeAccess, s.accessTTL)
}

func (s *Signer) IssueRefreshToken(userID, tenantID uuid.UUID) (string, error) {
	return s.issue(userID, tenantID, nil, tokenTypeRefresh, s.refreshTTL)
}

// IssueSessionAccessToken is IssueAccessToken carrying sess's auth time and
// session id. A nil sess, or zero fields, leave those claims out.
func (s *Signer) IssueSessionAccessToken(userID, tenantID uuid.UUID, sess *Session) (string, error) {
	return s.issue(userID, tenantID, sess, tokenTypeAccess, s.accessTTL)
}

// IssueSessionRefreshToken is IssueRefreshToken carrying sess's auth time
// and session id. A nil sess, or zero fields, leave those claims out.
func (s *Signer) IssueSessionRefreshToken(userID, tenantID uuid.UUID, sess *Session) (string, error) {
	return s.issue(userID, tenantID, sess, tokenTypeRefresh, s.refreshTTL)
}

func (s *Signer) issue(userID, tenantID uuid.UUID, sess *Session, tokenType string, ttl time.Duration) (string, error) {
	now := s.now()
	claims := Claims{
		UserID:   userID,
		TenantID: tenantID,
		Type:     tokenType,
		RegisteredClaims: jwt.RegisteredClaims{
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
		},
	}
	if sess != nil {
		if !sess.AuthTime.IsZero() {
			claims.AuthTime = jwt.NewNumericDate(sess.AuthTime)
		}
		claims.SessionID = sess.ID
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString(s.secret)
	if err != nil {
		return "", fmt.Errorf("auth: sign token: %w", err)
	}
	return signed, nil
}

func (s *Signer) ParseAccessToken(raw string) (Claims, error) {
	return s.parse(raw, tokenTypeAccess)
}

func (s *Signer) ParseRefreshToken(raw string) (Claims, error) {
	return s.parse(raw, tokenTypeRefresh)
}

// parse rejects tokens signed with anything but HS256 (algorithm-confusion
// guard) and enforces tokenType, so a refresh token can't be replayed as an
// access token or vice versa.
func (s *Signer) parse(raw string, tokenType string) (Claims, error) {
	var claims Claims
	_, err := jwt.ParseWithClaims(raw, &claims, func(t *jwt.Token) (any, error) {
		return s.secret, nil
	}, jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Name}), jwt.WithTimeFunc(s.now))
	if err != nil {
		return Claims{}, ErrInvalidToken
	}
	if claims.Type != tokenType {
		return Claims{}, ErrInvalidToken
	}
	return claims, nil
}
