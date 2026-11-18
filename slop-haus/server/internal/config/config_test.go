package config_test

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/change"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"

	"github.com/lowaa/slop-factory/go-auth/email"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/config"
)

// valid is a working configuration as flags; each test breaks one thing.
func valid() map[string]string {
	return map[string]string{
		"database-url":    "postgres://owner:pw@db/slop",
		"app-db-password": "app-pw",
		"public-base-url": "https://slop.example/",
		"jwt-secret":      strings.Repeat("s", 32),
		"email-backend":   "log",
	}
}

func load(t *testing.T, flags map[string]string) (*config.Config, error) {
	t.Helper()
	// Env would leak in from the developer's shell or CI; flags win over
	// it, so every key under test is given as a flag.
	for _, key := range []string{"DATABASE_URL", "APP_DB_PASSWORD", "PUBLIC_BASE_URL", "JWT_SECRET", "EMAIL_BACKEND", "SMTP_HOST", "EMAIL_FROM", "ALLOW_INSECURE_HTTP", "CREDENTIAL_ENCRYPTION_KEY", "CREDENTIAL_ENCRYPTION_KEY_PREVIOUS", "GITHUB_API_BASE_URL", "WEBHOOK_LISTEN_ADDR", "WEBHOOK_PUBLIC_BASE_URL", "INTAKE_LISTEN_ADDR", "INTAKE_PUBLIC_BASE_URL", "INTAKE_KEY", "INTAKE_RATE_PER_MINUTE", "INTAKE_BURST", "INTAKE_CLIENT_LIMIT_PER_MINUTE", "WEBHOOK_CLIENT_LIMIT_PER_MINUTE", "PASSWORD_SIGNIN", "LOGIN_CLIENT_LIMIT", "MAIL_CLIENT_LIMIT", "TENANT_OWNER_LIMIT", "METRICS_LISTEN_ADDR", "LOGS_OTLP_ENDPOINT", "BUILD_TAG"} {
		t.Setenv(key, "")
	}
	var args []string
	for k, v := range flags {
		args = append(args, "--"+k+"="+v)
	}
	return config.Load(args)
}

func TestAValidConfigLoads(t *testing.T) {
	cfg, err := load(t, valid())
	require.NoError(t, err)
	assert.Equal(t, "https://slop.example", cfg.PublicBaseURL, "trailing slash trimmed")
	assert.Equal(t, "slophaus_app", cfg.AppDBRole)
	assert.True(t, cfg.SecureCookies())
}

func TestTheBuildTagIsDevUnlessTheImageSaysOtherwise(t *testing.T) {
	cfg, err := load(t, valid())
	require.NoError(t, err)
	assert.Equal(t, "dev", cfg.BuildTag)

	flags := valid()
	flags["build-tag"] = "20260928T151918Z-77bef973"
	cfg, err = load(t, flags)
	require.NoError(t, err)
	assert.Equal(t, "20260928T151918Z-77bef973", cfg.BuildTag)
}

func TestPlainHTTPMeansInsecureCookies(t *testing.T) {
	flags := valid()
	flags["public-base-url"] = "http://localhost:5173"
	cfg, err := load(t, flags)
	require.NoError(t, err)
	assert.False(t, cfg.SecureCookies())
}

func TestPlainHTTPElsewhereNeedsTheOptIn(t *testing.T) {
	flags := valid()
	flags["public-base-url"] = "http://frontend"
	flags["allow-insecure-http"] = "true"
	cfg, err := load(t, flags)
	require.NoError(t, err)
	assert.False(t, cfg.SecureCookies())
}

// TRUSTED_PROXIES is a comma list, split into the ranges clientip.NewResolver
// parses -- each trimmed, so a value written with spaces after the commas
// (as an operator typing it by hand would) still parses one CIDR per entry
// rather than one with leading whitespace baked in.
func TestTrustedProxiesIsACommaListWithSpacesTrimmed(t *testing.T) {
	flags := valid()
	flags["trusted-proxies"] = "10.0.0.0/8, 192.168.0.0/16 ,  ::1/128"
	cfg, err := load(t, flags)
	require.NoError(t, err)
	assert.Equal(t, []string{"10.0.0.0/8", "192.168.0.0/16", "::1/128"}, cfg.TrustedProxies)
}

func TestLandingKnownFlakyIsACommaListWithSpacesTrimmed(t *testing.T) {
	flags := valid()
	flags["landing-known-flaky"] = "e2e, frontend ,,kube"
	cfg, err := load(t, flags)
	require.NoError(t, err)
	assert.Equal(t, []string{"e2e", "frontend", "kube"}, cfg.LandingKnownFlaky)
}

func TestInvalidConfigsAreRefused(t *testing.T) {
	cases := map[string]func(map[string]string){
		"no app db password":      func(f map[string]string) { delete(f, "app-db-password") },
		"no jwt secret":           func(f map[string]string) { delete(f, "jwt-secret") },
		"short jwt secret":        func(f map[string]string) { f["jwt-secret"] = strings.Repeat("s", 31) },
		"no public base url":      func(f map[string]string) { delete(f, "public-base-url") },
		"relative base url":       func(f map[string]string) { f["public-base-url"] = "/board" },
		"non-http base url":       func(f map[string]string) { f["public-base-url"] = "javascript://x" },
		"no email backend":        func(f map[string]string) { delete(f, "email-backend") },
		"typo'd email backend":    func(f map[string]string) { f["email-backend"] = "smpt" },
		"smtp without a host":     func(f map[string]string) { f["email-backend"] = "smtp"; f["email-from"] = "a@b" },
		"zero access ttl":         func(f map[string]string) { f["access-token-ttl"] = "0s" },
		"http on a real host":     func(f map[string]string) { f["public-base-url"] = "http://slop.example" },
		"zero login client limit": func(f map[string]string) { f["login-client-limit"] = "0" },
		"invalid trusted proxy CIDR": func(f map[string]string) {
			f["trusted-proxies"] = "not-a-cidr"
		},
	}
	for name, breakIt := range cases {
		t.Run(name, func(t *testing.T) {
			flags := valid()
			breakIt(flags)
			_, err := load(t, flags)
			require.Error(t, err)
		})
	}
}

// An invalid CIDR is refused at Load, by name, rather than surfacing later
// as a resolver clientip constructs and every request runs through.
func TestInvalidTrustedProxyCIDRNamesItselfInTheError(t *testing.T) {
	flags := valid()
	flags["trusted-proxies"] = "10.0.0.0/8,not-a-cidr"
	_, err := load(t, flags)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "TRUSTED_PROXIES")
}

// The cluster configures by environment, not flags; every value must land
// in the field it names, with a flag still winning over it.
func TestEnvironmentIsReadIntoEveryField(t *testing.T) {
	env := map[string]string{
		"DATABASE_URL":         "postgres://owner:pw@db/slop",
		"APP_DB_ROLE":          "custom_app",
		"APP_DB_PASSWORD":      "app-pw",
		"PUBLIC_BASE_URL":      "https://slop.example",
		"JWT_SECRET":           strings.Repeat("s", 32),
		"ACCESS_TOKEN_TTL":     "5m",
		"REFRESH_TOKEN_TTL":    "48h",
		"ACTIVATION_TTL":       "2h",
		"RESET_TTL":            "30m",
		"INVITE_TTL":           "72h",
		"REGISTRATION_OPEN":    "true",
		"PASSWORD_SIGNIN":      "false",
		"LOGIN_CLIENT_LIMIT":   "7",
		"TENANT_OWNER_LIMIT":   "5",
		"GITHUB_CLIENT_ID":     "gh-id",
		"GITHUB_CLIENT_SECRET": "gh-secret",
		"EMAIL_BACKEND":        "smtp",
		"EMAIL_FROM":           "slop@example.com",
		"SMTP_HOST":            "smtp.example.com",
		"SMTP_PORT":            "2525",
		"SMTP_USER":            "user",
		"SMTP_PASS":            "pass",
		"ALLOW_INSECURE_HTTP":  "",
	}
	for k, v := range env {
		t.Setenv(k, v)
	}

	cfg, err := config.Load([]string{"--smtp-port=465"})
	require.NoError(t, err)

	assert.Equal(t, "/srv/supervisor", cfg.SupervisorPackageDir)
	assert.Equal(t, "custom_app", cfg.AppDBRole)
	assert.Equal(t, "app-pw", cfg.AppDBPassword)
	assert.Equal(t, 5*time.Minute, cfg.AccessTokenTTL)
	assert.Equal(t, 48*time.Hour, cfg.RefreshTokenTTL)
	assert.Equal(t, 2*time.Hour, cfg.ActivationTTL)
	assert.Equal(t, 30*time.Minute, cfg.ResetTTL)
	assert.Equal(t, 72*time.Hour, cfg.InviteTTL)
	assert.True(t, cfg.RegistrationOpen)
	assert.False(t, cfg.PasswordSignIn)
	assert.Equal(t, 7, cfg.LoginClientLimit)
	assert.Equal(t, 5, cfg.TenantOwnerLimit)
	assert.Equal(t, 20, cfg.MailClientLimit, "the default")
	assert.Equal(t, email.Config{
		Backend: "smtp", From: "slop@example.com",
		SMTPHost: "smtp.example.com", SMTPPort: "465", SMTPUser: "user", SMTPPass: "pass",
		SMTPRequireTLS: true,
	}, cfg.Email, "the flag's port wins over the environment's; TLS is required by default")
}

func clearProviderEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{"GITHUB_CLIENT_ID", "GITHUB_CLIENT_SECRET", "OIDC_ISSUER_URL", "OIDC_CLIENT_ID", "OIDC_CLIENT_SECRET", "OIDC_PROVIDER_NAME", "OIDC_DISPLAY_NAME"} {
		t.Setenv(key, "")
	}
}

// With nothing configured: password sign-in alone, as before providers
// existed.
func TestNoProvidersByDefault(t *testing.T) {
	clearProviderEnv(t)
	cfg, err := load(t, valid())
	require.NoError(t, err)
	assert.Nil(t, cfg.GitHub)
	assert.Nil(t, cfg.OIDC)
	assert.True(t, cfg.PasswordSignIn)
}

func TestPasswordSignInCanBeTurnedOffWithAProvider(t *testing.T) {
	clearProviderEnv(t)
	flags := valid()
	flags["password-signin"] = "false"
	flags["github-client-id"] = "gh-id"
	flags["github-client-secret"] = "gh-secret"
	cfg, err := load(t, flags)
	require.NoError(t, err)
	assert.False(t, cfg.PasswordSignIn)
}

// With neither a password nor a provider, nobody could sign in.
func TestPasswordSignInOffWithNoProviderIsRefused(t *testing.T) {
	clearProviderEnv(t)
	flags := valid()
	flags["password-signin"] = "false"
	_, err := load(t, flags)
	require.ErrorContains(t, err, "PASSWORD_SIGNIN=false needs GitHub or OIDC")
}

func TestBothProvidersCanBeConfigured(t *testing.T) {
	clearProviderEnv(t)
	flags := valid()
	flags["github-client-id"] = "gh-id"
	flags["github-client-secret"] = "gh-secret"
	flags["oidc-issuer-url"] = "https://dex.example/dex"
	flags["oidc-client-id"] = "oidc-id"
	flags["oidc-client-secret"] = "oidc-secret"
	flags["oidc-provider-name"] = "dex"
	flags["oidc-display-name"] = "Dex"
	cfg, err := load(t, flags)
	require.NoError(t, err)
	assert.Equal(t, &config.GitHubConfig{ClientID: "gh-id", ClientSecret: "gh-secret"}, cfg.GitHub)
	assert.Equal(t, &config.OIDCConfig{
		IssuerURL: "https://dex.example/dex", ClientID: "oidc-id", ClientSecret: "oidc-secret",
		ProviderName: "dex", DisplayName: "Dex",
	}, cfg.OIDC)
}

func TestOIDCNameAndDisplayNameDefault(t *testing.T) {
	clearProviderEnv(t)
	flags := valid()
	flags["oidc-issuer-url"] = "https://idp.example"
	flags["oidc-client-id"] = "id"
	flags["oidc-client-secret"] = "secret"
	cfg, err := load(t, flags)
	require.NoError(t, err)
	assert.Equal(t, "oidc", cfg.OIDC.ProviderName)
	assert.Equal(t, "Single sign-on", cfg.OIDC.DisplayName)
}

func TestInvalidProviderConfigsAreRefused(t *testing.T) {
	oidc := func(f map[string]string) {
		f["oidc-issuer-url"] = "https://idp.example"
		f["oidc-client-id"] = "id"
		f["oidc-client-secret"] = "secret"
	}
	cases := map[string]func(map[string]string){
		"github id without a secret":  func(f map[string]string) { f["github-client-id"] = "id" },
		"github secret without an id": func(f map[string]string) { f["github-client-secret"] = "s" },
		"oidc without a secret":       func(f map[string]string) { oidc(f); delete(f, "oidc-client-secret") },
		"oidc without an issuer":      func(f map[string]string) { oidc(f); delete(f, "oidc-issuer-url") },
		"oidc issuer not a url":       func(f map[string]string) { oidc(f); f["oidc-issuer-url"] = "idp.example" },
		"oidc issuer plain http":      func(f map[string]string) { oidc(f); f["oidc-issuer-url"] = "http://idp.example" },
		"oidc named github":           func(f map[string]string) { oidc(f); f["oidc-provider-name"] = "github" },
		"oidc named password":         func(f map[string]string) { oidc(f); f["oidc-provider-name"] = "password" },
		"oidc name with a slash":      func(f map[string]string) { oidc(f); f["oidc-provider-name"] = "a/b" },
		"oidc blank display name":     func(f map[string]string) { oidc(f); f["oidc-display-name"] = " " },
	}
	for name, breakIt := range cases {
		t.Run(name, func(t *testing.T) {
			clearProviderEnv(t)
			flags := valid()
			breakIt(flags)
			_, err := load(t, flags)
			require.Error(t, err)
		})
	}
}

// A plain-http issuer is fine where plain http already is: loopback, or a
// throwaway stack that opted in (the e2e stack's Dex).
func TestPlainHTTPIssuerOnLoopbackOrOptIn(t *testing.T) {
	for _, tc := range []struct{ issuer, insecure string }{
		{"http://localhost:5556/dex", ""},
		{"http://dex:5556/dex", "true"},
	} {
		clearProviderEnv(t)
		flags := valid()
		flags["oidc-issuer-url"] = tc.issuer
		flags["oidc-client-id"] = "id"
		flags["oidc-client-secret"] = "secret"
		if tc.insecure != "" {
			flags["allow-insecure-http"] = tc.insecure
		}
		_, err := load(t, flags)
		require.NoError(t, err, tc.issuer)
	}
}

// Off unless WEBHOOK_PUBLIC_BASE_URL is set: the door has nowhere to tell
// GitHub to deliver without it.
func TestNoWebhookDoorWithoutAPublicURL(t *testing.T) {
	cfg, err := load(t, valid())
	require.NoError(t, err)
	assert.Nil(t, cfg.Webhook)
}

func webhookFlags() map[string]string {
	flags := valid()
	flags["webhook-public-base-url"] = "https://hooks.example/"
	return flags
}

// A throwaway stack reaches its listener over plain http on its own
// network, as the e2e suite does.
func TestAPlainHTTPWebhookURLNeedsTheOptIn(t *testing.T) {
	flags := webhookFlags()
	flags["webhook-public-base-url"] = "http://api:8081"
	flags["allow-insecure-http"] = "true"
	cfg, err := load(t, flags)
	require.NoError(t, err)
	assert.Equal(t, "http://api:8081", cfg.Webhook.PublicBaseURL)
}

func TestAWebhookPublicURLOpensTheDoor(t *testing.T) {
	cfg, err := load(t, webhookFlags())
	require.NoError(t, err)
	require.NotNil(t, cfg.Webhook)
	assert.Equal(t, ":8081", cfg.Webhook.ListenAddr, "its own port by default")
	assert.Equal(t, "https://hooks.example", cfg.Webhook.PublicBaseURL, "trailing slash trimmed")
}

func TestInvalidWebhookConfigsAreRefused(t *testing.T) {
	for name, tc := range map[string]struct {
		edit func(map[string]string)
		want string
	}{
		"public url path":   {func(f map[string]string) { f["webhook-public-base-url"] = "https://hooks.example/x" }, "with no path"},
		"public url query":  {func(f map[string]string) { f["webhook-public-base-url"] = "https://hooks.example?x=1" }, "with no path"},
		"public url frag":   {func(f map[string]string) { f["webhook-public-base-url"] = "https://hooks.example#x" }, "with no path"},
		"plain http":        {func(f map[string]string) { f["webhook-public-base-url"] = "http://hooks.example" }, "plain http"},
		"shares the port":   {func(f map[string]string) { f["webhook-listen-addr"] = ":8080" }, "differ from LISTEN_ADDR"},
		"zero client limit": {func(f map[string]string) { f["webhook-client-limit-per-minute"] = "0" }, "WEBHOOK_CLIENT_LIMIT_PER_MINUTE must be at least 1"},
	} {
		t.Run(name, func(t *testing.T) {
			flags := webhookFlags()
			tc.edit(flags)
			_, err := load(t, flags)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

// The deployment sets these as env, not flags.
func TestWebhookSettingsAreReadFromTheEnvironment(t *testing.T) {
	for k, v := range map[string]string{
		"WEBHOOK_LISTEN_ADDR":             ":9091",
		"WEBHOOK_PUBLIC_BASE_URL":         "https://slop-haus-hooks.example",
		"WEBHOOK_CLIENT_LIMIT_PER_MINUTE": "77",
		"GITHUB_DELIVERY_RETENTION":       "72h",
		"LLM_USAGE_RETENTION":             "96h",
		"PENDING_ACTION_RETENTION":        "48h",
		"CONVERSATION_RETENTION":          "24h",
	} {
		t.Setenv(k, v)
	}
	var args []string
	for k, v := range valid() {
		args = append(args, "--"+k+"="+v)
	}
	cfg, err := config.Load(args)
	require.NoError(t, err)
	assert.Equal(t, &config.WebhookConfig{
		ListenAddr: ":9091", PublicBaseURL: "https://slop-haus-hooks.example",
		ClientLimitPerMinute: 77,
	}, cfg.Webhook)
	assert.Equal(t, 72*time.Hour, cfg.Prune.DeliveryRetention)
	assert.Equal(t, 96*time.Hour, cfg.Prune.LLMUsageRetention)
	assert.Equal(t, 48*time.Hour, cfg.Prune.PendingActionRetention)
	assert.Equal(t, 24*time.Hour, cfg.Prune.ConversationRetention)
}

func TestNoIntakeDoorWithoutAListenAddress(t *testing.T) {
	flags := valid()
	flags["intake-public-base-url"] = "http://intake.example"
	flags["intake-key"] = "short"
	cfg, err := load(t, flags)
	require.NoError(t, err)
	assert.Nil(t, cfg.Intake)
}

func intakeFlags() map[string]string {
	flags := valid()
	flags["intake-listen-addr"] = ":8082"
	flags["intake-public-base-url"] = "https://intake.example/"
	flags["intake-key"] = strings.Repeat("i", 32)
	return flags
}

func TestAnIntakeListenAddressOpensTheDoor(t *testing.T) {
	cfg, err := load(t, intakeFlags())
	require.NoError(t, err)
	assert.Equal(t, &config.IntakeConfig{
		ListenAddr: ":8082", PublicBaseURL: "https://intake.example", Key: strings.Repeat("i", 32),
		RatePerMinute: 60, Burst: 20, ClientLimitPerMinute: 600,
	}, cfg.Intake)
}

func TestIntakeNeedsNoKeyAndAllowsPlainHTTP(t *testing.T) {
	flags := intakeFlags()
	delete(flags, "intake-key")
	flags["intake-public-base-url"] = "http://slop-haus-intake:8082"
	flags["intake-rate-per-minute"] = "5"
	flags["intake-burst"] = "1"
	cfg, err := load(t, flags)
	require.NoError(t, err)
	assert.Empty(t, cfg.Intake.Key, "bearer sources only")
	assert.Equal(t, "http://slop-haus-intake:8082", cfg.Intake.PublicBaseURL)
	assert.Equal(t, 5, cfg.Intake.RatePerMinute)
	assert.Equal(t, 1, cfg.Intake.Burst)
}

func TestInvalidIntakeConfigsAreRefused(t *testing.T) {
	for name, tc := range map[string]struct {
		edit func(map[string]string)
		want string
	}{
		"shares the api port": {func(f map[string]string) { f["intake-listen-addr"] = ":8080" }, "INTAKE_LISTEN_ADDR"},
		"shares the webhook port": {func(f map[string]string) {
			f["intake-listen-addr"] = ":8081"
			f["webhook-public-base-url"] = "https://hooks.example"
		}, "INTAKE_LISTEN_ADDR"},
		"short key":         {func(f map[string]string) { f["intake-key"] = "short" }, "INTAKE_KEY must be at least"},
		"key is jwt secret": {func(f map[string]string) { f["intake-key"] = f["jwt-secret"] }, "INTAKE_KEY must not be JWT_SECRET"},
		"no public url":     {func(f map[string]string) { delete(f, "intake-public-base-url") }, "INTAKE_PUBLIC_BASE_URL must be"},
		"relative url":      {func(f map[string]string) { f["intake-public-base-url"] = "intake.example" }, "INTAKE_PUBLIC_BASE_URL must be"},
		"public url path":   {func(f map[string]string) { f["intake-public-base-url"] = "https://intake.example/x" }, "with no path"},
		"public url query":  {func(f map[string]string) { f["intake-public-base-url"] = "https://intake.example?x=1" }, "with no path"},
		"zero rate":         {func(f map[string]string) { f["intake-rate-per-minute"] = "0" }, "must be at least 1"},
		"zero burst":        {func(f map[string]string) { f["intake-burst"] = "0" }, "must be at least 1"},
		"zero client limit": {func(f map[string]string) { f["intake-client-limit-per-minute"] = "0" }, "INTAKE_CLIENT_LIMIT_PER_MINUTE must be at least 1"},
		"metrics on intake": {func(f map[string]string) { f["metrics-listen-addr"] = ":8082" }, "METRICS_LISTEN_ADDR"},
	} {
		t.Run(name, func(t *testing.T) {
			flags := intakeFlags()
			tc.edit(flags)
			_, err := load(t, flags)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

// The deployment sets these as env, not flags.
func TestIntakeSettingsAreReadFromTheEnvironment(t *testing.T) {
	for k, v := range map[string]string{
		"INTAKE_LISTEN_ADDR":             ":9092",
		"INTAKE_PUBLIC_BASE_URL":         "http://slop-haus-intake.tailnet.example/",
		"INTAKE_KEY":                     strings.Repeat("e", 40),
		"INTAKE_RATE_PER_MINUTE":         "12",
		"INTAKE_BURST":                   "3",
		"INTAKE_CLIENT_LIMIT_PER_MINUTE": "99",
	} {
		t.Setenv(k, v)
	}
	var args []string
	for k, v := range valid() {
		args = append(args, "--"+k+"="+v)
	}
	cfg, err := config.Load(args)
	require.NoError(t, err)
	assert.Equal(t, &config.IntakeConfig{
		ListenAddr: ":9092", PublicBaseURL: "http://slop-haus-intake.tailnet.example", Key: strings.Repeat("e", 40),
		RatePerMinute: 12, Burst: 3, ClientLimitPerMinute: 99,
	}, cfg.Intake)
}

func TestTelemetryIsOffUnlessConfigured(t *testing.T) {
	cfg, err := load(t, valid())
	require.NoError(t, err)
	assert.Empty(t, cfg.MetricsListenAddr, "no metrics listener")
	assert.Empty(t, cfg.LogsOTLPEndpoint, "no log export")

	flags := valid()
	flags["metrics-listen-addr"] = ":9090"
	flags["logs-otlp-endpoint"] = "http://victoria-logs:9428/insert/opentelemetry/v1/logs"
	cfg, err = load(t, flags)
	require.NoError(t, err)
	assert.Equal(t, ":9090", cfg.MetricsListenAddr)
	assert.Equal(t, "http://victoria-logs:9428/insert/opentelemetry/v1/logs", cfg.LogsOTLPEndpoint)
}

// Metrics get a listener of their own so that no door reaches them; one
// shared with a door would put them behind it.
func TestInvalidTelemetryConfigsAreRefused(t *testing.T) {
	for name, tweak := range map[string]map[string]string{
		"metrics on the board's listener":   {"metrics-listen-addr": ":8080"},
		"metrics on the webhook's listener": {"metrics-listen-addr": ":9999", "webhook-listen-addr": ":9999", "webhook-public-base-url": "https://hooks.example"},
		"a log endpoint with no scheme":     {"logs-otlp-endpoint": "victoria-logs:9428/insert"},
		"a log endpoint with no host":       {"logs-otlp-endpoint": "http:///insert"},
	} {
		t.Run(name, func(t *testing.T) {
			flags := valid()
			for k, v := range tweak {
				flags[k] = v
			}
			_, err := load(t, flags)
			assert.Error(t, err)
		})
	}
}

func TestTelemetrySettingsAreReadFromTheEnvironment(t *testing.T) {
	flags := valid()
	_, err := load(t, flags)
	require.NoError(t, err)
	t.Setenv("METRICS_LISTEN_ADDR", ":9090")
	t.Setenv("LOGS_OTLP_ENDPOINT", " http://victoria-logs:9428/insert/opentelemetry/v1/logs ")
	var args []string
	for k, v := range flags {
		args = append(args, "--"+k+"="+v)
	}
	cfg, err := config.Load(args)
	require.NoError(t, err)
	assert.Equal(t, ":9090", cfg.MetricsListenAddr)
	assert.Equal(t, "http://victoria-logs:9428/insert/opentelemetry/v1/logs", cfg.LogsOTLPEndpoint, "trimmed")
}

// The bulk cap bounds how long one board action holds the claim lock, so a
// value that isn't a positive integer is refused rather than read as "no
// cap" or "nothing allowed".
func TestBulkMaxTickets(t *testing.T) {
	t.Setenv("BULK_MAX_TICKETS", "")
	cfg, err := load(t, valid())
	require.NoError(t, err)
	assert.Equal(t, 100, cfg.BulkMaxTickets, "the default")

	t.Setenv("BULK_MAX_TICKETS", "7")
	cfg, err = load(t, valid())
	require.NoError(t, err)
	assert.Equal(t, 7, cfg.BulkMaxTickets, "read from the environment")

	for _, bad := range []string{"0", "-3", "lots", "2.5"} {
		flags := valid()
		flags["bulk-max-tickets"] = bad
		_, err := load(t, flags)
		assert.ErrorContains(t, err, "BULK_MAX_TICKETS must be a positive integer", "value %q", bad)
	}
}

func TestResumeAffinity(t *testing.T) {
	t.Setenv("RESUME_AFFINITY", "")
	cfg, err := load(t, valid())
	require.NoError(t, err)
	assert.Equal(t, 30*time.Minute, cfg.ResumeAffinity, "the default")

	flags := valid()
	flags["resume-affinity"] = "2h"
	cfg, err = load(t, flags)
	require.NoError(t, err)
	assert.Equal(t, 2*time.Hour, cfg.ResumeAffinity, "a positive value is read as given")

	flags["resume-affinity"] = "0s"
	cfg, err = load(t, flags)
	require.NoError(t, err)
	assert.Zero(t, cfg.ResumeAffinity, "zero opens no window")

	flags["resume-affinity"] = "-1m"
	_, err = load(t, flags)
	assert.ErrorContains(t, err, "RESUME_AFFINITY must not be negative")
}

func TestReleaseCooldown(t *testing.T) {
	for _, name := range []string{"RELEASE_COOLDOWN_MIN", "RELEASE_COOLDOWN_MAX", "RELEASE_COOLDOWN_ESCALATE_AFTER", "RELEASE_COOLDOWN_MAX_TOTAL", "COOLDOWN_SWEEP_INTERVAL"} {
		t.Setenv(name, "")
	}
	cfg, err := load(t, valid())
	require.NoError(t, err)
	assert.Equal(t, ticket.DefaultCooldownPolicy(), cfg.Cooldown, "the defaults")
	assert.Equal(t, ticket.DefaultCooldownSweepInterval, cfg.CooldownSweepInterval)

	flags := valid()
	flags["release-cooldown-min"] = "1m"
	flags["release-cooldown-max"] = "2h"
	flags["release-cooldown-escalate-after"] = "5"
	flags["release-cooldown-max-total"] = "10h"
	flags["cooldown-sweep-interval"] = "7s"
	cfg, err = load(t, flags)
	require.NoError(t, err)
	assert.Equal(t, ticket.CooldownPolicy{Min: time.Minute, Max: 2 * time.Hour, EscalateAfter: 5, MaxTotal: 10 * time.Hour}, cfg.Cooldown)
	assert.Equal(t, 7*time.Second, cfg.CooldownSweepInterval)

	flags = valid()
	flags["release-cooldown-max"] = "0s"
	cfg, err = load(t, flags)
	require.NoError(t, err, "a zero maximum switches cooldowns off without also zeroing the minimum")
	assert.Zero(t, cfg.Cooldown.Max)

	for name, bad := range map[string]map[string]string{
		"minimum above maximum": {"release-cooldown-min": "48h"},
		"negative minimum":      {"release-cooldown-min": "-1m"},
		"negative maximum":      {"release-cooldown-max": "-1m"},
		"negative escalation":   {"release-cooldown-escalate-after": "-1"},
		"negative total":        {"release-cooldown-max-total": "-1h"},
		"negative sweep":        {"cooldown-sweep-interval": "-1s"},
	} {
		flags = valid()
		for k, v := range bad {
			flags[k] = v
		}
		_, err = load(t, flags)
		assert.Error(t, err, name)
	}
}

func TestProposalLimits(t *testing.T) {
	for _, name := range []string{"PROPOSAL_TTL", "PROPOSAL_MAX_PENDING_PER_AGENT", "PROPOSAL_RATE_PER_MINUTE"} {
		t.Setenv(name, "")
	}
	cfg, err := load(t, valid())
	require.NoError(t, err)
	assert.Equal(t, 7*24*time.Hour, cfg.Proposals.TTL, "the default")
	assert.Equal(t, 50, cfg.Proposals.MaxPendingPerAgent)
	assert.Equal(t, 30, cfg.Proposals.RatePerMinute)

	t.Setenv("PROPOSAL_TTL", "2h")
	t.Setenv("PROPOSAL_MAX_PENDING_PER_AGENT", "5")
	t.Setenv("PROPOSAL_RATE_PER_MINUTE", "9")
	cfg, err = load(t, valid())
	require.NoError(t, err)
	assert.Equal(t, 2*time.Hour, cfg.Proposals.TTL)
	assert.Equal(t, 5, cfg.Proposals.MaxPendingPerAgent)
	assert.Equal(t, 9, cfg.Proposals.RatePerMinute)

	for flag, want := range map[string]string{
		"proposal-ttl":                   "PROPOSAL_TTL must be positive",
		"proposal-max-pending-per-agent": "PROPOSAL_MAX_PENDING_PER_AGENT and PROPOSAL_RATE_PER_MINUTE must be positive",
		"proposal-rate-per-minute":       "PROPOSAL_MAX_PENDING_PER_AGENT and PROPOSAL_RATE_PER_MINUTE must be positive",
	} {
		for _, bad := range []string{"0", "-1", "lots"} {
			flags := valid()
			flags[flag] = bad
			_, err := load(t, flags)
			assert.ErrorContains(t, err, want, "%s=%s", flag, bad)
		}
	}
}

func credentialKey(b byte) string {
	return base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{b}, 32))
}

// Off unless CREDENTIAL_ENCRYPTION_KEY is set: a deploy that expects the key
// starts before anyone has created its Secret.
func TestNoStoredCredentialsWithoutAnEncryptionKey(t *testing.T) {
	cfg, err := load(t, valid())
	require.NoError(t, err)
	assert.Nil(t, cfg.Credentials)
	assert.Equal(t, "https://api.github.com", cfg.GitHubAPIBaseURL)
}

func TestAnEncryptionKeyAndItsPreviousKeyAreDecoded(t *testing.T) {
	flags := valid()
	flags["credential-encryption-key"] = credentialKey(1)
	cfg, err := load(t, flags)
	require.NoError(t, err)
	require.NotNil(t, cfg.Credentials)
	assert.Equal(t, bytes.Repeat([]byte{1}, 32), cfg.Credentials.Key)
	assert.Nil(t, cfg.Credentials.PreviousKey)

	flags["credential-encryption-key-previous"] = credentialKey(2)
	cfg, err = load(t, flags)
	require.NoError(t, err)
	assert.Equal(t, bytes.Repeat([]byte{2}, 32), cfg.Credentials.PreviousKey)
}

func TestInvalidCredentialKeysAreRefusedWithoutEchoingThem(t *testing.T) {
	cases := map[string]struct {
		mutate func(map[string]string)
		want   string
	}{
		"not base64": {func(f map[string]string) { f["credential-encryption-key"] = "not*base64*not*base64*not*base64*" }, "CREDENTIAL_ENCRYPTION_KEY: not base64"},
		"wrong length": {func(f map[string]string) {
			f["credential-encryption-key"] = base64.StdEncoding.EncodeToString([]byte("short"))
		}, "must decode to 32 bytes"},
		"previous alone": {func(f map[string]string) {
			delete(f, "credential-encryption-key")
			f["credential-encryption-key-previous"] = credentialKey(2)
		}, "PREVIOUS is set without"},
		"previous is same":  {func(f map[string]string) { f["credential-encryption-key-previous"] = credentialKey(1) }, "must differ"},
		"previous is short": {func(f map[string]string) { f["credential-encryption-key-previous"] = "AAAA" }, "CREDENTIAL_ENCRYPTION_KEY_PREVIOUS: must decode"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			flags := valid()
			flags["credential-encryption-key"] = credentialKey(1)
			tc.mutate(flags)
			_, err := load(t, flags)
			require.ErrorContains(t, err, tc.want)
			assert.NotContains(t, err.Error(), credentialKey(1))
		})
	}
}

func TestTheGitHubAPIURLIsServerConfigurationAndIsValidated(t *testing.T) {
	flags := valid()
	flags["github-api-base-url"] = "https://ghe.example/api/v3/"
	cfg, err := load(t, flags)
	require.NoError(t, err)
	assert.Equal(t, "https://ghe.example/api/v3/", cfg.GitHubAPIBaseURL)

	for name, bad := range map[string]string{
		"relative": "api.github.com", "credentials": "https://u:p@api.github.com", "query": "https://x?y=1", "scheme": "ftp://x",
	} {
		flags["github-api-base-url"] = bad
		_, err := load(t, flags)
		assert.ErrorContains(t, err, "GITHUB_API_BASE_URL", name)
	}

	// The App's JWT would cross the network in the clear.
	flags["github-api-base-url"] = "http://ghe.example"
	_, err = load(t, flags)
	assert.ErrorContains(t, err, "plain http")
	flags["allow-insecure-http"] = "true"
	_, err = load(t, flags)
	assert.NoError(t, err)
}

func TestTheGitHubWebURLIsServerConfigurationAndIsValidated(t *testing.T) {
	cfg, err := load(t, valid())
	require.NoError(t, err)
	assert.Equal(t, "https://github.com", cfg.GitHubWebBaseURL)

	flags := valid()
	flags["github-web-base-url"] = "https://ghe.example"
	cfg, err = load(t, flags)
	require.NoError(t, err)
	assert.Equal(t, "https://ghe.example", cfg.GitHubWebBaseURL)

	for name, bad := range map[string]string{
		"relative": "github.com", "credentials": "https://u:p@github.com", "query": "https://x?y=1", "scheme": "ftp://x",
	} {
		flags["github-web-base-url"] = bad
		_, err := load(t, flags)
		assert.ErrorContains(t, err, "GITHUB_WEB_BASE_URL", name)
	}

	flags["github-web-base-url"] = "http://ghe.example"
	_, err = load(t, flags)
	assert.ErrorContains(t, err, "plain http")
	flags["allow-insecure-http"] = "true"
	_, err = load(t, flags)
	assert.NoError(t, err)
}

func TestSetupClaimGateIsOnByDefaultAndSwitchedOffByFalse(t *testing.T) {
	t.Setenv("SETUP_CLAIM_GATE", "")
	cfg, err := load(t, valid())
	require.NoError(t, err)
	assert.True(t, cfg.SetupClaimGate)

	off := valid()
	off["setup-claim-gate"] = "false"
	cfg, err = load(t, off)
	require.NoError(t, err)
	assert.False(t, cfg.SetupClaimGate)
}

// On unless set to false; the env var is only an off switch.
func TestAttemptsAddEnabledIsOnByDefaultAndSwitchedOffByFalse(t *testing.T) {
	t.Setenv("ATTEMPTS_ADD_ENABLED", "")
	cfg, err := load(t, valid())
	require.NoError(t, err)
	assert.True(t, cfg.AttemptsAddEnabled)

	off := valid()
	off["attempts-add-enabled"] = "false"
	cfg, err = load(t, off)
	require.NoError(t, err)
	assert.False(t, cfg.AttemptsAddEnabled)
}

// Unfinished work: off unless set to true.
func TestReplaysEnabledIsOffByDefaultAndSwitchedOnByTrue(t *testing.T) {
	t.Setenv("REPLAYS_ENABLED", "")
	cfg, err := load(t, valid())
	require.NoError(t, err)
	assert.False(t, cfg.ReplaysEnabled)

	on := valid()
	on["replays-enabled"] = "true"
	cfg, err = load(t, on)
	require.NoError(t, err)
	assert.True(t, cfg.ReplaysEnabled)

	t.Setenv("REPLAYS_ENABLED", "true")
	cfg, err = load(t, valid())
	require.NoError(t, err)
	assert.True(t, cfg.ReplaysEnabled, "the env var alone")
}

func TestAttemptsPromoteEnabledIsOnByDefaultAndSwitchedOffByFalse(t *testing.T) {
	t.Setenv("ATTEMPTS_PROMOTE_ENABLED", "")
	cfg, err := load(t, valid())
	require.NoError(t, err)
	assert.True(t, cfg.AttemptsPromoteEnabled)

	off := valid()
	off["attempts-promote-enabled"] = "false"
	cfg, err = load(t, off)
	require.NoError(t, err)
	assert.False(t, cfg.AttemptsPromoteEnabled)
}

func TestServerMergeEnabledIsOnByDefaultAndSwitchedOffByFalse(t *testing.T) {
	t.Setenv("SERVER_MERGE_ENABLED", "")
	cfg, err := load(t, valid())
	require.NoError(t, err)
	assert.True(t, cfg.ServerMergeEnabled)

	off := valid()
	off["server-merge-enabled"] = "false"
	cfg, err = load(t, off)
	require.NoError(t, err)
	assert.False(t, cfg.ServerMergeEnabled)
}

func TestExperimentReconcile(t *testing.T) {
	t.Setenv("EXPERIMENT_RECONCILE_INTERVAL", "")
	cfg, err := load(t, valid())
	require.NoError(t, err)
	assert.Equal(t, change.DefaultReconcileInterval, cfg.ExperimentReconcileInterval)

	flags := valid()
	flags["experiment-reconcile-interval"] = "3s"
	cfg, err = load(t, flags)
	require.NoError(t, err)
	assert.Equal(t, 3*time.Second, cfg.ExperimentReconcileInterval)

	flags = valid()
	flags["experiment-reconcile-interval"] = "-1s"
	_, err = load(t, flags)
	assert.Error(t, err)
}

func TestTenantOwnerLimit(t *testing.T) {
	cfg, err := load(t, valid())
	require.NoError(t, err)
	assert.Equal(t, 3, cfg.TenantOwnerLimit, "three owned tenants by default")

	off := valid()
	off["tenant-owner-limit"] = "0"
	cfg, err = load(t, off)
	require.NoError(t, err)
	assert.Zero(t, cfg.TenantOwnerLimit, "0 turns self-service creation off")

	bad := valid()
	bad["tenant-owner-limit"] = "-1"
	_, err = load(t, bad)
	assert.ErrorContains(t, err, "TENANT_OWNER_LIMIT")
}

func TestTenantOwnerTotalLimit(t *testing.T) {
	cfg, err := load(t, valid())
	require.NoError(t, err)
	assert.Equal(t, 10, cfg.TenantOwnerTotalLimit, "ten owned tenants including archived, by default")

	below := valid()
	below["tenant-owner-total-limit"] = "2"
	_, err = load(t, below)
	assert.ErrorContains(t, err, "TENANT_OWNER_TOTAL_LIMIT")

	equal := valid()
	equal["tenant-owner-total-limit"] = "3"
	_, err = load(t, equal)
	assert.NoError(t, err)
}

func TestMergeQueueEnabledIsOnByDefaultAndSwitchedOffByFalse(t *testing.T) {
	t.Setenv("MERGE_QUEUE_ENABLED", "")
	cfg, err := load(t, valid())
	require.NoError(t, err)
	assert.True(t, cfg.MergeQueueEnabled)

	off := valid()
	off["merge-queue-enabled"] = "false"
	cfg, err = load(t, off)
	require.NoError(t, err)
	assert.False(t, cfg.MergeQueueEnabled)
}

func TestWoodpecker(t *testing.T) {
	for _, name := range []string{"WOODPECKER_URL", "WOODPECKER_TOKEN_FILE", "WOODPECKER_REPO_ID", "WOODPECKER_BASE_BRANCH"} {
		t.Setenv(name, "")
	}
	cfg, err := load(t, valid())
	require.NoError(t, err)
	assert.Empty(t, cfg.Woodpecker.URL, "off unless configured")
	assert.Equal(t, "main", cfg.Woodpecker.BaseBranch)

	set := valid()
	set["woodpecker-url"], set["woodpecker-token-file"], set["woodpecker-repo-id"] = "https://ci.example.com", "/secrets/token", "7"
	cfg, err = load(t, set)
	require.NoError(t, err)
	assert.Equal(t, config.WoodpeckerConfig{URL: "https://ci.example.com", TokenFile: "/secrets/token", RepoID: 7, BaseBranch: "main"}, cfg.Woodpecker)

	for name, mutate := range map[string]func(map[string]string){
		"no token file": func(m map[string]string) { delete(m, "woodpecker-token-file") },
		"no repo id":    func(m map[string]string) { delete(m, "woodpecker-repo-id") },
		"bad url":       func(m map[string]string) { m["woodpecker-url"] = "ci.example.com" },
		"token without url": func(m map[string]string) {
			delete(m, "woodpecker-url")
		},
	} {
		t.Run(name, func(t *testing.T) {
			bad := valid()
			bad["woodpecker-url"], bad["woodpecker-token-file"], bad["woodpecker-repo-id"] = "https://ci.example.com", "/secrets/token", "7"
			mutate(bad)
			_, err := load(t, bad)
			assert.Error(t, err)
		})
	}
}

func TestAttachments(t *testing.T) {
	for _, name := range []string{"S3_ENDPOINT", "S3_CREDENTIALS_FILE", "S3_BUCKET", "S3_REGION", "ATTACHMENT_UPLOAD_GRACE", "ATTACHMENT_SWEEP_INTERVAL"} {
		t.Setenv(name, "")
	}
	cfg, err := load(t, valid())
	require.NoError(t, err)
	assert.False(t, cfg.Attachments.Enabled(), "off unless an object store is configured")

	good := func() map[string]string {
		m := valid()
		m["s3-endpoint"], m["s3-credentials-file"] = "http://rustfs:9000", "/secrets/s3"
		return m
	}
	cfg, err = load(t, good())
	require.NoError(t, err)
	assert.True(t, cfg.Attachments.Enabled())
	assert.Equal(t, config.AttachmentsConfig{
		S3Endpoint: "http://rustfs:9000", S3Region: "us-east-1", S3Bucket: "slop-haus-attachments", S3CredentialsFile: "/secrets/s3",
		SweepInterval: 15 * time.Minute, UploadGrace: 24 * time.Hour,
	}, cfg.Attachments)

	custom := good()
	custom["s3-region"], custom["s3-bucket"], custom["attachment-tmp-dir"] = "eu-west-1", "b", "/scratch"
	custom["attachment-sweep-interval"], custom["attachment-upload-grace"] = "1h", "2h"
	cfg, err = load(t, custom)
	require.NoError(t, err)
	assert.Equal(t, config.AttachmentsConfig{
		S3Endpoint: "http://rustfs:9000", S3Region: "eu-west-1", S3Bucket: "b", S3CredentialsFile: "/secrets/s3",
		TmpDir: "/scratch", SweepInterval: time.Hour, UploadGrace: 2 * time.Hour,
	}, cfg.Attachments)

	for name, mutate := range map[string]func(map[string]string){
		"no credentials file":   func(m map[string]string) { delete(m, "s3-credentials-file") },
		"credentials, no store": func(m map[string]string) { delete(m, "s3-endpoint") },
		"bad endpoint":          func(m map[string]string) { m["s3-endpoint"] = "rustfs:9000" },
		"no sweep":              func(m map[string]string) { m["attachment-sweep-interval"] = "0s" },
	} {
		t.Run(name, func(t *testing.T) {
			bad := good()
			mutate(bad)
			_, err := load(t, bad)
			assert.Error(t, err)
		})
	}
}

func TestMergeQueueApprovalEnabledIsOffByDefaultAndSwitchedOnByTrue(t *testing.T) {
	t.Setenv("MERGE_QUEUE_APPROVAL_ENABLED", "")
	cfg, err := load(t, valid())
	require.NoError(t, err)
	assert.False(t, cfg.MergeQueueApprovalEnabled)

	on := valid()
	on["merge-queue-approval-enabled"] = "true"
	cfg, err = load(t, on)
	require.NoError(t, err)
	assert.True(t, cfg.MergeQueueApprovalEnabled)
}
