package main

import (
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/lowaa/slop-factory/go-auth/ratelimit"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/config"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/metrics"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/oauth"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/user"
)

// The policy the services are built with, spelled out in full: changing a
// limit or a lifetime changes this test as well as the value, so neither
// happens by accident.

type policyRig struct {
	cfg       *config.Config
	telemetry *metrics.Metrics
	limits    *limiters
}

func newPolicyRig() *policyRig {
	telemetry := metrics.New(&metrics.Options{})
	return &policyRig{
		cfg: &config.Config{
			PublicBaseURL: "https://slop.example", LoginClientLimit: 30, MailClientLimit: 20, TenantOwnerLimit: 3, TenantOwnerTotalLimit: 10,
			ActivationTTL: 24 * time.Hour, ResetTTL: time.Hour, InviteTTL: 7 * 24 * time.Hour,
			RegistrationOpen: true, PasswordSignIn: false,
		},
		telemetry: telemetry,
		limits:    &limiters{telemetry: telemetry, logHandler: slog.DiscardHandler},
	}
}

// limit is a limiter as every one is built: bounded in keys, reporting to
// the metrics and the log.
func (r *policyRig) limit(name string, n int, window time.Duration) *ratelimit.Config {
	return &ratelimit.Config{Name: name, Limit: n, Window: window, MaxKeys: 100_000,
		Logger: slog.DiscardHandler, Observer: r.telemetry}
}

func TestAgentOAuthConfig(t *testing.T) {
	t.Parallel()
	r := newPolicyRig()
	assert.Equal(t, &oauth.Config{
		Issuer:              "https://slop.example",
		AccessTTL:           15 * time.Minute,
		RefreshTTL:          30 * 24 * time.Hour,
		CodeTTL:             time.Minute,
		AuthRequestTTL:      10 * time.Minute,
		DeviceTTL:           5 * time.Minute,
		PollInterval:        5 * time.Second,
		RegisterLimit:       r.limit("oauth_register", 20, time.Hour),
		DeviceLookupLimit:   r.limit("device_lookup", 20, 10*time.Minute),
		DeviceStartLimit:    r.limit("device_start", 60, time.Hour),
		SlotCredentialLimit: r.limit("slot_credential", 60, time.Hour),
		MaxUnclaimedClients: 1000,
	}, agentOAuthConfig(r.cfg, r.limits))
}

func TestUsersConfig(t *testing.T) {
	t.Parallel()
	r := newPolicyRig()
	assert.Equal(t, &user.Config{
		PublicBaseURL:          "https://slop.example",
		ActivationTTL:          24 * time.Hour,
		ResetTTL:               time.Hour,
		InviteTTL:              7 * 24 * time.Hour,
		RegistrationOpen:       true,
		PasswordSignInDisabled: true,
		LoginLimit:             r.limit("login", 10, time.Minute),
		MailLimit:              r.limit("mail", 5, time.Hour),
		LoginClientLimit:       r.limit("login_client", 30, 10*time.Minute),
		MailClientLimit:        r.limit("mail_client", 20, time.Hour),
		InviteLimit:            r.limit("invite", 30, 24*time.Hour),
		TenantCreateLimit:      r.limit("tenant_create", 10, time.Hour),
		TenantOwnerLimit:       3,
		TenantOwnerTotalLimit:  10,
	}, usersConfig(r.cfg, r.limits))
}

func TestOutboxConfig(t *testing.T) {
	t.Parallel()
	assert.Equal(t, &user.OutboxConfig{QueueSize: 256, SendTimeout: 30 * time.Second}, outboxConfig())
}

func TestOtelProxyLimit(t *testing.T) {
	t.Parallel()
	r := newPolicyRig()
	assert.Equal(t, r.limit("otel_proxy", 30, time.Minute), r.limits.limit(config.OtelProxyLimit()))
}
