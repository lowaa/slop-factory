package githubapp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/audit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/authn"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/secretbox"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/user"
)

var (
	// ErrDisabled means the server has no CREDENTIAL_ENCRYPTION_KEY, so it
	// cannot keep a credential safely and does not keep one.
	ErrDisabled = errors.New("githubapp: credentials are not configured on this server (CREDENTIAL_ENCRYPTION_KEY is unset)")
	// ErrNotOwner is anyone but an owner of the tenant changing or
	// verifying its credential.
	ErrNotOwner = errors.New("githubapp: only the tenant's owners can manage its code host credential")
	// ErrInvalidInput is a request that is not a credential.
	ErrInvalidInput = errors.New("githubapp: invalid credential")
	// ErrCredentialChanged means the credential was replaced while a token
	// was being minted from, or verified with, the old one; the result is
	// discarded. Retry.
	ErrCredentialChanged = errors.New("githubapp: the credential changed while it was being used")
	// ErrStoredKeyUnreadable is a stored key that decrypted but is not a
	// usable private key: the row is damaged and the owner must connect again.
	ErrStoredKeyUnreadable = errors.New("githubapp: stored key unreadable")
	// ErrRepoNotCovered means GitHub minted a token that does not cover the
	// repository asked for: the installation does not include it, or the
	// repo is another account's with a name the installation's also has.
	ErrRepoNotCovered = errors.New("githubapp: the installation does not cover that repository")
)

// Owners answers who may manage the credential, checked against the
// database each time rather than read from the session.
type Owners interface {
	IsTenantOwner(ctx context.Context, userID, tenantID uuid.UUID) (bool, error)
}

// Manifest is the repositories a tenant declared it works in, as
// normalised remotes ("github.com/acme/app").
type Manifest interface {
	Remotes(ctx context.Context) ([]string, error)
}

// Options are what NewService needs. Everything but Keyring, Manifest and Now
// is required; a nil Keyring is the feature switched off.
type Options struct {
	Store    Store
	Keyring  *secretbox.Keyring
	Client   *Client
	Owners   Owners
	Manifest Manifest
	Events   *audit.Recorder
	Logger   slog.Handler
	// WebBaseURL is where GitHub's own pages are, for the browser to be sent
	// to: https://github.com unless the operator runs GitHub Enterprise
	// Server. Like the API's, it is the server's configuration only.
	WebBaseURL string
	// BoardBaseURL is where the board is served, which GitHub redirects the
	// browser back to. Empty switches the manifest flow off (connecting by
	// hand still works).
	BoardBaseURL string
	// Webhook keeps the App's GitHub-side webhook pointed here. Nil leaves
	// the App without one: the manifest asks for no deliveries and the
	// board's webhook status says it is unavailable.
	Webhook *WebhookConfigurer
	// Deliveries is what the App's webhook has delivered, for the status
	// view. Nil is a server that records none.
	Deliveries AppDeliveries
	// Now is the clock; nil is time.Now.
	Now func() time.Time
}

// Service is a tenant's GitHub App credential: connecting it, checking it,
// and minting installation tokens from it.
type Service struct {
	store    Store
	keyring  *secretbox.Keyring
	client   *Client
	owners   Owners
	gate     *authn.OwnerGate
	manifest Manifest
	events   *audit.Recorder
	logger   *slog.Logger
	now      func() time.Time
	tokens   *tokenCache
	webhook  *WebhookConfigurer
	hooks    AppDeliveries
	web      *url.URL
	board    *url.URL
}

var _ codehost.Credentials = (*Service)(nil)

func NewService(opts *Options) (*Service, error) {
	if opts.Store == nil || opts.Client == nil || opts.Owners == nil || opts.Events == nil || opts.Logger == nil {
		return nil, errors.New("githubapp: Store, Client, Owners, Events and Logger are required")
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	web, err := parseBaseURL("web", opts.WebBaseURL, DefaultWebBaseURL)
	if err != nil {
		return nil, err
	}
	var board *url.URL
	if opts.BoardBaseURL != "" {
		if board, err = parseBaseURL("board", opts.BoardBaseURL, ""); err != nil {
			return nil, err
		}
	}
	return &Service{
		web: web, board: board,
		store: opts.Store, keyring: opts.Keyring, client: opts.Client, owners: opts.Owners, gate: authn.NewOwnerGate(opts.Owners, authn.WithRefusal(ErrNotOwner)),
		manifest: opts.Manifest, events: opts.Events, logger: slog.New(opts.Logger), now: now,
		tokens: newTokenCache(now), webhook: opts.Webhook, hooks: opts.Deliveries,
	}, nil
}

// Enabled is whether the server can keep credentials at all.
func (s *Service) Enabled() bool { return s.keyring != nil }

// Connected is whether the tenant (the context's) has a credential the
// server can use: one is stored and its key opens (decrypts and parses).
// It is what decides whether briefings send agents to the change tools, so
// a stored key the server can no longer open counts as not connected: the
// change tools would only fail, and the repo's own tool works.
//
// An unreadable key is the answer false, not an error, so a damaged row
// never fails the setup and update scripts that ask; the owner finds out
// from Verify and connects again. A failure to read the store is an error,
// since it says nothing about the credential. No host is called, and no key
// material leaves the method.
func (s *Service) Connected(ctx context.Context) (bool, error) {
	if !s.Enabled() {
		return false, nil
	}
	tenant, err := db.TenantFrom(ctx)
	if err != nil {
		return false, err
	}
	rec, err := s.store.Get(ctx)
	switch {
	case errors.Is(err, codehost.ErrNotConnected):
		return false, nil
	case err != nil:
		return false, err
	case rec.Pending():
		return false, nil
	}
	if _, err := s.openKey(tenant, rec); err != nil {
		if errors.Is(err, ErrStoredKeyUnreadable) {
			s.logger.Error("a tenant's stored code host key is not a usable private key", "tenant", tenant, "key_id", rec.KeyID)
		}
		return false, nil
	}
	return true, nil
}

// Status is what the board shows about the connection. Nothing in it is
// secret: the key itself is write-only.
type Status struct {
	Enabled bool
	// CanEdit is whether the asker is an owner.
	CanEdit bool
	// Credential is nil while nothing is connected.
	Credential *Record
}

func (s *Service) Status(ctx context.Context, who *authn.User) (*Status, error) {
	canEdit, err := s.owners.IsTenantOwner(ctx, who.ID, who.TenantID)
	if err != nil {
		return nil, err
	}
	st := &Status{Enabled: s.Enabled(), CanEdit: canEdit}
	rec, err := s.store.Get(ctx)
	switch {
	case errors.Is(err, codehost.ErrNotConnected):
	case err != nil:
		return nil, err
	default:
		st.Credential = rec
	}
	return st, nil
}

// Connection is what an owner supplies. PrivateKey is the PEM GitHub's "Generate a private key" downloads.
type Connection struct {
	AppID          int64
	InstallationID int64
	PrivateKey     []byte
}

// Connect stores a credential, replacing any the tenant has. The key is
// parsed first and refused unless it is a usable RSA key; it is sealed
// under the current key before it touches the database. A new credential
// is unverified until Verify says otherwise.
//
// Like adding a sign-in method, it needs a recent sign-in: whoever holds
// this credential can act on the tenant's repositories as slop-haus.
func (s *Service) Connect(ctx context.Context, who *authn.User, c *Connection) (*Record, error) {
	if !s.Enabled() {
		return nil, ErrDisabled
	}
	if err := s.gate.RequireOwner(ctx, who); err != nil {
		return nil, err
	}
	if err := user.RequireRecentAuth(who); err != nil {
		return nil, err
	}
	if c.AppID <= 0 || c.InstallationID <= 0 {
		return nil, fmt.Errorf("%w: the App id and the installation id are positive numbers", ErrInvalidInput)
	}
	key, err := ParsePrivateKey(c.PrivateKey)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidInput, err)
	}
	rec, err := s.seal(who.TenantID, key)
	if err != nil {
		return nil, err
	}
	rec.AppID, rec.InstallationID, rec.CreatedBy = c.AppID, c.InstallationID, &who.ID
	stored, err := s.store.Put(ctx, rec)
	if err != nil {
		return nil, err
	}
	s.tokens.forget(who.TenantID)
	s.events.Record(ctx, audit.CodeHostCredentialConnected, "user_id", who.ID, "tenant", who.TenantID,
		"app_id", stored.AppID, "installation_id", stored.InstallationID, "key_fingerprint", stored.Fingerprint)
	s.applyWebhook(ctx, who.TenantID)
	return stored, nil
}

// seal is a fresh row's worth of encrypted key, with the metadata that is
// derived from it.
func (s *Service) seal(tenant uuid.UUID, key *PrivateKey) (*Record, error) {
	pemBytes, err := key.PEM()
	if err != nil {
		return nil, err
	}
	fingerprint, err := key.Fingerprint()
	if err != nil {
		return nil, err
	}
	id := uuid.New()
	blob, err := s.keyring.Seal(pemBytes, secretbox.Binding{Tenant: tenant, Row: id})
	if err != nil {
		return nil, err
	}
	return &Record{ID: id, PrivateKeyEnc: blob, KeyID: s.keyring.CurrentID(), Fingerprint: fingerprint}, nil
}

// Remove deletes the credential and forgets every token minted from it.
// Tokens already handed out are not revocable here and run out on their
// own within the hour, so remove the installation on GitHub to end them.
func (s *Service) Remove(ctx context.Context, who *authn.User) error {
	if err := s.gate.RequireOwner(ctx, who); err != nil {
		return err
	}
	if err := user.RequireRecentAuth(who); err != nil {
		return err
	}
	removed, err := s.store.Delete(ctx)
	if err != nil {
		return err
	}
	s.tokens.forget(who.TenantID)
	if removed {
		s.events.Record(ctx, audit.CodeHostCredentialRemoved, "user_id", who.ID, "tenant", who.TenantID)
	}
	return nil
}

// RepoCheck is one of the manifest's repositories against what the
// installation can see.
type RepoCheck struct {
	Remote  string
	Visible bool
}

// Verification is the result of checking a credential against GitHub.
type Verification struct {
	// Account is who installed the App.
	Account string
	// Repos is every repository the installation can see.
	Repos []string
	// Manifest is the manifest's GitHub repositories against Repos; empty
	// while the tenant has declared none.
	Manifest []RepoCheck
}

// Verify proves the stored credential works end to end -- the key opens,
// GitHub accepts the App's signature and the installation exists -- and
// lists what the installation can see against the manifest. A credential
// sealed under a previous key is re-sealed under the current one.
func (s *Service) Verify(ctx context.Context, who *authn.User) (*Verification, error) {
	if !s.Enabled() {
		return nil, ErrDisabled
	}
	if err := s.gate.RequireOwner(ctx, who); err != nil {
		return nil, err
	}
	rec, err := s.store.Get(ctx)
	if err != nil {
		return nil, err
	}
	key, err := s.openKey(who.TenantID, rec)
	if err != nil {
		return nil, err
	}
	s.reseal(ctx, who.TenantID, rec, key)

	app, err := signAppJWT(key, rec.AppID, s.now())
	if err != nil {
		return nil, err
	}
	if rec.Pending() {
		if rec, err = s.adoptInstallation(ctx, who, rec, app); err != nil {
			return nil, err
		}
	}
	inst, err := s.client.installation(ctx, app, rec.InstallationID)
	if err != nil {
		return nil, err
	}
	perms, _ := permissionsFor(operationVerify)
	minted, err := s.client.installationToken(ctx, app, rec.InstallationID, &tokenRequest{Permissions: perms})
	if err != nil {
		return nil, err
	}
	repos, err := s.client.installationRepos(ctx, &minted.token)
	if err != nil {
		return nil, err
	}
	out := &Verification{Account: inst.Account, Repos: repos}
	if s.manifest != nil {
		remotes, err := s.manifest.Remotes(ctx)
		if err != nil {
			return nil, err
		}
		out.Manifest = compare(remotes, repos)
	}
	// Last, so a verification that failed anywhere is never on record as one.
	// A credential replaced while GitHub was being asked is not the one that
	// was checked, so the result is not reported as its.
	marked, err := s.store.MarkVerified(ctx, rec.ID)
	if err != nil {
		return nil, err
	}
	if !marked {
		return nil, ErrCredentialChanged
	}
	return out, nil
}

// compare checks each manifest remote on github.com against the
// installation's repositories; GitHub names are case-insensitive.
func compare(remotes, repos []string) []RepoCheck {
	visible := make(map[string]bool, len(repos))
	for _, r := range repos {
		visible[strings.ToLower(r)] = true
	}
	var out []RepoCheck
	for _, remote := range remotes {
		name, ok := strings.CutPrefix(strings.ToLower(remote), "github.com/")
		if !ok {
			continue
		}
		out = append(out, RepoCheck{Remote: remote, Visible: visible[name]})
	}
	return out
}

// openKey decrypts the tenant's key. A failure to is logged, not shown: the
// likely cause (the server's key was changed) is one the operator fixes,
// and the owner's remedy is to connect the credential again.
func (s *Service) openKey(tenant uuid.UUID, rec *Record) (*PrivateKey, error) {
	plain, err := s.keyring.Open(rec.PrivateKeyEnc, secretbox.Binding{Tenant: tenant, Row: rec.ID})
	if err != nil {
		s.logger.Error("cannot decrypt a tenant's code host credential", "tenant", tenant, "key_id", rec.KeyID)
		return nil, err
	}
	key, err := ParsePrivateKey(plain)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrStoredKeyUnreadable, err)
	}
	return key, nil
}

// reseal moves a credential sealed under a previous key onto the current
// one. Best effort: the credential works either way.
func (s *Service) reseal(ctx context.Context, tenant uuid.UUID, rec *Record, key *PrivateKey) {
	if rec.KeyID == s.keyring.CurrentID() {
		return
	}
	pemBytes, err := key.PEM()
	if err == nil {
		var blob []byte
		blob, err = s.keyring.Seal(pemBytes, secretbox.Binding{Tenant: tenant, Row: rec.ID})
		if err == nil {
			_, err = s.store.Reseal(ctx, rec.ID, blob, s.keyring.CurrentID())
		}
	}
	if err != nil {
		s.logger.Warn("could not re-seal a code host credential under the current key", "tenant", tenant, "err", err)
	}
}

var (
	ownerRE = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})$`)
	repoRE  = regexp.MustCompile(`^[A-Za-z0-9._-]{1,100}$`)
)

// TokenFor mints an installation token for op on repo, valid only for that
// repository and the permissions op needs (see permissionsFor). Tokens are
// cached in memory until shortly before they expire, and never stored.
//
// The cache is keyed on the stored credential's row id, read from the
// database each call, so a credential replaced or removed -- on any server
// -- is not served from the cache.
func (s *Service) TokenFor(ctx context.Context, tenant uuid.UUID, repo codehost.Repo, op codehost.Operation) (codehost.Token, error) {
	if !s.Enabled() {
		return nil, ErrDisabled
	}
	perms, err := permissionsFor(op)
	if err != nil {
		return nil, err
	}
	if !ownerRE.MatchString(repo.Owner) || !repoRE.MatchString(repo.Name) || repo.Name == "." || repo.Name == ".." {
		return nil, fmt.Errorf("%w: %q is not a repository name", ErrInvalidInput, repo.Owner+"/"+repo.Name)
	}
	ctx = db.WithTenant(ctx, tenant)
	rec, err := s.store.Get(ctx)
	if err != nil {
		return nil, err
	}
	if rec.Pending() {
		return nil, codehost.ErrNotConnected
	}
	key := tokenKey{tenant: tenant, credential: rec.ID, repo: strings.ToLower(repo.Owner + "/" + repo.Name), op: op}
	if tok := s.tokens.get(key); tok != nil {
		return tok, nil
	}

	pk, err := s.openKey(tenant, rec)
	if err != nil {
		return nil, err
	}
	app, err := signAppJWT(pk, rec.AppID, s.now())
	if err != nil {
		return nil, err
	}
	minted, err := s.client.installationToken(ctx, app, rec.InstallationID,
		&tokenRequest{Repositories: []string{repo.Name}, Permissions: perms})
	if err != nil {
		return nil, err
	}
	// `repositories` takes bare names, which are not unique across the
	// accounts an installation might span: check the token covers this
	// owner's repo and only it.
	if len(minted.repos) != 1 || !strings.EqualFold(minted.repos[0], repo.Owner+"/"+repo.Name) {
		return nil, ErrRepoNotCovered
	}
	// A credential replaced or removed while GitHub was being asked must not
	// hand out a token made from the old one.
	now, err := s.store.Get(ctx)
	if err != nil {
		return nil, err
	}
	if now.ID != rec.ID {
		return nil, ErrCredentialChanged
	}
	s.tokens.put(key, &minted.token)
	s.logger.Info("minted a code host token", "tenant", tenant, "repo", key.repo, "operation", string(op),
		"expires_at", minted.token.expiresAt)
	return &minted.token, nil
}

type tokenKey struct {
	tenant uuid.UUID
	// credential is the stored row's id, which every Connect changes.
	credential uuid.UUID
	repo       string
	op         codehost.Operation
}

// refreshMargin is how long before expiry a cached token stops being
// handed out: long enough that it will not expire mid-operation.
const refreshMargin = 5 * time.Minute

// maxCachedTokens bounds the cache; one tenant has a handful of repos and
// a few operations.
const maxCachedTokens = 1024

// tokenCache holds installation tokens in memory only. A lost cache costs
// one extra mint.
type tokenCache struct {
	now func() time.Time
	mu  sync.Mutex
	m   map[tokenKey]*Token
}

func newTokenCache(now func() time.Time) *tokenCache {
	return &tokenCache{now: now, m: map[tokenKey]*Token{}}
}

func (c *tokenCache) get(k tokenKey) *Token {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := c.m[k]
	if t == nil {
		return nil
	}
	if !c.now().Add(refreshMargin).Before(t.expiresAt) {
		delete(c.m, k)
		return nil
	}
	return t
}

func (c *tokenCache) put(k tokenKey, t *Token) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.m) >= maxCachedTokens {
		for key, tok := range c.m {
			if !c.now().Add(refreshMargin).Before(tok.expiresAt) {
				delete(c.m, key)
			}
		}
		if len(c.m) >= maxCachedTokens {
			clear(c.m)
		}
	}
	c.m[k] = t
}

func (c *tokenCache) forget(tenant uuid.UUID) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k := range c.m {
		if k.tenant == tenant {
			delete(c.m, k)
		}
	}
}
