package githubapp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/secretbox"
)

// AppWebhookPath is where the App's deliveries are received, appended to
// the environment's WEBHOOK_PUBLIC_BASE_URL.
const AppWebhookPath = "/hooks/github-app"

// webhookSecretPurpose separates the sealed webhook secret from the sealed
// private key of the same row.
const webhookSecretPurpose = "webhook secret"

// deliveriesLimit is how many recent deliveries the status view reads.
const deliveriesLimit = 30

var (
	// ErrNoWebhookSecret means no webhook secret is stored for the App, so
	// there is nothing to open.
	ErrNoWebhookSecret = errors.New("githubapp: no webhook secret is stored for the App")
	// ErrInstallationShared means another tenant of this deployment already
	// has the webhook set up for the same installation. GitHub holds one
	// secret per App, so setting this tenant's would overwrite theirs and
	// stop their deliveries verifying; the second tenant keeps its repo
	// webhooks instead.
	ErrInstallationShared = errors.New("githubapp: another workspace on this server already receives this installation's webhooks")
)

// ForeignWebhookError means GitHub's webhook URL for the App is on another
// host than this environment's own. An App has one webhook URL, so two
// environments sharing one would overwrite each other's on every
// re-apply; the URL is left alone unless the caller says to take it over.
type ForeignWebhookError struct {
	// Current is the URL GitHub holds (a URL, not a secret).
	Current  string
	Expected string
}

func (e *ForeignWebhookError) Error() string {
	return fmt.Sprintf("githubapp: the App's webhook URL points at %s, not at this environment (%s)", e.Current, e.Expected)
}

// WebhookOptions configure a WebhookConfigurer.
type WebhookOptions struct {
	Store   Store
	Keyring *secretbox.Keyring
	Client  *Client
	Logger  slog.Handler
	// PublicBaseURL is WEBHOOK_PUBLIC_BASE_URL: this environment's own
	// origin, with no path.
	PublicBaseURL string
	// Now is the clock; nil is time.Now.
	Now func() time.Time
}

// WebhookConfigurer keeps an App's GitHub-side webhook pointed at this
// environment with a secret only slop-haus knows. Every method runs in the
// context's tenant and acts on its credential; who may ask is the caller's
// to decide.
//
// Each change is made on GitHub first and stored second, under the tenant's
// webhook lock, so a stored secret is never one GitHub does not have. A
// failure between the two (the store failing, or the credential replaced
// meanwhile) leaves GitHub with a secret that was not stored: Apply and
// Rotate replace it, and a replaced credential has no stored secret, which
// Reconcile applies afresh. Reconcile alone does not see the first case.
type WebhookConfigurer struct {
	store    Store
	keyring  *secretbox.Keyring
	client   *Client
	logger   *slog.Logger
	now      func() time.Time
	expected string
	host     string
}

func NewWebhookConfigurer(opts *WebhookOptions) (*WebhookConfigurer, error) {
	if opts.Store == nil || opts.Keyring == nil || opts.Client == nil || opts.Logger == nil {
		return nil, errors.New("githubapp: Store, Keyring, Client and Logger are required")
	}
	base, err := url.Parse(opts.PublicBaseURL)
	if err != nil || (base.Scheme != "https" && base.Scheme != "http") || base.Host == "" ||
		(base.Path != "" && base.Path != "/") || base.RawQuery != "" || base.Fragment != "" || base.User != nil {
		return nil, errors.New("githubapp: webhook public base URL must be an absolute http(s) origin with no path")
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &WebhookConfigurer{
		store: opts.Store, keyring: opts.Keyring, client: opts.Client, logger: slog.New(opts.Logger), now: now,
		expected: strings.TrimRight(opts.PublicBaseURL, "/") + AppWebhookPath,
		host:     strings.ToLower(base.Host),
	}, nil
}

// ExpectedURL is the URL the App's webhook should have.
func (w *WebhookConfigurer) ExpectedURL() string { return w.expected }

// WebhookOutcome is what a call did to GitHub's configuration.
type WebhookOutcome string

const (
	// WebhookApplied: GitHub's configuration was set.
	WebhookApplied WebhookOutcome = "applied"
	// WebhookUnchanged: GitHub's URL already matched and nothing was sent.
	WebhookUnchanged WebhookOutcome = "unchanged"
)

// WebhookResult reports a call. GitHubURL is what GitHub held before it.
type WebhookResult struct {
	Outcome   WebhookOutcome
	GitHubURL string
}

// ApplyOptions modify Apply.
type ApplyOptions struct {
	// TakeOver replaces a URL on another host. It is the owner's explicit
	// word that the App is not shared with another environment.
	TakeOver bool
}

type webhookTarget struct {
	tenant uuid.UUID
	rec    *Record
	app    appJWT
}

func (w *WebhookConfigurer) target(ctx context.Context) (*webhookTarget, error) {
	tenant, err := db.TenantFrom(ctx)
	if err != nil {
		return nil, err
	}
	rec, err := w.store.Get(ctx)
	if err != nil {
		return nil, err
	}
	// A delivery names its installation, which is how the receiver finds
	// the tenant: with none there is nothing to receive for.
	if rec.Pending() {
		return nil, ErrNotInstalled
	}
	plain, err := w.keyring.Open(rec.PrivateKeyEnc, secretbox.Binding{Tenant: tenant, Row: rec.ID})
	if err != nil {
		w.logger.Error("cannot decrypt a tenant's code host credential", "tenant", tenant, "key_id", rec.KeyID)
		return nil, err
	}
	key, err := ParsePrivateKey(plain)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrStoredKeyUnreadable, err)
	}
	app, err := signAppJWT(key, rec.AppID, w.now())
	if err != nil {
		return nil, err
	}
	return &webhookTarget{tenant: tenant, rec: rec, app: app}, nil
}

func (w *WebhookConfigurer) binding(t *webhookTarget) secretbox.Binding {
	return secretbox.Binding{Tenant: t.tenant, Row: t.rec.ID, Purpose: webhookSecretPurpose}
}

// openSecret is the stored webhook secret, ErrNoWebhookSecret if none.
func (w *WebhookConfigurer) openSecret(t *webhookTarget) (*WebhookSecret, error) {
	if t.rec.WebhookSecretEnc == nil {
		return nil, ErrNoWebhookSecret
	}
	plain, err := w.keyring.Open(t.rec.WebhookSecretEnc, w.binding(t))
	if err != nil {
		w.logger.Error("cannot decrypt a tenant's webhook secret", "tenant", t.tenant, "key_id", t.rec.WebhookSecretKeyID)
		return nil, err
	}
	return &WebhookSecret{value: string(plain)}, nil
}

// checkHost refuses a URL GitHub holds on another host than this
// environment's. An empty URL (an App never given one) is nobody's.
func (w *WebhookConfigurer) checkHost(current string) error {
	if current == "" {
		return nil
	}
	u, err := url.Parse(current)
	if err == nil && strings.ToLower(u.Host) == w.host {
		return nil
	}
	return &ForeignWebhookError{Current: current, Expected: w.expected}
}

// push sets GitHub's configuration to this environment's URL and secret,
// then stores the secret and URL. GitHub is told first.
func (w *WebhookConfigurer) push(ctx context.Context, t *webhookTarget, secret *WebhookSecret) error {
	held, err := w.store.InstallationHeldByOther(ctx)
	if err != nil {
		return err
	}
	if held {
		return ErrInstallationShared
	}
	blob, err := w.keyring.Seal([]byte(secret.Reveal()), w.binding(t))
	if err != nil {
		return err
	}
	if err := w.client.setAppHookConfig(ctx, t.app, w.expected, secret); err != nil {
		return err
	}
	ok, err := w.store.SetWebhook(ctx, t.rec.ID, blob, w.keyring.CurrentID(), w.expected)
	if err != nil {
		return err
	}
	if !ok {
		return ErrCredentialChanged
	}
	w.logger.Info("applied a GitHub App webhook configuration", "tenant", t.tenant, "app_id", t.rec.AppID, "url", w.expected)
	return nil
}

// Apply points the App's webhook at this environment with the stored
// secret, generating one if there is none. A URL on another host is
// refused with *ForeignWebhookError unless opts.TakeOver.
func (w *WebhookConfigurer) Apply(ctx context.Context, opts ApplyOptions) (*WebhookResult, error) {
	var res *WebhookResult
	err := w.store.WithWebhookLock(ctx, func(ctx context.Context) (err error) {
		res, err = w.applyLocked(ctx, opts)
		return err
	})
	return res, err
}

func (w *WebhookConfigurer) applyLocked(ctx context.Context, opts ApplyOptions) (*WebhookResult, error) {
	t, err := w.target(ctx)
	if err != nil {
		return nil, err
	}
	cfg, err := w.client.appHookConfig(ctx, t.app)
	if err != nil {
		return nil, err
	}
	return w.apply(ctx, t, cfg, opts)
}

func (w *WebhookConfigurer) apply(ctx context.Context, t *webhookTarget, cfg *HookConfig, opts ApplyOptions) (*WebhookResult, error) {
	if !opts.TakeOver {
		if err := w.checkHost(cfg.URL); err != nil {
			return nil, err
		}
	}
	secret, err := w.openSecret(t)
	if errors.Is(err, ErrNoWebhookSecret) {
		secret, err = newWebhookSecret()
	}
	if err != nil {
		return nil, err
	}
	if err := w.push(ctx, t, secret); err != nil {
		return nil, err
	}
	return &WebhookResult{Outcome: WebhookApplied, GitHubURL: cfg.URL}, nil
}

// Reconcile reads GitHub's configuration and applies only when it is not
// what this environment expects: a URL that differs, or no secret stored
// (GitHub's is then one nobody here knows). A configuration that matches is
// left untouched, bar re-sealing the secret under a newer key.
func (w *WebhookConfigurer) Reconcile(ctx context.Context) (*WebhookResult, error) {
	var res *WebhookResult
	err := w.store.WithWebhookLock(ctx, func(ctx context.Context) (err error) {
		res, err = w.reconcileLocked(ctx)
		return err
	})
	return res, err
}

func (w *WebhookConfigurer) reconcileLocked(ctx context.Context) (*WebhookResult, error) {
	t, err := w.target(ctx)
	if err != nil {
		return nil, err
	}
	cfg, err := w.client.appHookConfig(ctx, t.app)
	if err != nil {
		return nil, err
	}
	if cfg.URL == w.expected && t.rec.WebhookSecretEnc != nil {
		w.reseal(ctx, t)
		return &WebhookResult{Outcome: WebhookUnchanged, GitHubURL: cfg.URL}, nil
	}
	return w.apply(ctx, t, cfg, ApplyOptions{})
}

// Rotate sets a newly generated secret on GitHub, then stores it. If
// GitHub refuses, the stored secret is unchanged and still the one GitHub
// has. The URL is checked as in Apply.
func (w *WebhookConfigurer) Rotate(ctx context.Context, opts ApplyOptions) error {
	return w.store.WithWebhookLock(ctx, func(ctx context.Context) error { return w.rotateLocked(ctx, opts) })
}

func (w *WebhookConfigurer) rotateLocked(ctx context.Context, opts ApplyOptions) error {
	t, err := w.target(ctx)
	if err != nil {
		return err
	}
	cfg, err := w.client.appHookConfig(ctx, t.app)
	if err != nil {
		return err
	}
	if !opts.TakeOver {
		if err := w.checkHost(cfg.URL); err != nil {
			return err
		}
	}
	secret, err := newWebhookSecret()
	if err != nil {
		return err
	}
	return w.push(ctx, t, secret)
}

// Deliveries are the App's most recent webhook deliveries, for the status
// view.
func (w *WebhookConfigurer) Deliveries(ctx context.Context) ([]HookDelivery, error) {
	t, err := w.target(ctx)
	if err != nil {
		return nil, err
	}
	return w.client.appHookDeliveries(ctx, t.app, deliveriesLimit)
}

// reseal moves the stored secret onto the current key. Best effort: the
// secret works either way.
func (w *WebhookConfigurer) reseal(ctx context.Context, t *webhookTarget) {
	if t.rec.WebhookSecretKeyID == w.keyring.CurrentID() {
		return
	}
	secret, err := w.openSecret(t)
	if err == nil {
		var blob []byte
		blob, err = w.keyring.Seal([]byte(secret.Reveal()), w.binding(t))
		if err == nil {
			_, err = w.store.ResealWebhook(ctx, t.rec.ID, blob, w.keyring.CurrentID())
		}
	}
	if err != nil {
		w.logger.Warn("could not re-seal a webhook secret under the current key", "tenant", t.tenant, "err", err)
	}
}
