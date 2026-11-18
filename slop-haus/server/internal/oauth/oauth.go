// Package oauth is slop-haus's OAuth 2.1 authorization server, which is
// how agents are credentialed.
//
// A signed-in user authorizes a client to act as one of their tenant's
// agents. Two clients matter: Claude Code's MCP connection, which follows
// the MCP authorization spec (protected resource metadata, dynamic client
// registration, the authorization code grant with PKCE), and a workspace's
// hooks and scripts, which use the device grant (RFC 8628) because they run
// on machines without a browser. Either way the result is a grant, and
// short-lived access tokens with rotating refresh tokens under it.
//
// Deliberately small: public clients only (no secrets; PKCE S256 does that
// job), opaque tokens checked against the database on every request (so
// revoking a grant or disabling an agent takes effect at once), and no
// OpenID Connect -- nothing here identifies a person to a third party.
package oauth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/agent"
	"net"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/go-auth/auth"
)

// Grant types, as registered clients and token requests name them.
const (
	GrantAuthorizationCode = "authorization_code"
	GrantRefreshToken      = "refresh_token"
	GrantDeviceCode        = "urn:ietf:params:oauth:grant-type:device_code"
	// GrantClientCredentials is only ever a read-only client's, made by an
	// admin (cmd/smoke_client) -- never open to registration.
	GrantClientCredentials = "client_credentials"
)

// Scopes: what a grant's tokens may do.
const (
	// ScopeAgent is everything an agent does.
	ScopeAgent = "agent"
	// ScopeRead may call read-only MCP tools and nothing else -- for a
	// machine checking a deployment works, with nothing to break if its
	// secret leaks.
	ScopeRead = "read"
)

// supervisorKind is the agent_kind a consent names to create a supervisor.
const supervisorKind = "supervisor"

// WorkspaceClientID is the client every workspace's hooks and scripts use,
// seeded by the migration rather than registered per workspace.
const WorkspaceClientID = "slop-haus-workspace"

// Client is a registered client: public (no secret -- every registered
// one, and the workspace client), or a read-only confidential client an
// admin made, authenticated by its secret.
type Client struct {
	ID           string
	Name         string
	RedirectURIs []string
	GrantTypes   []string
	// SecretHash is empty for a public client.
	SecretHash string
}

func (c *Client) allows(grantType string) bool {
	for _, g := range c.GrantTypes {
		if g == grantType {
			return true
		}
	}
	return false
}

func (c *Client) hasRedirect(uri string) bool {
	for _, r := range c.RedirectURIs {
		if r == uri {
			return true
		}
	}
	return false
}

// Grant is one client authorized to act as one agent.
type Grant struct {
	ID         uuid.UUID
	TenantID   uuid.UUID
	AgentID    uuid.UUID
	ClientID   string
	ClientName string
	ApprovedBy *uuid.UUID
	Resource   string
	// Scope is ScopeAgent or ScopeRead.
	Scope      string
	CreatedAt  time.Time
	LastUsedAt *time.Time
	// TenantArchived is set when the grant's tenant has been archived: the
	// grant survives, so unarchiving restores it, but nothing is issued or
	// accepted under it meanwhile.
	TenantArchived bool
	// AgentName and AgentKind are the agent the grant acts as, read by the
	// listing only.
	AgentName string
	AgentKind string
}

// Access is what an access token resolves to. Declared in agent, which
// consumes it, so agent does not import this package.
type Access = agent.Access

// AuthRequest is an authorization request waiting on the user's decision.
type AuthRequest struct {
	ID            uuid.UUID
	ClientID      string
	RedirectURI   string
	CodeChallenge string
	State         string
	Resource      string
	ExpiresAt     time.Time
}

// DeviceCode is a device authorization waiting on, or given, a decision.
type DeviceCode struct {
	ID         uuid.UUID
	UserCode   string
	ClientID   string
	ClientName string
	Resource   string
	ExpiresAt  time.Time
	// StartedFrom is the client address that started it; the zero Addr
	// when unknown.
	StartedFrom netip.Addr
	CreatedAt   time.Time
}

// Error is an OAuth error response (RFC 6749 section 5.2): a code from the
// spec's fixed list, a description for humans, and the HTTP status.
type Error struct {
	Code        string
	Description string
	Status      int
}

func (e *Error) Error() string { return "oauth: " + e.Code + ": " + e.Description }

func oauthErr(status int, code, description string) *Error {
	return &Error{Code: code, Description: description, Status: status}
}

var (
	errInvalidGrant      = oauthErr(400, "invalid_grant", "the code or token is invalid, expired, revoked or already used")
	errTenantArchived    = oauthErr(400, "invalid_grant", "this tenant was archived")
	errPending           = oauthErr(400, "authorization_pending", "the user has not decided yet")
	errSlowDown          = oauthErr(400, "slow_down", "polling too fast")
	errDenied            = oauthErr(400, "access_denied", "the user denied the request")
	errExpiredToken      = oauthErr(400, "expired_token", "the device code has expired")
	errInvalidClient     = oauthErr(401, "invalid_client", "unknown client")
	errUnauthorizedGrant = oauthErr(400, "unauthorized_client", "the client may not use this grant type")
	errInvalidTarget     = oauthErr(400, "invalid_target", "tokens are only issued for this server's MCP endpoint")
	errRateLimited       = oauthErr(429, "slow_down", "too many requests")
	errRegistrationFull  = oauthErr(503, "temporarily_unavailable", "too many clients are registered and not yet authorized; try again later")
)

// ReusedError is the invalid_grant a store answers a spent code or refresh
// token presented again with, after revoking the grant it belonged to. It
// is invalid_grant to the client, like any other dead credential; the
// service tells it apart only to record the reuse.
type ReusedError struct {
	// Kind is "code" or "refresh".
	Kind    string
	GrantID uuid.UUID
}

func (e *ReusedError) Error() string { return errInvalidGrant.Error() + " (reused " + e.Kind + ")" }
func (e *ReusedError) Unwrap() error { return errInvalidGrant }

// errRefreshSpent is a store's answer to a refresh token that is not live.
// It is not yet a reuse: a token rotated moments ago may be a retry.
var errRefreshSpent = fmt.Errorf("oauth: refresh token not live")

// ErrNotFound means no such pending request, device code or grant.
var ErrNotFound = agent.ErrGrantNotFound

// ErrNoApprover means a supervisor's grants record no person who approved
// them (the account was deleted), so a credential issued under them would
// name nobody.
var ErrNoApprover = agent.ErrGrantNoApprover

// ErrSlotCredentialsOff means the service was built without a slot
// credential limit, which turns issuing them off.
var ErrSlotCredentialsOff = fmt.Errorf("oauth: slot credentials are not configured")

// ErrTooManySlotCredentials means a supervisor asked for slot credentials
// faster than the limit allows.
var ErrTooManySlotCredentials = agent.ErrTooManySlotCredentials

// ErrTooManyClients means registration is at its cap of clients never
// authorized.
var ErrTooManyClients = fmt.Errorf("oauth: too many unclaimed clients")

// newSecret returns a random token and the hash it is stored as.
func newSecret() (raw, hash string, err error) {
	return auth.NewOpaqueToken()
}

func hashSecret(raw string) string { return auth.HashOpaqueToken(raw) }

// verifyPKCE checks an S256 code verifier against its challenge
// (RFC 7636). The verifier's shape is checked too: a short one would make
// the challenge guessable.
func verifyPKCE(verifier, challenge string) bool {
	if len(verifier) < 43 || len(verifier) > 128 {
		return false
	}
	for _, r := range verifier {
		if !(r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || strings.ContainsRune("-._~", r)) {
			return false
		}
	}
	sum := sha256.Sum256([]byte(verifier))
	computed := base64.RawURLEncoding.EncodeToString(sum[:])
	return subtle.ConstantTimeCompare([]byte(computed), []byte(challenge)) == 1
}

// userCodeAlphabet has no vowels, so no code spells a word, and no
// look-alikes. Twenty letters, eight of them: 2.5e10 codes, against a
// lookup that is rate limited and a code that lives ten minutes.
const userCodeAlphabet = "BCDFGHJKLMNPQRSTVWXZ"

func newUserCode() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("oauth: user code: %w", err)
	}
	var sb strings.Builder
	for i, v := range b {
		if i == 4 {
			sb.WriteByte('-')
		}
		// 256 is not a multiple of 20; the bias this leaves is a fraction
		// of a percent per letter, immaterial for a ten-minute code.
		sb.WriteByte(userCodeAlphabet[int(v)%len(userCodeAlphabet)])
	}
	return sb.String(), nil
}

// NormalizeUserCode accepts a code as a person types it: any case, with or
// without the dash or spaces.
func NormalizeUserCode(s string) string {
	s = strings.ToUpper(strings.NewReplacer("-", "", " ", "").Replace(s))
	if len(s) != 8 {
		return s
	}
	return s[:4] + "-" + s[4:]
}

// validRedirectURI accepts only loopback http redirects (RFC 8252 section
// 7.3). An approval's code goes wherever the redirect points, so a client
// allowed to name any https URL could be registered by anyone, called
// "Claude Code", and handed to a user to approve -- delivering an agent's
// credentials to its author. Loopback keeps the code on the approving
// user's own machine, which is where Claude Code listens.
func validRedirectURI(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" || u.User != nil || u.Fragment != "" {
		return false
	}
	host := u.Hostname()
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
