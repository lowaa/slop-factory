// Package githubapp is slop-haus's GitHub App: its manifest-flow setup, installation tokens and webhook secret.
package githubapp

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/user"
)

var (
	// ErrWrongApp is an installation id that is not an installation of the
	// App this tenant created: the id came through the browser, so it is
	// never believed until GitHub says, to the App's own JWT, that it is.
	ErrWrongApp = errors.New("githubapp: that installation does not belong to this tenant's GitHub App")
	// ErrNotInstalled is a credential whose App GitHub knows no installation
	// of yet: nobody installed it, or an org owner has not approved it.
	ErrNotInstalled = errors.New("githubapp: the App is not installed yet; if an org owner has to approve it, it is waiting for them")
	// ErrAmbiguousInstallation is GitHub listing several installations of a
	// private App, which should be installable only on its owning account.
	ErrAmbiguousInstallation = errors.New("githubapp: GitHub lists more than one installation of the App; remove the extra installations of it on GitHub, or connect the App again")
	// ErrAlreadyInstalled is an install step for a credential that has one.
	ErrAlreadyInstalled = errors.New("githubapp: the App is already installed; replace the connection to start again")
)

// parseBaseURL checks a root URL taken from configuration. def is used when
// raw is empty.
func parseBaseURL(what, raw, def string) (*url.URL, error) {
	if raw == "" {
		raw = def
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" ||
		u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("githubapp: %s base URL must be an absolute http(s) URL without credentials, query or fragment", what)
	}
	u.Path = strings.TrimRight(u.Path, "/")
	return u, nil
}

// ManifestStart is what the browser needs to send the owner to GitHub's
// create-an-App page: it posts Manifest there, to ActionURL.
type ManifestStart struct {
	ActionURL string
	Manifest  string
}

// requireSetup is who may set up a credential: an owner with a recent
// sign-in, on a server that keeps credentials.
func (s *Service) requireSetup(ctx context.Context, who *authn.User) error {
	if !s.Enabled() {
		return ErrDisabled
	}
	if err := s.gate.RequireOwner(ctx, who); err != nil {
		return err
	}
	return user.RequireRecentAuth(who)
}

func (s *Service) issueState(ctx context.Context, who *authn.User, kind StateKind) (string, error) {
	token, hash, err := newState()
	if err != nil {
		return "", err
	}
	if err := s.store.IssueState(ctx, hash, who.ID, kind, s.now().Add(stateLifetime)); err != nil {
		return "", err
	}
	return token, nil
}

func (s *Service) consumeState(ctx context.Context, who *authn.User, kind StateKind, token string) error {
	ok, err := s.store.ConsumeState(ctx, hashState(token), who.ID, kind, s.now())
	if err != nil {
		return err
	}
	if !ok {
		return ErrBadState
	}
	return nil
}

// BeginManifest starts creating the tenant's App on GitHub. account is the
// organisation that should own it, or empty for the owner's own account.
func (s *Service) BeginManifest(ctx context.Context, who *authn.User, account string) (*ManifestStart, error) {
	if err := s.requireSetup(ctx, who); err != nil {
		return nil, err
	}
	if err := checkAccount(account); err != nil {
		return nil, err
	}
	manifest, err := buildManifest(s.board, s.hookURL())
	if err != nil {
		return nil, err
	}
	body, err := manifest.json()
	if err != nil {
		return nil, err
	}
	state, err := s.issueState(ctx, who, StateManifest)
	if err != nil {
		return nil, err
	}
	action, err := createAppURL(s.web, account, state)
	if err != nil {
		return nil, err
	}
	return &ManifestStart{ActionURL: action, Manifest: body}, nil
}

// CompleteManifest finishes creating the App: it checks the state, exchanges
// GitHub's code for the App and its private key, and stores them with no
// installation yet, replacing any credential the tenant had: the old
// connection stops working at once and stays down until the new App is
// installed. The key goes from GitHub's answer to the sealed row and nowhere
// else. It returns the page to install the App on.
func (s *Service) CompleteManifest(ctx context.Context, who *authn.User, code, state string) (*Record, string, error) {
	if err := s.requireSetup(ctx, who); err != nil {
		return nil, "", err
	}
	if !codeRE.MatchString(code) {
		return nil, "", fmt.Errorf("%w: that is not a code GitHub issues", ErrInvalidInput)
	}
	// Spent before GitHub is asked: the code works once, so a failed exchange
	// could not be retried with the same state anyway.
	if err := s.consumeState(ctx, who, StateManifest, state); err != nil {
		return nil, "", err
	}
	app, err := s.client.convertManifest(ctx, code)
	if err != nil {
		return nil, "", err
	}
	key, err := ParsePrivateKey(app.pem)
	if err != nil {
		return nil, "", fmt.Errorf("%w: %w", ErrBadResponse, err)
	}
	// Refused before anything is stored if GitHub's slug cannot go in a URL.
	if _, err := installAppURL(s.web, app.slug, "x"); err != nil {
		return nil, "", err
	}
	rec, err := s.seal(who.TenantID, key)
	if err != nil {
		return nil, "", err
	}
	rec.AppID, rec.AppSlug, rec.CreatedBy = app.appID, app.slug, &who.ID
	stored, err := s.store.Put(ctx, rec)
	if err != nil {
		return nil, "", err
	}
	s.tokens.forget(who.TenantID)
	s.events.Record(ctx, audit.CodeHostCredentialConnected, "user_id", who.ID, "tenant", who.TenantID,
		"app_id", stored.AppID, "key_fingerprint", stored.Fingerprint, "via", "manifest")
	// From the App just stored, not read back: another setup finishing at the
	// same time may have replaced the row, and this one must lead to its own App.
	installURL, err := s.installURL(ctx, who, stored.AppSlug)
	if err != nil {
		return nil, "", err
	}
	return stored, installURL, nil
}

// InstallURL is the page to install the tenant's App from, carrying a state
// for the way back. Only an App made through the manifest has one.
func (s *Service) InstallURL(ctx context.Context, who *authn.User) (string, error) {
	if err := s.requireSetup(ctx, who); err != nil {
		return "", err
	}
	rec, err := s.store.Get(ctx)
	if err != nil {
		return "", err
	}
	if rec.AppSlug == "" {
		return "", fmt.Errorf("%w: this App was connected by hand; install it on GitHub", ErrInvalidInput)
	}
	return s.installURL(ctx, who, rec.AppSlug)
}

func (s *Service) installURL(ctx context.Context, who *authn.User, slug string) (string, error) {
	state, err := s.issueState(ctx, who, StateInstall)
	if err != nil {
		return "", err
	}
	return installAppURL(s.web, slug, state)
}

// InstallResult is how an install step ended.
type InstallResult struct {
	Record *Record
	// Requested is true when the person is not an org owner and GitHub sent
	// the request to one: the credential stays pending.
	Requested bool
}

// CompleteInstall takes the browser's word that GitHub installed the App,
// and does not believe it: the installation id is accepted only if GitHub,
// asked as the App itself, says it is an installation of that App.
// setupAction "request" is an org member asking an owner; nothing is saved
// and Verify picks the installation up once it exists.
func (s *Service) CompleteInstall(ctx context.Context, who *authn.User, installationID int64, setupAction, state string) (*InstallResult, error) {
	if err := s.requireSetup(ctx, who); err != nil {
		return nil, err
	}
	if err := s.consumeState(ctx, who, StateInstall, state); err != nil {
		return nil, err
	}
	rec, err := s.store.Get(ctx)
	if err != nil {
		return nil, err
	}
	if setupAction == "request" {
		return &InstallResult{Record: rec, Requested: true}, nil
	}
	if installationID <= 0 {
		return nil, fmt.Errorf("%w: no installation id", ErrInvalidInput)
	}
	if !rec.Pending() {
		return nil, ErrAlreadyInstalled
	}
	key, err := s.openKey(who.TenantID, rec)
	if err != nil {
		return nil, err
	}
	app, err := signAppJWT(key, rec.AppID, s.now())
	if err != nil {
		return nil, err
	}
	inst, err := s.client.installation(ctx, app, installationID)
	var apiErr *APIError
	if errors.As(err, &apiErr) && (apiErr.Status == 404 || apiErr.Status == 403) {
		return nil, ErrWrongApp
	}
	if err != nil {
		return nil, err
	}
	if inst.AppID != rec.AppID {
		return nil, ErrWrongApp
	}
	stored, err := s.store.SetInstallation(ctx, rec.ID, installationID)
	if err != nil {
		return nil, err
	}
	s.tokens.forget(who.TenantID)
	s.events.Record(ctx, audit.CodeHostCredentialConnected, "user_id", who.ID, "tenant", who.TenantID,
		"app_id", stored.AppID, "installation_id", stored.InstallationID, "via", "install")
	s.applyWebhook(ctx, who.TenantID)
	return &InstallResult{Record: stored}, nil
}

// adoptInstallation finds the installation a pending credential waits for,
// by asking GitHub as the App. The App is private, so it can be installed
// only on its owning account: one installation is the one; none means it is
// still waiting.
func (s *Service) adoptInstallation(ctx context.Context, who *authn.User, rec *Record, app appJWT) (*Record, error) {
	installs, err := s.client.appInstallations(ctx, app)
	if err != nil {
		return nil, err
	}
	var mine *Installation
	for _, inst := range installs {
		if inst.AppID != rec.AppID {
			continue
		}
		if mine != nil {
			return nil, ErrAmbiguousInstallation
		}
		mine = inst
	}
	if mine == nil || mine.ID <= 0 {
		return nil, ErrNotInstalled
	}
	stored, err := s.store.SetInstallation(ctx, rec.ID, mine.ID)
	if err != nil {
		return nil, err
	}
	s.tokens.forget(who.TenantID)
	s.events.Record(ctx, audit.CodeHostCredentialConnected, "user_id", who.ID, "tenant", who.TenantID,
		"app_id", stored.AppID, "installation_id", stored.InstallationID, "via", "verify")
	s.applyWebhook(ctx, who.TenantID)
	return stored, nil
}
