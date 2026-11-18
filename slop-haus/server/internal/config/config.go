// Package config is the single place slop-haus/server reads environment
// variables and CLI flags; every other package receives fully-parsed config
// rather than calling os.Getenv/flag itself.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/pflag"
	"github.com/spf13/viper"

	"github.com/lowaa/slop-factory/go-auth/clientip"
	"github.com/lowaa/slop-factory/go-auth/email"
	"github.com/lowaa/slop-factory/go-auth/login"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/change"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/pendingaction"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/prune"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/secretbox"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

type Config struct {
	// DatabaseURL is the owning role's DSN. It runs the migrations and
	// creates the app role, then the server serves every request as the
	// app role instead: row level security does not bind a table's owner,
	// so serving as the owner would let every tenant read every other's
	// rows.
	DatabaseURL   string
	AppDBRole     string
	AppDBPassword string
	// BuildTag is the deploy tag this binary was built as, baked into the
	// image (BUILD_TAG, see ../Dockerfile) and reported at /api/version so
	// a deploy can tell when it is the one serving. "dev" when unset.
	BuildTag string
	// SupervisorPackageDir holds the supervisor package the server serves
	// (the image puts it at /srv/supervisor). A directory that is absent
	// means a server not built from the image, which does not ship it.
	SupervisorPackageDir string
	ListenAddr           string
	// MetricsListenAddr is where /metrics is served, on a listener of its
	// own: the board's doors all lead to ListenAddr, so nothing in front
	// of the server can route a request here by mistake. Empty serves no
	// metrics.
	MetricsListenAddr string
	// LogsOTLPEndpoint is the OTLP/HTTP URL logs are exported to as well
	// as stderr (the collector's /v1/logs). Empty
	// exports nothing.
	LogsOTLPEndpoint string

	// PublicBaseURL is where the board is served, and where emailed links
	// point. Its scheme also decides whether session cookies are Secure.
	PublicBaseURL string
	// TrustedProxies are the ranges whose X-Forwarded-For is believed when
	// working out a client's address (see clientip). On the cluster that
	// is the pod network, where the Tailscale proxy and the frontend's nginx run.
	TrustedProxies []string
	// AllowInsecureHTTP permits a plain-http PublicBaseURL on a host other
	// than loopback -- the e2e stack, where the browser reaches the board
	// by a compose service name. Anywhere real, http would mean session
	// cookies sent in the clear.
	AllowInsecureHTTP bool

	// JWTSecret signs session tokens. Anyone holding it can sign in as
	// anyone, so it comes from a secret, and is refused if it is short
	// enough to guess.
	JWTSecret string
	// AccessTokenTTL and RefreshTokenTTL are a signed-in person's session.
	// Agents' OAuth tokens are a different credential with their own
	// lifetimes, set with the rest of their policy in AgentOAuth.
	AccessTokenTTL  time.Duration
	RefreshTokenTTL time.Duration
	ActivationTTL   time.Duration
	ResetTTL        time.Duration
	InviteTTL       time.Duration
	// RegistrationOpen lets anyone sign up; off, accounts start from an
	// invite. On for development and the e2e suite, off anywhere real.
	RegistrationOpen bool
	// AttemptsAddEnabled serves the endpoint that adds an attempt to a ticket
	// (POST /api/tickets/{id}/attempts). On unless set to false; set false
	// only to switch it off, when it answers 404 as if the route did not
	// exist.
	AttemptsAddEnabled bool
	// ReplaysEnabled lets the add-attempt endpoint take replay: true on a done
	// ticket. Off unless set to true, while replays are unfinished (C7); off,
	// a replay request answers 404 feature_disabled.
	ReplaysEnabled bool
	// SetupClaimGate refuses an agent's claim in a tenant with no repo in its
	// workspace manifest and no code host connected. On unless set to false;
	// the e2e suite and tests that claim in bare tenants switch it off.
	SetupClaimGate bool
	// AttemptsPromoteEnabled serves the endpoint that promotes an attempt
	// (POST /api/tickets/{id}/attempts/{n}/promote). On unless set to false;
	// set false only to switch it off, when it answers 404.
	AttemptsPromoteEnabled bool
	// ServerMergeEnabled lets the server merge a ticket's change itself when
	// a person approves it, for a tenant with a code host credential, instead
	// of leaving the merge to the author's agent. On unless set to false; set
	// false only to switch it off, when nothing is registered and an approval
	// behaves as it always has.
	ServerMergeEnabled bool
	// MergeQueueEnabled is the kill switch for the landing runner: off, no
	// tenant is served whatever its merge_method says. On unless set to false.
	MergeQueueEnabled bool
	// MergeQueueApprovalEnabled enqueues an approved ticket's change in the
	// landing queue, for a tenant on the queue, instead of servermerge/runner
	// merging it. Off unless set to true.
	MergeQueueApprovalEnabled bool
	// Woodpecker is the CI the landing runner reads and retries pipelines
	// through. Without WOODPECKER_URL the runner relies on the code host's
	// check statuses alone.
	Woodpecker WoodpeckerConfig
	// Attachments is the object store ticket attachments are kept in. Empty,
	// the attachment routes answer 503.
	Attachments AttachmentsConfig
	// LandingKnownFlaky names the CI workflows whose red the merge queue
	// retries once, each as workflow:owner-ticket:review-date (a bare name
	// still works, with a warning).
	LandingKnownFlaky []string
	// PasswordSignIn offers email and password sign-in. Off, people sign
	// in only through GitHub or OIDC and so inherit that provider's second
	// factor; slop-haus has none of its own. Off wherever the board is
	// reachable from the internet.
	PasswordSignIn bool
	// LoginClientLimit and MailClientLimit are how many sign-ins (per 10
	// minutes) and emails (per hour) one client may ask for across every
	// address. Settings rather than constants for the e2e suite, whose
	// browser signs in as dozens of accounts from one address.
	LoginClientLimit int
	MailClientLimit  int
	// TenantOwnerLimit is how many tenants a signed-in user may create for
	// themselves (those they own and have not archived); 0 turns
	// self-service creation off. An admin's new_tenant invite is not held to
	// it.
	TenantOwnerLimit int
	// TenantOwnerTotalLimit is the same count including archived tenants, so
	// archiving and creating again can't go on for ever. At least
	// TenantOwnerLimit.
	TenantOwnerTotalLimit int
	// BulkMaxTickets is the most tickets one bulk action on the board may
	// name. A bulk move holds the claim lock for its whole batch, blocking
	// every agent's claim until it commits.
	BulkMaxTickets int
	// ResumeAffinity is how long the agent that handed a ticket to a human
	// keeps first claim once they answer. Zero opens no window.
	ResumeAffinity time.Duration
	// Cooldown bounds how long a ticket_release holds a ticket out of the
	// queue, and how many cooldown releases in a row it gets before it is
	// moved to discussion.
	Cooldown ticket.CooldownPolicy
	// ExperimentReconcileInterval is how often the closing rule is checked
	// for experiment changes the transitions missed; zero switches the
	// reconciler off.
	ExperimentReconcileInterval time.Duration
	// CooldownSweepInterval is how often expired cooldowns are looked for.
	CooldownSweepInterval time.Duration
	// Proposals bounds what a console may put in the person's approvals queue.
	Proposals pendingaction.Config

	Email email.Config

	// GitHub and OIDC are the login providers people may sign in with
	// besides a password; either, both or neither may be configured. Nil
	// when not configured.
	GitHub *GitHubConfig
	OIDC   *OIDCConfig

	// Webhook is the GitHub App webhook door; nil when
	// WEBHOOK_PUBLIC_BASE_URL is unset, which leaves the door shut rather
	// than refusing to start.
	Webhook *WebhookConfig

	// Intake is the door external systems open tickets through; nil when
	// INTAKE_LISTEN_ADDR is unset, which shuts it.
	Intake *IntakeConfig

	// Credentials is the key code host credentials are stored encrypted
	// under; nil when CREDENTIAL_ENCRYPTION_KEY is unset, which means the
	// server keeps none, so the key can be created after a deploy that
	// expects it.
	Credentials *CredentialsConfig
	// GitHubAPIBaseURL is where the server talks to GitHub as a tenant's
	// App. Server configuration only: every such call carries a JWT signed
	// with a tenant's key, so no tenant or request may choose where it goes.
	GitHubAPIBaseURL string
	// GitHubWebBaseURL is GitHub's website, which the board sends an owner's
	// browser to when creating and installing the App. Server configuration
	// only, as the API's is: the browser is sent there carrying a manifest.
	GitHubWebBaseURL string

	// Prune is the retention policy: sessions and leases grow with agent
	// activity. Tickets are never pruned: they are the
	// work record, and deleting them is an archival decision a human makes
	// on purpose.
	Prune prune.Config
}

// GitHubConfig is a GitHub OAuth app. Its callback URL is
// PUBLIC_BASE_URL/api/auth/callback/github.
type GitHubConfig struct {
	ClientID     string
	ClientSecret string
}

// OIDCConfig is an OpenID Connect issuer (Dex, Keycloak, ...). Its
// callback URL is PUBLIC_BASE_URL/api/auth/callback/<ProviderName>.
type OIDCConfig struct {
	IssuerURL    string
	ClientID     string
	ClientSecret string
	// ProviderName is the namespace this issuer's identities are stored
	// under. It must stay tied to one issuer for the life of the
	// deployment -- subjects are only unique per issuer -- which cmd/api
	// enforces at every startup (go-auth login.IssuerBindings).
	ProviderName string
	// DisplayName names the provider on the sign-in button.
	DisplayName string
}

// WebhookConfig is the GitHub webhook door (internal/githubhook): a
// listener of its own, meant to be the only thing reachable from the
// internet.
type WebhookConfig struct {
	// ListenAddr is the webhook listener's own address. Nothing else is
	// served on it.
	ListenAddr string
	// PublicBaseURL is where GitHub reaches that listener -- the public
	// hostname in front of it, not the board's -- for the URL given to each
	// tenant's GitHub App.
	PublicBaseURL string
	// ClientLimitPerMinute is how many requests one client address may
	// make to the listener per minute, counted before anything is read.
	ClientLimitPerMinute int
}

// defaultGitHubAPIBaseURL is api.github.com, the root githubapp.DefaultAPIBaseURL
// also names.
const defaultGitHubAPIBaseURL = "https://api.github.com"

// defaultGitHubWebBaseURL is github.com, the root githubapp.DefaultWebBaseURL
// also names.
const defaultGitHubWebBaseURL = "https://github.com"

// CredentialsConfig is the master key for secrets kept in the database
// (internal/secretbox), already decoded.
type CredentialsConfig struct {
	// Key seals every new secret and opens those sealed under it.
	Key []byte
	// PreviousKey, when set, only opens: what it sealed is re-sealed under
	// Key the next time a credential is verified or replaced, after which
	// it can be dropped. Separate from JWT_SECRET and the other keys, so
	// rotating one does not rotate the others.
	PreviousKey []byte
}

// IntakeConfig is the intake door (internal/intake): a listener of its own
// for external systems to open, update and resolve tickets. Like the
// GitHub door it may be reached from the internet, so every request is
// counted per client address before it is read.
type IntakeConfig struct {
	// ListenAddr is the intake listener's own address. Nothing else is
	// served on it.
	ListenAddr string
	// PublicBaseURL is where senders reach that listener, for the URL the
	// board shows.
	PublicBaseURL string
	// Key derives every HMAC source's signing secret; empty means a source
	// can only authenticate with a bearer token. Separate from JWT_SECRET
	// so rotating one does not change the other.
	Key string
	// RatePerMinute and Burst are each source's request rate limit.
	RatePerMinute int
	Burst         int
	// ClientLimitPerMinute is how many requests one client address may
	// make to the listener per minute, counted before anything is read.
	// RatePerMinute above counts per source, after authentication.
	ClientLimitPerMinute int
}

// PasswordMethod names the password sign-in method wherever a provider
// name would otherwise go, so it is reserved too.
const PasswordMethod = "password"

const defaultOIDCProviderName = "oidc"

var oidcProviderNameRE = regexp.MustCompile(`^[a-z][a-z0-9-]{0,30}$`)

// ValidateOIDCProviderName checks an OIDC identity namespace: short and
// plain -- it is a URL path segment -- and never "github", whose subjects
// are GitHub's numeric user IDs (an issuer sharing that namespace could
// mint a subject that signs in as a GitHub user), nor "password".
func ValidateOIDCProviderName(name string) error {
	if !oidcProviderNameRE.MatchString(name) {
		return fmt.Errorf("%q must match %s", name, oidcProviderNameRE)
	}
	if name == login.GitHubProviderName {
		return fmt.Errorf("%q is reserved for GitHub", name)
	}
	if name == PasswordMethod {
		return fmt.Errorf("%q is reserved for password sign-in", name)
	}
	return nil
}

// AttachmentsConfig is the attachment object store and sweep. The keys are
// read from a file (a mounted Secret) so they are in no environment or flag
// listing.
type AttachmentsConfig struct {
	S3Endpoint        string
	S3Region          string
	S3Bucket          string
	S3CredentialsFile string
	// TmpDir is where an upload is spooled while it is checked; the OS's when empty.
	TmpDir        string
	SweepInterval time.Duration
	UploadGrace   time.Duration
}

// Enabled says whether an object store is configured.
func (a AttachmentsConfig) Enabled() bool { return a.S3Endpoint != "" }

// WoodpeckerConfig is the CI adapter's settings. The token is read from a file
// (a mounted Secret) so it is in no environment or flag listing.
type WoodpeckerConfig struct {
	URL        string
	TokenFile  string
	RepoID     int64
	BaseBranch string
}

// minJWTSecretBytes is HS256's key size: a shorter secret is weaker than
// the signature it keys.
const minJWTSecretBytes = 32

// flagSpec ties a CLI flag to the env var/viper key it mirrors, plus its
// default. Single table so the flag set and the env bindings can't drift
// apart from each other.
type flagSpec struct {
	flag string
	key  string // viper key; also the env var name (see Load)
	def  string
}

var flagSpecs = []flagSpec{
	{"database-url", "DATABASE_URL", ""},
	{"build-tag", "BUILD_TAG", "dev"},
	{"supervisor-package-dir", "SUPERVISOR_PACKAGE_DIR", "/srv/supervisor"},
	{"listen-addr", "LISTEN_ADDR", ":8080"},
	{"metrics-listen-addr", "METRICS_LISTEN_ADDR", ""},
	{"logs-otlp-endpoint", "LOGS_OTLP_ENDPOINT", ""},
	{"app-db-role", "APP_DB_ROLE", "slophaus_app"},
	{"app-db-password", "APP_DB_PASSWORD", ""},
	{"public-base-url", "PUBLIC_BASE_URL", ""},
	{"allow-insecure-http", "ALLOW_INSECURE_HTTP", "false"},
	{"trusted-proxies", "TRUSTED_PROXIES", ""},
	{"jwt-secret", "JWT_SECRET", ""},
	{"access-token-ttl", "ACCESS_TOKEN_TTL", "15m"},
	{"refresh-token-ttl", "REFRESH_TOKEN_TTL", "168h"},
	{"activation-ttl", "ACTIVATION_TTL", "24h"},
	{"reset-ttl", "RESET_TTL", "1h"},
	{"invite-ttl", "INVITE_TTL", "168h"},
	{"registration-open", "REGISTRATION_OPEN", "false"},
	{"password-signin", "PASSWORD_SIGNIN", "true"},
	{"attempts-add-enabled", "ATTEMPTS_ADD_ENABLED", "true"},
	{"replays-enabled", "REPLAYS_ENABLED", "false"},
	{"setup-claim-gate", "SETUP_CLAIM_GATE", "true"},
	{"attempts-promote-enabled", "ATTEMPTS_PROMOTE_ENABLED", "true"},
	{"server-merge-enabled", "SERVER_MERGE_ENABLED", "true"},
	{"merge-queue-enabled", "MERGE_QUEUE_ENABLED", "true"},
	{"merge-queue-approval-enabled", "MERGE_QUEUE_APPROVAL_ENABLED", "false"},
	{"s3-endpoint", "S3_ENDPOINT", ""},
	{"s3-region", "S3_REGION", "us-east-1"},
	{"s3-bucket", "S3_BUCKET", "slop-haus-attachments"},
	{"s3-credentials-file", "S3_CREDENTIALS_FILE", ""},
	{"attachment-tmp-dir", "ATTACHMENT_TMP_DIR", ""},
	{"attachment-sweep-interval", "ATTACHMENT_SWEEP_INTERVAL", "15m"},
	{"attachment-upload-grace", "ATTACHMENT_UPLOAD_GRACE", "24h"},
	{"woodpecker-url", "WOODPECKER_URL", ""},
	{"woodpecker-token-file", "WOODPECKER_TOKEN_FILE", ""},
	{"woodpecker-repo-id", "WOODPECKER_REPO_ID", "0"},
	{"landing-known-flaky", "LANDING_KNOWN_FLAKY", ""},
	{"woodpecker-base-branch", "WOODPECKER_BASE_BRANCH", "main"},
	{"login-client-limit", "LOGIN_CLIENT_LIMIT", "30"},
	{"mail-client-limit", "MAIL_CLIENT_LIMIT", "20"},
	{"tenant-owner-limit", "TENANT_OWNER_LIMIT", "3"},
	{"tenant-owner-total-limit", "TENANT_OWNER_TOTAL_LIMIT", "10"},
	{"bulk-max-tickets", "BULK_MAX_TICKETS", strconv.Itoa(ticket.DefaultBulkMax)},
	{"resume-affinity", "RESUME_AFFINITY", ticket.DefaultResumeAffinity.String()},
	{"release-cooldown-min", "RELEASE_COOLDOWN_MIN", ticket.DefaultCooldownPolicy().Min.String()},
	{"release-cooldown-max", "RELEASE_COOLDOWN_MAX", ticket.DefaultCooldownPolicy().Max.String()},
	{"release-cooldown-escalate-after", "RELEASE_COOLDOWN_ESCALATE_AFTER", strconv.Itoa(ticket.DefaultCooldownPolicy().EscalateAfter)},
	{"release-cooldown-max-total", "RELEASE_COOLDOWN_MAX_TOTAL", ticket.DefaultCooldownPolicy().MaxTotal.String()},
	{"experiment-reconcile-interval", "EXPERIMENT_RECONCILE_INTERVAL", change.DefaultReconcileInterval.String()},
	{"cooldown-sweep-interval", "COOLDOWN_SWEEP_INTERVAL", ticket.DefaultCooldownSweepInterval.String()},
	{"email-backend", "EMAIL_BACKEND", ""},
	{"email-from", "EMAIL_FROM", ""},
	{"smtp-host", "SMTP_HOST", ""},
	{"smtp-port", "SMTP_PORT", "587"},
	{"smtp-user", "SMTP_USER", ""},
	{"smtp-pass", "SMTP_PASS", ""},
	// On unless a local relay needs it off: activation and reset links are
	// credentials, and opportunistic STARTTLS can be stripped in transit.
	{"smtp-require-tls", "SMTP_REQUIRE_TLS", "true"},
	// Login providers. Each is off unless its client credentials are set.
	{"github-client-id", "GITHUB_CLIENT_ID", ""},
	{"github-client-secret", "GITHUB_CLIENT_SECRET", ""},
	{"oidc-issuer-url", "OIDC_ISSUER_URL", ""},
	{"oidc-client-id", "OIDC_CLIENT_ID", ""},
	{"oidc-client-secret", "OIDC_CLIENT_SECRET", ""},
	{"oidc-provider-name", "OIDC_PROVIDER_NAME", defaultOIDCProviderName},
	{"oidc-display-name", "OIDC_DISPLAY_NAME", "Single sign-on"},
	// The GitHub App webhook door: off unless WEBHOOK_PUBLIC_BASE_URL is set.
	{"webhook-listen-addr", "WEBHOOK_LISTEN_ADDR", ":8081"},
	{"webhook-public-base-url", "WEBHOOK_PUBLIC_BASE_URL", ""},
	{"webhook-client-limit-per-minute", "WEBHOOK_CLIENT_LIMIT_PER_MINUTE", "300"},
	// Stored code host credentials: off unless CREDENTIAL_ENCRYPTION_KEY
	// (base64 of 32 bytes) is set.
	{"credential-encryption-key", "CREDENTIAL_ENCRYPTION_KEY", ""},
	{"credential-encryption-key-previous", "CREDENTIAL_ENCRYPTION_KEY_PREVIOUS", ""},
	{"github-api-base-url", "GITHUB_API_BASE_URL", defaultGitHubAPIBaseURL},
	{"github-web-base-url", "GITHUB_WEB_BASE_URL", defaultGitHubWebBaseURL},
	// The intake door: off unless INTAKE_LISTEN_ADDR is set.
	{"intake-listen-addr", "INTAKE_LISTEN_ADDR", ""},
	{"intake-public-base-url", "INTAKE_PUBLIC_BASE_URL", ""},
	{"intake-key", "INTAKE_KEY", ""},
	{"intake-rate-per-minute", "INTAKE_RATE_PER_MINUTE", "60"},
	{"intake-burst", "INTAKE_BURST", "20"},
	{"intake-client-limit-per-minute", "INTAKE_CLIENT_LIMIT_PER_MINUTE", "600"},
	{"session-retention", "SESSION_RETENTION", prune.DefaultSessionRetention.String()},
	{"lease-retention", "LEASE_RETENTION", prune.DefaultLeaseRetention.String()},
	{"prune-interval", "PRUNE_INTERVAL", prune.DefaultInterval.String()},
	{"github-delivery-retention", "GITHUB_DELIVERY_RETENTION", prune.DefaultDeliveryRetention.String()},
	{"llm-usage-retention", "LLM_USAGE_RETENTION", prune.DefaultLLMUsageRetention.String()},
	{"proposal-ttl", "PROPOSAL_TTL", pendingaction.DefaultTTL.String()},
	{"proposal-max-pending-per-agent", "PROPOSAL_MAX_PENDING_PER_AGENT", strconv.Itoa(pendingaction.DefaultMaxPendingPerAgent)},
	{"proposal-rate-per-minute", "PROPOSAL_RATE_PER_MINUTE", strconv.Itoa(pendingaction.DefaultRatePerMinute)},
	{"pending-action-retention", "PENDING_ACTION_RETENTION", prune.DefaultPendingActionRetention.String()},
	{"conversation-retention", "CONVERSATION_RETENTION", prune.DefaultConversationRetention.String()},
}

// Load reads configuration from CLI flags (highest precedence), then env
// vars, then defaults (viper's normal precedence order). args is the
// program's argument list excluding argv[0] (i.e. os.Args[1:]).
func Load(args []string) (*Config, error) {
	v := viper.New()

	fs := pflag.NewFlagSet("slop-haus-api", pflag.ContinueOnError)
	for _, s := range flagSpecs {
		fs.String(s.flag, "", fmt.Sprintf("overrides %s", s.key))
	}
	if err := fs.Parse(args); err != nil {
		return nil, fmt.Errorf("config: parse flags: %w", err)
	}

	// BindEnv per key (rather than v.AutomaticEnv()) so each viper key maps
	// to exactly the env var name docker-compose sets -- AutomaticEnv's
	// dash-to-underscore key munging is a no-op for these already-
	// underscored/single-word keys, but binding explicitly keeps that from
	// being an assumption we could get wrong later.
	for _, s := range flagSpecs {
		if err := v.BindEnv(s.key, s.key); err != nil {
			return nil, fmt.Errorf("config: bind env %s: %w", s.key, err)
		}
		if err := v.BindPFlag(s.key, fs.Lookup(s.flag)); err != nil {
			return nil, fmt.Errorf("config: bind flag %s: %w", s.flag, err)
		}
		if s.def != "" {
			v.SetDefault(s.key, s.def)
		}
	}

	credentials, err := credentialsFrom(v)
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}

	cfg := &Config{
		DatabaseURL:               v.GetString("DATABASE_URL"),
		BuildTag:                  v.GetString("BUILD_TAG"),
		SupervisorPackageDir:      v.GetString("SUPERVISOR_PACKAGE_DIR"),
		AppDBRole:                 v.GetString("APP_DB_ROLE"),
		AppDBPassword:             v.GetString("APP_DB_PASSWORD"),
		ListenAddr:                v.GetString("LISTEN_ADDR"),
		MetricsListenAddr:         v.GetString("METRICS_LISTEN_ADDR"),
		LogsOTLPEndpoint:          strings.TrimSpace(v.GetString("LOGS_OTLP_ENDPOINT")),
		PublicBaseURL:             strings.TrimRight(v.GetString("PUBLIC_BASE_URL"), "/"),
		AllowInsecureHTTP:         v.GetBool("ALLOW_INSECURE_HTTP"),
		TrustedProxies:            splitList(v.GetString("TRUSTED_PROXIES")),
		JWTSecret:                 v.GetString("JWT_SECRET"),
		AccessTokenTTL:            v.GetDuration("ACCESS_TOKEN_TTL"),
		RefreshTokenTTL:           v.GetDuration("REFRESH_TOKEN_TTL"),
		ActivationTTL:             v.GetDuration("ACTIVATION_TTL"),
		ResetTTL:                  v.GetDuration("RESET_TTL"),
		InviteTTL:                 v.GetDuration("INVITE_TTL"),
		RegistrationOpen:          v.GetBool("REGISTRATION_OPEN"),
		PasswordSignIn:            v.GetBool("PASSWORD_SIGNIN"),
		AttemptsAddEnabled:        v.GetBool("ATTEMPTS_ADD_ENABLED"),
		ReplaysEnabled:            v.GetBool("REPLAYS_ENABLED"),
		SetupClaimGate:            v.GetBool("SETUP_CLAIM_GATE"),
		AttemptsPromoteEnabled:    v.GetBool("ATTEMPTS_PROMOTE_ENABLED"),
		ServerMergeEnabled:        v.GetBool("SERVER_MERGE_ENABLED"),
		MergeQueueEnabled:         v.GetBool("MERGE_QUEUE_ENABLED"),
		MergeQueueApprovalEnabled: v.GetBool("MERGE_QUEUE_APPROVAL_ENABLED"),
		Attachments: AttachmentsConfig{
			S3Endpoint: v.GetString("S3_ENDPOINT"), S3Region: v.GetString("S3_REGION"),
			S3Bucket: v.GetString("S3_BUCKET"), S3CredentialsFile: v.GetString("S3_CREDENTIALS_FILE"),
			TmpDir:        v.GetString("ATTACHMENT_TMP_DIR"),
			SweepInterval: v.GetDuration("ATTACHMENT_SWEEP_INTERVAL"), UploadGrace: v.GetDuration("ATTACHMENT_UPLOAD_GRACE"),
		},
		Woodpecker: WoodpeckerConfig{
			URL: v.GetString("WOODPECKER_URL"), TokenFile: v.GetString("WOODPECKER_TOKEN_FILE"),
			RepoID: v.GetInt64("WOODPECKER_REPO_ID"), BaseBranch: v.GetString("WOODPECKER_BASE_BRANCH"),
		},
		LandingKnownFlaky:     splitList(v.GetString("LANDING_KNOWN_FLAKY")),
		LoginClientLimit:      v.GetInt("LOGIN_CLIENT_LIMIT"),
		MailClientLimit:       v.GetInt("MAIL_CLIENT_LIMIT"),
		TenantOwnerLimit:      v.GetInt("TENANT_OWNER_LIMIT"),
		TenantOwnerTotalLimit: v.GetInt("TENANT_OWNER_TOTAL_LIMIT"),
		BulkMaxTickets:        strictInt(v.GetString("BULK_MAX_TICKETS")),
		ResumeAffinity:        v.GetDuration("RESUME_AFFINITY"),
		Cooldown: ticket.CooldownPolicy{
			Min:           v.GetDuration("RELEASE_COOLDOWN_MIN"),
			Max:           v.GetDuration("RELEASE_COOLDOWN_MAX"),
			EscalateAfter: v.GetInt("RELEASE_COOLDOWN_ESCALATE_AFTER"),
			MaxTotal:      v.GetDuration("RELEASE_COOLDOWN_MAX_TOTAL"),
		},
		CooldownSweepInterval:       v.GetDuration("COOLDOWN_SWEEP_INTERVAL"),
		ExperimentReconcileInterval: v.GetDuration("EXPERIMENT_RECONCILE_INTERVAL"),
		Proposals: pendingaction.Config{
			TTL:                v.GetDuration("PROPOSAL_TTL"),
			MaxPendingPerAgent: strictInt(v.GetString("PROPOSAL_MAX_PENDING_PER_AGENT")),
			RatePerMinute:      strictInt(v.GetString("PROPOSAL_RATE_PER_MINUTE")),
		},
		Email: email.Config{
			Backend:        v.GetString("EMAIL_BACKEND"),
			From:           v.GetString("EMAIL_FROM"),
			SMTPHost:       v.GetString("SMTP_HOST"),
			SMTPPort:       v.GetString("SMTP_PORT"),
			SMTPUser:       v.GetString("SMTP_USER"),
			SMTPPass:       v.GetString("SMTP_PASS"),
			SMTPRequireTLS: v.GetBool("SMTP_REQUIRE_TLS"),
		},
		GitHub:  githubFrom(v),
		OIDC:    oidcFrom(v),
		Webhook: webhookFrom(v),
		Intake:  intakeFrom(v),

		Credentials:      credentials,
		GitHubAPIBaseURL: strings.TrimSpace(v.GetString("GITHUB_API_BASE_URL")),
		GitHubWebBaseURL: strings.TrimSpace(v.GetString("GITHUB_WEB_BASE_URL")),
		Prune: prune.Config{
			SessionRetention:       v.GetDuration("SESSION_RETENTION"),
			LeaseRetention:         v.GetDuration("LEASE_RETENTION"),
			DeliveryRetention:      v.GetDuration("GITHUB_DELIVERY_RETENTION"),
			LLMUsageRetention:      v.GetDuration("LLM_USAGE_RETENTION"),
			PendingActionRetention: v.GetDuration("PENDING_ACTION_RETENTION"),
			ConversationRetention:  v.GetDuration("CONVERSATION_RETENTION"),
			Interval:               v.GetDuration("PRUNE_INTERVAL"),
		},
	}
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// validate refuses a configuration that would start but not work, or work
// insecurely -- found at startup rather than on the first sign-up.
func (c *Config) validate() error {
	var errs []error
	for _, required := range []struct{ value, name string }{
		{c.DatabaseURL, "DATABASE_URL"},
		{c.AppDBRole, "APP_DB_ROLE"},
		{c.AppDBPassword, "APP_DB_PASSWORD"},
		{c.PublicBaseURL, "PUBLIC_BASE_URL"},
		{c.JWTSecret, "JWT_SECRET"},
	} {
		if required.value == "" {
			errs = append(errs, fmt.Errorf("%s is required", required.name))
		}
	}
	if c.JWTSecret != "" && len(c.JWTSecret) < minJWTSecretBytes {
		errs = append(errs, fmt.Errorf("JWT_SECRET must be at least %d bytes", minJWTSecretBytes))
	}
	if c.PublicBaseURL != "" {
		u, err := url.Parse(c.PublicBaseURL)
		switch {
		case err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "":
			errs = append(errs, fmt.Errorf("PUBLIC_BASE_URL must be an absolute http(s) URL, got %q", c.PublicBaseURL))
		case u.Scheme == "http" && !isLoopback(u.Hostname()) && !c.AllowInsecureHTTP:
			// http turns the session cookies' Secure flag off; on a real
			// host that sends the session in the clear.
			errs = append(errs, fmt.Errorf("PUBLIC_BASE_URL is plain http on %s; use https, or set ALLOW_INSECURE_HTTP for a throwaway stack", u.Hostname()))
		}
	}
	// go-auth's email.New treats anything but "smtp" as "log", so a typo
	// here would quietly write every reset link to the logs instead of
	// sending it.
	switch c.Email.Backend {
	case "log":
	case "smtp":
		if c.Email.SMTPHost == "" || c.Email.From == "" {
			errs = append(errs, fmt.Errorf("EMAIL_BACKEND=smtp needs SMTP_HOST and EMAIL_FROM"))
		}
	default:
		errs = append(errs, fmt.Errorf("EMAIL_BACKEND must be smtp or log, got %q", c.Email.Backend))
	}
	for _, ttl := range []struct {
		value time.Duration
		name  string
	}{
		{c.AccessTokenTTL, "ACCESS_TOKEN_TTL"},
		{c.RefreshTokenTTL, "REFRESH_TOKEN_TTL"},
		{c.ActivationTTL, "ACTIVATION_TTL"},
		{c.ResetTTL, "RESET_TTL"},
		{c.InviteTTL, "INVITE_TTL"},
	} {
		if ttl.value <= 0 {
			errs = append(errs, fmt.Errorf("%s must be positive", ttl.name))
		}
	}
	if c.LoginClientLimit <= 0 || c.MailClientLimit <= 0 {
		errs = append(errs, errors.New("LOGIN_CLIENT_LIMIT and MAIL_CLIENT_LIMIT must be positive"))
	}
	if c.TenantOwnerLimit < 0 {
		errs = append(errs, errors.New("TENANT_OWNER_LIMIT must not be negative"))
	}
	if c.TenantOwnerTotalLimit < c.TenantOwnerLimit {
		errs = append(errs, errors.New("TENANT_OWNER_TOTAL_LIMIT must be at least TENANT_OWNER_LIMIT"))
	}
	if c.Proposals.TTL <= 0 {
		errs = append(errs, errors.New("PROPOSAL_TTL must be positive"))
	}
	if c.Proposals.MaxPendingPerAgent <= 0 || c.Proposals.RatePerMinute <= 0 {
		errs = append(errs, errors.New("PROPOSAL_MAX_PENDING_PER_AGENT and PROPOSAL_RATE_PER_MINUTE must be positive integers"))
	}
	if c.ResumeAffinity < 0 {
		errs = append(errs, errors.New("RESUME_AFFINITY must not be negative"))
	}
	// A zero maximum switches cooldowns off, whatever the minimum says.
	if c.Cooldown.Min < 0 || c.Cooldown.Max < 0 || (c.Cooldown.Max > 0 && c.Cooldown.Max < c.Cooldown.Min) ||
		c.Cooldown.EscalateAfter < 0 || c.Cooldown.MaxTotal < 0 {
		errs = append(errs, errors.New("RELEASE_COOLDOWN_MIN and RELEASE_COOLDOWN_MAX must not be negative and the minimum must not exceed a non-zero maximum, and RELEASE_COOLDOWN_ESCALATE_AFTER and RELEASE_COOLDOWN_MAX_TOTAL must not be negative"))
	}
	if c.CooldownSweepInterval < 0 {
		errs = append(errs, errors.New("COOLDOWN_SWEEP_INTERVAL must not be negative"))
	}
	if c.ExperimentReconcileInterval < 0 {
		errs = append(errs, errors.New("EXPERIMENT_RECONCILE_INTERVAL must not be negative"))
	}
	if c.BulkMaxTickets <= 0 {
		errs = append(errs, errors.New("BULK_MAX_TICKETS must be a positive integer"))
	}
	if _, err := clientip.NewResolver(c.TrustedProxies); err != nil {
		errs = append(errs, fmt.Errorf("TRUSTED_PROXIES: %w", err))
	}
	errs = append(errs, c.validateProviders()...)
	errs = append(errs, c.validateWebhook()...)
	errs = append(errs, c.validateIntake()...)
	errs = append(errs, c.validateCredentials()...)
	errs = append(errs, c.validateTelemetry()...)
	errs = append(errs, c.validateWoodpecker()...)
	errs = append(errs, c.validateAttachments()...)
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	return nil
}

// githubFrom is GitHub's config, or nil if it is not configured. Half a
// configuration is kept, for validate to refuse, rather than read as off.
func githubFrom(v *viper.Viper) *GitHubConfig {
	g := &GitHubConfig{ClientID: v.GetString("GITHUB_CLIENT_ID"), ClientSecret: v.GetString("GITHUB_CLIENT_SECRET")}
	if g.ClientID == "" && g.ClientSecret == "" {
		return nil
	}
	return g
}

// oidcFrom is the OIDC issuer's config, or nil if it is not configured.
// The name and display name have defaults, so only the three settings
// without one decide whether it is.
func oidcFrom(v *viper.Viper) *OIDCConfig {
	o := &OIDCConfig{
		IssuerURL:    strings.TrimSpace(v.GetString("OIDC_ISSUER_URL")),
		ClientID:     v.GetString("OIDC_CLIENT_ID"),
		ClientSecret: v.GetString("OIDC_CLIENT_SECRET"),
		ProviderName: v.GetString("OIDC_PROVIDER_NAME"),
		DisplayName:  strings.TrimSpace(v.GetString("OIDC_DISPLAY_NAME")),
	}
	if o.IssuerURL == "" && o.ClientID == "" && o.ClientSecret == "" {
		return nil
	}
	return o
}

// webhookFrom is the webhook door's config, or nil when
// WEBHOOK_PUBLIC_BASE_URL is unset.
func webhookFrom(v *viper.Viper) *WebhookConfig {
	if v.GetString("WEBHOOK_PUBLIC_BASE_URL") == "" {
		return nil
	}
	return &WebhookConfig{
		ListenAddr:    v.GetString("WEBHOOK_LISTEN_ADDR"),
		PublicBaseURL: strings.TrimRight(v.GetString("WEBHOOK_PUBLIC_BASE_URL"), "/"),

		ClientLimitPerMinute: v.GetInt("WEBHOOK_CLIENT_LIMIT_PER_MINUTE"),
	}
}

// validateWebhook refuses a door whose listener or public address is unusable.
func (c *Config) validateWebhook() []error {
	w := c.Webhook
	if w == nil {
		return nil
	}
	var errs []error
	if w.ClientLimitPerMinute < 1 {
		errs = append(errs, errors.New("WEBHOOK_CLIENT_LIMIT_PER_MINUTE must be at least 1"))
	}
	if w.ListenAddr == "" || w.ListenAddr == c.ListenAddr {
		errs = append(errs, fmt.Errorf("WEBHOOK_LISTEN_ADDR must be set, and differ from LISTEN_ADDR (%q)", c.ListenAddr))
	}
	u, err := url.Parse(w.PublicBaseURL)
	switch {
	case err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || (u.Path != "" && u.Path != "/") ||
		u.RawQuery != "" || u.Fragment != "" || u.User != nil:
		// The App's webhook URL is this with a path appended; anything but a
		// bare origin would make it a different URL, or no URL at all.
		errs = append(errs, fmt.Errorf("WEBHOOK_PUBLIC_BASE_URL must be an absolute http(s) URL with no path, got %q", w.PublicBaseURL))
	case u.Scheme == "http" && !isLoopback(u.Hostname()) && !c.AllowInsecureHTTP:
		// GitHub would send every delivery, and its signature, in the
		// clear.
		errs = append(errs, fmt.Errorf("WEBHOOK_PUBLIC_BASE_URL is plain http on %s; use https, or set ALLOW_INSECURE_HTTP for a throwaway stack", u.Hostname()))
	}
	return errs
}

// validateCredentials checks the master keys, and the GitHub API URL a
// tenant's App signs calls for. Neither error says what the key was.
func (c *Config) validateCredentials() []error {
	var errs []error
	// The GitHub client checks the same when it is built; this fails the
	// start-up on a bad value with the variable's name on it.
	for _, u := range []struct{ name, value, risk string }{
		{"GITHUB_API_BASE_URL", c.GitHubAPIBaseURL, "the App's JWT would cross the network in the clear"},
		{"GITHUB_WEB_BASE_URL", c.GitHubWebBaseURL, "the App's manifest and key setup would cross the network in the clear"},
	} {
		if parsed, err := url.Parse(u.value); err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" ||
			parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
			errs = append(errs, fmt.Errorf("%s must be an absolute http(s) URL without credentials, query or fragment", u.name))
		} else if parsed.Scheme == "http" && !isLoopback(parsed.Hostname()) && !c.AllowInsecureHTTP {
			errs = append(errs, fmt.Errorf("%s is plain http on %s; use https, or set ALLOW_INSECURE_HTTP for a throwaway stack", u.name, parsed.Hostname()))
		}
	}
	return errs
}

// credentialsFrom is the master keys' config, or nil when
// CREDENTIAL_ENCRYPTION_KEY is unset. A previous key alone is an error
// rather than "off": it would silently leave stored credentials unreadable.
func credentialsFrom(v *viper.Viper) (*CredentialsConfig, error) {
	current, previous := v.GetString("CREDENTIAL_ENCRYPTION_KEY"), v.GetString("CREDENTIAL_ENCRYPTION_KEY_PREVIOUS")
	if current == "" {
		if previous != "" {
			return nil, errors.New("CREDENTIAL_ENCRYPTION_KEY_PREVIOUS is set without CREDENTIAL_ENCRYPTION_KEY")
		}
		return nil, nil
	}
	key, err := secretbox.ParseKey(current)
	if err != nil {
		return nil, fmt.Errorf("CREDENTIAL_ENCRYPTION_KEY: %w", err)
	}
	out := &CredentialsConfig{Key: key}
	if previous != "" {
		if out.PreviousKey, err = secretbox.ParseKey(previous); err != nil {
			return nil, fmt.Errorf("CREDENTIAL_ENCRYPTION_KEY_PREVIOUS: %w", err)
		}
		if bytes.Equal(out.Key, out.PreviousKey) {
			return nil, errors.New("CREDENTIAL_ENCRYPTION_KEY_PREVIOUS must differ from CREDENTIAL_ENCRYPTION_KEY")
		}
	}
	return out, nil
}

// intakeFrom is the intake door's config, or nil when INTAKE_LISTEN_ADDR is
// unset.
func intakeFrom(v *viper.Viper) *IntakeConfig {
	addr := v.GetString("INTAKE_LISTEN_ADDR")
	if addr == "" {
		return nil
	}
	return &IntakeConfig{
		ListenAddr:    addr,
		PublicBaseURL: strings.TrimRight(v.GetString("INTAKE_PUBLIC_BASE_URL"), "/"),
		Key:           v.GetString("INTAKE_KEY"),
		RatePerMinute: v.GetInt("INTAKE_RATE_PER_MINUTE"),
		Burst:         v.GetInt("INTAKE_BURST"),

		ClientLimitPerMinute: v.GetInt("INTAKE_CLIENT_LIMIT_PER_MINUTE"),
	}
}

// validateIntake refuses an intake door on another door's address, a key
// short enough to guess, and rate limits that would let nothing, or
// everything, through. Plain http is allowed: the door is for the tailnet
// and the cluster's own network, where a sender reaches it by service name.
func (c *Config) validateIntake() []error {
	in := c.Intake
	if in == nil {
		return nil
	}
	var errs []error
	if in.ListenAddr == c.ListenAddr || (c.Webhook != nil && in.ListenAddr == c.Webhook.ListenAddr) {
		errs = append(errs, fmt.Errorf("INTAKE_LISTEN_ADDR %q must differ from LISTEN_ADDR and WEBHOOK_LISTEN_ADDR", in.ListenAddr))
	}
	if in.Key != "" {
		if len(in.Key) < minJWTSecretBytes {
			errs = append(errs, fmt.Errorf("INTAKE_KEY must be at least %d bytes", minJWTSecretBytes))
		}
		if in.Key == c.JWTSecret {
			errs = append(errs, errors.New("INTAKE_KEY must not be JWT_SECRET: they are rotated separately"))
		}
	}
	u, err := url.Parse(in.PublicBaseURL)
	if in.PublicBaseURL == "" || err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" ||
		(u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		errs = append(errs, fmt.Errorf("INTAKE_PUBLIC_BASE_URL must be an absolute http(s) URL with no path, got %q", in.PublicBaseURL))
	}
	if in.RatePerMinute < 1 || in.Burst < 1 {
		errs = append(errs, errors.New("INTAKE_RATE_PER_MINUTE and INTAKE_BURST must be at least 1"))
	}
	if in.ClientLimitPerMinute < 1 {
		errs = append(errs, errors.New("INTAKE_CLIENT_LIMIT_PER_MINUTE must be at least 1"))
	}
	return errs
}

// validateTelemetry refuses a metrics listener sharing an address with a
// door -- the point of its own listener is that no door reaches it -- and
// a log endpoint that is not a URL.
// validateAttachments refuses an object store without the keys to reach it.
// The upload grace's floor is the sweeper's to enforce, at startup.
func (c *Config) validateAttachments() []error {
	a := c.Attachments
	var errs []error
	if a.S3Endpoint == "" {
		if a.S3CredentialsFile != "" {
			errs = append(errs, errors.New("S3_CREDENTIALS_FILE needs S3_ENDPOINT"))
		}
		return errs
	}
	if u, err := url.Parse(a.S3Endpoint); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil {
		errs = append(errs, errors.New("S3_ENDPOINT must be an absolute http(s) URL without credentials"))
	}
	if a.S3CredentialsFile == "" {
		errs = append(errs, errors.New("S3_CREDENTIALS_FILE is required with S3_ENDPOINT"))
	}
	if a.S3Bucket == "" {
		errs = append(errs, errors.New("S3_BUCKET is required with S3_ENDPOINT"))
	}
	if a.SweepInterval <= 0 {
		errs = append(errs, errors.New("ATTACHMENT_SWEEP_INTERVAL must be positive"))
	}
	return errs
}

// validateWoodpecker refuses a half-set adapter, which would leave the runner
// on the code host's checks alone without anyone having chosen it.
func (c *Config) validateWoodpecker() []error {
	w := c.Woodpecker
	if w.URL == "" {
		if w.TokenFile != "" || w.RepoID != 0 {
			return []error{errors.New("WOODPECKER_TOKEN_FILE and WOODPECKER_REPO_ID need WOODPECKER_URL")}
		}
		return nil
	}
	var errs []error
	if u, err := url.Parse(w.URL); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil {
		errs = append(errs, errors.New("WOODPECKER_URL must be an absolute http(s) URL without credentials"))
	}
	if w.TokenFile == "" {
		errs = append(errs, errors.New("WOODPECKER_TOKEN_FILE is required with WOODPECKER_URL"))
	}
	if w.RepoID <= 0 {
		errs = append(errs, errors.New("WOODPECKER_REPO_ID must be positive with WOODPECKER_URL"))
	}
	return errs
}

func (c *Config) validateTelemetry() []error {
	var errs []error
	if m := c.MetricsListenAddr; m != "" {
		if m == c.ListenAddr || (c.Webhook != nil && m == c.Webhook.ListenAddr) || (c.Intake != nil && m == c.Intake.ListenAddr) {
			errs = append(errs, fmt.Errorf("METRICS_LISTEN_ADDR %q must differ from LISTEN_ADDR, WEBHOOK_LISTEN_ADDR and INTAKE_LISTEN_ADDR", m))
		}
	}
	if e := c.LogsOTLPEndpoint; e != "" {
		u, err := url.Parse(e)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			errs = append(errs, fmt.Errorf("LOGS_OTLP_ENDPOINT must be an absolute http(s) URL, got %q", e))
		}
	}
	return errs
}

// validateProviders refuses a half-configured provider rather than
// starting without it: an operator who set a client ID meant to turn it on.
func (c *Config) validateProviders() []error {
	var errs []error
	if !c.PasswordSignIn && c.GitHub == nil && c.OIDC == nil {
		// Nobody could sign in at all; better refused here than found
		// out from the sign-in page.
		errs = append(errs, errors.New("PASSWORD_SIGNIN=false needs GitHub or OIDC sign-in configured"))
	}
	if g := c.GitHub; g != nil && (g.ClientID == "" || g.ClientSecret == "") {
		errs = append(errs, errors.New("GITHUB_CLIENT_ID and GITHUB_CLIENT_SECRET must be set together"))
	}
	o := c.OIDC
	if o == nil {
		return errs
	}
	if o.IssuerURL == "" || o.ClientID == "" || o.ClientSecret == "" {
		errs = append(errs, errors.New("OIDC_ISSUER_URL, OIDC_CLIENT_ID and OIDC_CLIENT_SECRET must be set together"))
	}
	if o.IssuerURL != "" {
		u, err := url.Parse(o.IssuerURL)
		switch {
		case err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "":
			errs = append(errs, fmt.Errorf("OIDC_ISSUER_URL must be an absolute http(s) URL, got %q", o.IssuerURL))
		case u.Scheme == "http" && !isLoopback(u.Hostname()) && !c.AllowInsecureHTTP:
			// The token exchange carries the client secret and the
			// authorization code: in the clear, anyone on the path could
			// sign in as whoever just did.
			errs = append(errs, fmt.Errorf("OIDC_ISSUER_URL is plain http on %s; use https, or set ALLOW_INSECURE_HTTP for a throwaway stack", u.Hostname()))
		}
	}
	if err := ValidateOIDCProviderName(o.ProviderName); err != nil {
		errs = append(errs, fmt.Errorf("OIDC_PROVIDER_NAME: %w", err))
	}
	if o.DisplayName == "" {
		errs = append(errs, errors.New("OIDC_DISPLAY_NAME must not be empty"))
	}
	return errs
}

// strictInt parses a whole number, or returns 0 -- which validate refuses
// wherever a positive one is required. viper's GetInt would read "2.5" as 2
// and quietly run with a setting nobody wrote.
func strictInt(s string) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0
	}
	return n
}

func splitList(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// SecureCookies reports whether the board is served over https, so the
// session cookies must be Secure. Plain http is for local development.
func (c *Config) SecureCookies() bool {
	return strings.HasPrefix(c.PublicBaseURL, "https://")
}
