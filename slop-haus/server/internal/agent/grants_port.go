package agent

import (
	"errors"

	"github.com/google/uuid"
)

// What agent needs from the OAuth service, declared here so oauth depends on
// agent (it picks agents on the consent page) and not the reverse. oauth
// aliases these types and errors, so its callers are unchanged.

// Access is what an access token resolves to.
type Access struct {
	TenantID uuid.UUID
	AgentID  uuid.UUID
	// ReadOnly is a ScopeRead token: it may read, and nothing else.
	ReadOnly bool
}

// TokenResponse is a successful token response (RFC 6749 section 5.1).
type TokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int    `json:"expires_in"`
	// RefreshToken is omitted by the client credentials grant, whose
	// client re-authenticates with its secret instead.
	RefreshToken string `json:"refresh_token,omitempty"`
}

// SlotCredential is a fresh credential for one of a supervisor's slots.
type SlotCredential struct {
	Tokens  *TokenResponse
	GrantID uuid.UUID
	// ApprovedBy is the person who approved the supervisor, whom the new grant
	// is recorded as approved by.
	ApprovedBy uuid.UUID
}

var (
	// ErrGrantNotFound means no such pending request, device code or grant.
	ErrGrantNotFound = errors.New("oauth: not found")
	// ErrGrantNoApprover means a supervisor's grants record no person who
	// approved them (the account was deleted), so a credential issued under
	// them would name nobody.
	ErrGrantNoApprover = errors.New("oauth: the supervisor has no approver on record")
	// ErrTooManySlotCredentials means a supervisor asked for slot credentials
	// faster than the limit allows.
	ErrTooManySlotCredentials = errors.New("oauth: too many slot credentials requested")
)

// What an Agents implementation returns to the OAuth registration path.
var (
	// ErrAgentUnavailable: no such enabled agent.
	ErrAgentUnavailable = errors.New("oauth: no such enabled agent")
	// ErrAgentNameTaken: an agent with that name already exists.
	ErrAgentNameTaken = errors.New("oauth: an agent with that name already exists")
	// ErrAgentKindInvalid: a new agent can be a worker, a console or a supervisor.
	ErrAgentKindInvalid = errors.New("oauth: a new agent can be a worker, a console or a supervisor")
	// ErrSlotCapInvalid: slot_cap is outside the allowed range.
	ErrSlotCapInvalid = errors.New("oauth: slot_cap is outside the allowed range")
	// ErrReplaceUnavailable: that agent cannot be replaced.
	ErrReplaceUnavailable = errors.New("oauth: that agent cannot be replaced")
)
