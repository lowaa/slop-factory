package config

import "time"

// The values below are policy set in code rather than configuration: each
// is a judgement kept next to its reason, and changing one is a change to
// review, not a setting to flip.

// RateLimit allows Limit requests per Window from one key (an address, a
// client, an account: whatever the limiter counts by). Name labels it in
// the logs and metrics.
type RateLimit struct {
	Name   string
	Limit  int
	Window time.Duration
}

// LimiterMaxKeys bounds the memory a flood of distinct clients can take
// from any one limiter; past it they share one overflow bucket. Every limit
// is in memory, so restarting the api clears them all -- the way to let
// someone locked out back in (see the infra README's "Getting back in").
const LimiterMaxKeys = 100_000

// SignInLimits are the limits on people signing in and on the mail the
// board sends them.
type SignInLimits struct {
	Login        RateLimit
	Mail         RateLimit
	LoginClient  RateLimit
	MailClient   RateLimit
	Invite       RateLimit
	TenantCreate RateLimit
}

// SignInLimits are the sign-in limits. The per-client counts are settings
// (see LoginClientLimit), their windows are not.
func (c *Config) SignInLimits() *SignInLimits {
	return &SignInLimits{
		// Ten guesses a minute from one client at one address: a person
		// mistyping is never held up for long, and a guesser is slowed to
		// a crawl. Five emails an hour is more than anyone waiting on a
		// link needs.
		Login: RateLimit{Name: "login", Limit: 10, Window: time.Minute},
		Mail:  RateLimit{Name: "mail", Limit: 5, Window: time.Hour},
		// Across addresses, from one client (an IPv6 /64 counts as one):
		// enough for a household or office behind one address to sign in
		// and mistype, far too few to spray one password across accounts.
		LoginClient: RateLimit{Name: "login_client", Limit: c.LoginClientLimit, Window: 10 * time.Minute},
		MailClient:  RateLimit{Name: "mail_client", Limit: c.MailClientLimit, Window: time.Hour},
		// Invites are sent by signed-in users to addresses of their
		// choosing; thirty a day covers onboarding a team without making
		// an account a way to mail strangers in bulk.
		Invite: RateLimit{Name: "invite", Limit: 30, Window: 24 * time.Hour},
		// On top of the owned-tenant cap: stops a script from churning
		// through tenant creation, and gives an abusive account little room.
		TenantCreate: RateLimit{Name: "tenant_create", Limit: 10, Window: time.Hour},
	}
}

// AgentOAuthPolicy is the policy of the OAuth server agents sign in
// through. A person's session is a different credential, with lifetimes of
// its own (AccessTokenTTL, RefreshTokenTTL).
type AgentOAuthPolicy struct {
	AccessTTL           time.Duration
	RefreshTTL          time.Duration
	CodeTTL             time.Duration
	AuthRequestTTL      time.Duration
	DeviceTTL           time.Duration
	PollInterval        time.Duration
	RegisterLimit       RateLimit
	DeviceLookupLimit   RateLimit
	DeviceStartLimit    RateLimit
	SlotCredentialLimit RateLimit
	MaxUnclaimedClients int
}

// AgentOAuth is agents' OAuth policy.
func AgentOAuth() *AgentOAuthPolicy {
	return &AgentOAuthPolicy{
		// An access token sits in a hook script's environment; a short
		// life bounds what a leaked one is worth. The refresh token rotates
		// on every use, so a month is how long an idle workspace stays
		// signed in, not how long any one token is good for.
		AccessTTL:      15 * time.Minute,
		RefreshTTL:     30 * 24 * time.Hour,
		CodeTTL:        time.Minute,
		AuthRequestTTL: 10 * time.Minute,
		// Short, because a code someone else started and sent to a member
		// is only good for as long as this (RFC 8628 section 5.4).
		DeviceTTL:         5 * time.Minute,
		PollInterval:      5 * time.Second,
		RegisterLimit:     RateLimit{Name: "oauth_register", Limit: 20, Window: time.Hour},
		DeviceLookupLimit: RateLimit{Name: "device_lookup", Limit: 20, Window: 10 * time.Minute},
		// A workspace starts one device sign-in when it is set up, or
		// again when its grant is revoked; sixty an hour from one address
		// is a machine stuck in a loop, not a person.
		DeviceStartLimit: RateLimit{Name: "device_start", Limit: 60, Window: time.Hour},
		// A supervisor asks for a credential when it starts a slot's run or
		// the slot's refresh token has lapsed; with a handful of slots that is
		// a few an hour. Sixty is a runaway supervisor, or a stolen credential.
		SlotCredentialLimit: RateLimit{Name: "slot_credential", Limit: 60, Window: time.Hour},
		MaxUnclaimedClients: 1000,
	}
}

// MailOutboxPolicy sizes the background queue the board's mail is sent
// from.
type MailOutboxPolicy struct {
	QueueSize   int
	SendTimeout time.Duration
}

// MailOutbox is the mail outbox's policy.
func MailOutbox() *MailOutboxPolicy {
	return &MailOutboxPolicy{QueueSize: 256, SendTimeout: 30 * time.Second}
}

// LLMUsageLimit is how many usage reports one agent may send. A harness
// reports when it settles, when a subagent finishes and when a session
// ends -- a few a minute at most, each a full set of totals, so a refused
// report loses nothing the next one won't carry. Two a second, held up for
// a minute, is a client stuck in a loop.
func LLMUsageLimit() RateLimit {
	return RateLimit{Name: "llm_usage", Limit: 120, Window: time.Minute}
}

// OtelProxyLimit is how many log exports one client address may send the
// browser-facing /api/otel proxy. The frontend's exporter sends a batch every
// few seconds while errors are happening and nothing otherwise, so thirty a
// minute is generous for a real page; it exists to bound what a hostile
// client can push into the log store, per address.
func OtelProxyLimit() RateLimit {
	return RateLimit{Name: "otel_proxy", Limit: 30, Window: time.Minute}
}

// UIUsageLimit is how many batches of browser usage events one signed-in user
// may send. The page sends a batch when it is hidden or after fifty events,
// so a person switching tabs all day stays well inside this; it bounds what a
// script holding a session can push through the counters.
func UIUsageLimit() RateLimit {
	return RateLimit{Name: "ui_usage", Limit: 60, Window: time.Minute}
}

// ReviewReportLimit is how many slop-review runs and finding labels one agent
// or supervisor may post. A review takes minutes and posts one run, plus up to
// twenty spooled ones it retries, so even a supervisor posting for each of its
// slots sends a handful a minute; an agent then labels each finding, a few
// dozen at most. A refused label is not retried, so the limit sits well above
// any real burst: it bounds a client stuck in a loop.
func ReviewReportLimit() RateLimit {
	return RateLimit{Name: "review_report", Limit: 300, Window: time.Minute}
}
