package githubapp

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/user"
)

// ErrWebhookUnavailable means this server has no App webhook door to point
// GitHub at (its public address or the credential key is unset).
var ErrWebhookUnavailable = errors.New("githubapp: this server does not receive GitHub App webhooks")

// AppDelivery is one delivery that came through the App, as the status view
// shows it.
type AppDelivery struct {
	Event      string
	Action     string
	Outcome    string
	ReceivedAt time.Time
}

// AppDeliveries reads what the App's webhook delivered to the context's
// tenant.
type AppDeliveries interface {
	// Latest is the newest delivery of any event, nil if none.
	Latest(ctx context.Context) (*AppDelivery, error)
	// LatestOf is the newest delivery of one of events, nil if none. A
	// ping proves the URL is reachable; only one of these proves the App is
	// subscribed to the events that matter.
	LatestOf(ctx context.Context, events []string) (*AppDelivery, error)
}

// WebhookState is how far the App's webhook is from delivering reviews.
type WebhookState string

const (
	// WebhookUnavailable: nothing to configure -- the server has no door, no
	// App is connected, or it is not installed yet.
	WebhookUnavailable WebhookState = "unavailable"
	// WebhookNotApplied: slop-haus has not set its URL and secret on the App.
	WebhookNotApplied WebhookState = "not_applied"
	// WebhookSharedInstallation: another workspace on this server owns the
	// webhook for this installation, so this one is given none.
	WebhookSharedInstallation WebhookState = "shared_installation"
	// WebhookURLDiffers: the URL slop-haus last set is not this
	// environment's.
	WebhookURLDiffers WebhookState = "url_differs"
	// WebhookAwaitingEvents: configured, but no pull request event has
	// arrived. An existing App's events cannot be set through the API, so
	// the owner may still have to tick them; this stays until one arrives.
	WebhookAwaitingEvents WebhookState = "awaiting_events"
	// WebhookReceiving: a pull request event has arrived through the App.
	WebhookReceiving WebhookState = "receiving"
)

// WebhookStatus is what the board shows about the App's webhook. Nothing in
// it is secret.
type WebhookStatus struct {
	State   WebhookState
	CanEdit bool
	// ExpectedURL is where this environment receives the App's deliveries;
	// empty when it does not.
	ExpectedURL string
	// ConfiguredURL is the URL slop-haus last set on GitHub, empty if never.
	ConfiguredURL string
	AppliedAt     *time.Time
	// LastDelivery is the newest delivery of any event, LastEvent the
	// newest of AppWebhookEvents.
	LastDelivery *AppDelivery
	LastEvent    *AppDelivery
}

func (s *Service) webhookEnabled() bool { return s.webhook != nil }

func (s *Service) hookURL() string {
	if s.webhook == nil {
		return ""
	}
	return s.webhook.ExpectedURL()
}

// applyWebhook points the just-connected App's webhook here. It never fails
// the connect it follows: the App works without it, and the board's status
// view says what is left to do.
func (s *Service) applyWebhook(ctx context.Context, tenant uuid.UUID) {
	if !s.webhookEnabled() || !s.Enabled() {
		return
	}
	_, err := s.webhook.Apply(ctx, ApplyOptions{})
	s.logWebhookResult(tenant, "apply", err)
}

func (s *Service) logWebhookResult(tenant uuid.UUID, what string, err error) {
	var foreign *ForeignWebhookError
	switch {
	case err == nil:
	case errors.As(err, &foreign), errors.Is(err, ErrInstallationShared), errors.Is(err, ErrNotInstalled):
		s.logger.Info("left a GitHub App webhook alone", "tenant", tenant, "step", what, "reason", err.Error())
	default:
		s.logger.Warn("could not configure a GitHub App webhook", "tenant", tenant, "step", what, "err", err)
	}
}

// ReconcileWebhooks checks every tenant's App against this environment,
// applying where GitHub's configuration is not what it should be. It runs at
// startup, so a changed WEBHOOK_PUBLIC_BASE_URL reaches Apps without anyone
// pressing Check. One tenant's failure does not stop the others.
func (s *Service) ReconcileWebhooks(ctx context.Context) error {
	if !s.webhookEnabled() || !s.Enabled() {
		return nil
	}
	tenants, err := s.store.Tenants(ctx)
	if err != nil {
		return err
	}
	for _, tenant := range tenants {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		tctx := db.WithTenant(ctx, tenant)
		_, err := s.webhook.Reconcile(tctx)
		if errors.Is(err, codehost.ErrNotConnected) {
			continue
		}
		s.logWebhookResult(tenant, "reconcile", err)
	}
	return nil
}

// WebhookStatus is the App's webhook as slop-haus knows it, from its own
// records alone: GitHub is asked only by CheckWebhook.
func (s *Service) WebhookStatus(ctx context.Context, who *authn.User) (*WebhookStatus, error) {
	canEdit, err := s.owners.IsTenantOwner(ctx, who.ID, who.TenantID)
	if err != nil {
		return nil, err
	}
	return s.webhookStatus(ctx, canEdit)
}

func (s *Service) webhookStatus(ctx context.Context, canEdit bool) (*WebhookStatus, error) {
	st := &WebhookStatus{State: WebhookUnavailable, CanEdit: canEdit, ExpectedURL: s.hookURL()}
	if !s.webhookEnabled() || !s.Enabled() {
		return st, nil
	}
	rec, err := s.store.Get(ctx)
	if errors.Is(err, codehost.ErrNotConnected) {
		return st, nil
	}
	if err != nil {
		return nil, err
	}
	if rec.Pending() {
		return st, nil
	}
	st.ConfiguredURL, st.AppliedAt = rec.WebhookURL, rec.WebhookAppliedAt
	if s.hooks != nil {
		if st.LastDelivery, err = s.hooks.Latest(ctx); err != nil {
			return nil, err
		}
		if st.LastEvent, err = s.hooks.LatestOf(ctx, AppWebhookEvents); err != nil {
			return nil, err
		}
	}
	switch {
	case rec.WebhookSecretEnc == nil:
		st.State = WebhookNotApplied
		held, err := s.store.InstallationHeldByOther(ctx)
		if err != nil {
			return nil, err
		}
		if held {
			st.State = WebhookSharedInstallation
		}
	case rec.WebhookURL != st.ExpectedURL:
		st.State = WebhookURLDiffers
	case st.LastEvent == nil:
		st.State = WebhookAwaitingEvents
	default:
		st.State = WebhookReceiving
	}
	return st, nil
}

func (s *Service) requireWebhookOwner(ctx context.Context, who *authn.User) error {
	if !s.Enabled() {
		return ErrDisabled
	}
	if !s.webhookEnabled() {
		return ErrWebhookUnavailable
	}
	return s.gate.RequireOwner(ctx, who)
}

// CheckWebhook reads GitHub's configuration and puts right what differs
// from this environment's, then reports the status. takeOver is the owner
// saying the App is not shared with another environment, which replaces a
// URL on another host.
func (s *Service) CheckWebhook(ctx context.Context, who *authn.User, takeOver bool) (outcome WebhookOutcome, st *WebhookStatus, err error) {
	if err := s.requireWebhookOwner(ctx, who); err != nil {
		return "", nil, err
	}
	var res *WebhookResult
	if takeOver {
		res, err = s.webhook.Apply(ctx, ApplyOptions{TakeOver: true})
	} else {
		res, err = s.webhook.Reconcile(ctx)
	}
	if err != nil {
		return "", nil, err
	}
	st, err = s.webhookStatus(ctx, true)
	return res.Outcome, st, err
}

// RotateWebhook replaces the secret GitHub signs the App's deliveries with.
// Like Connect it needs a recent sign-in: the secret is what makes a
// delivery believed.
func (s *Service) RotateWebhook(ctx context.Context, who *authn.User) (*WebhookStatus, error) {
	if err := s.requireWebhookOwner(ctx, who); err != nil {
		return nil, err
	}
	if err := user.RequireRecentAuth(who); err != nil {
		return nil, err
	}
	if err := s.webhook.Rotate(ctx, ApplyOptions{}); err != nil {
		return nil, err
	}
	s.events.Record(ctx, audit.CodeHostWebhookRotated, "user_id", who.ID, "tenant", who.TenantID)
	return s.webhookStatus(ctx, true)
}
