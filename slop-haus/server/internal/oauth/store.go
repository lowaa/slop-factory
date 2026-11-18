package oauth

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
)

// Store is the persistence Service needs. Everything is in global_ tables
// and goes through the global handle: a token arrives before anything
// knows its tenant.
type Store interface {
	// CreateClient records c unless maxUnclaimed or more registered clients
	// have never been authorized -- what an anonymous flood of
	// registrations leaves behind -- in which case ErrTooManyClients.
	CreateClient(ctx context.Context, c *Client, maxUnclaimed int) error
	GetClient(ctx context.Context, id string) (*Client, error)

	CreateAuthRequest(ctx context.Context, r *AuthRequest) error
	GetAuthRequest(ctx context.Context, id uuid.UUID) (*AuthRequest, *Client, error)
	// ApproveAuthRequest spends the request and records the grant and its
	// code, together. ErrNotFound if the request is gone or decided.
	ApproveAuthRequest(ctx context.Context, requestID uuid.UUID, g *Grant, codeHash string, codeExpires time.Time) (*AuthRequest, error)
	DenyAuthRequest(ctx context.Context, requestID uuid.UUID) (*AuthRequest, error)
	// SpendCode exchanges an authorization code for pair, once. check
	// sees the code's redirect URI and PKCE challenge before anything is
	// issued; a failed check still spends the code (it was stolen or
	// mangled) but issues nothing. A code presented again revokes its
	// grant, and is answered with a *ReusedError: RFC 6749 section 4.1.2
	// -- a replayed code means someone else has it.
	SpendCode(ctx context.Context, codeHash string, pair *TokenPair, check func(g *Grant, redirectURI, challenge string) bool) (*Grant, error)
	// GrantForRefresh looks up the live grant a refresh token belongs to,
	// without spending it.
	GrantForRefresh(ctx context.Context, refreshHash string) (*Grant, error)
	// SpendRefresh rotates a refresh token out for pair, or answers
	// errRefreshSpent if it is not live (already rotated, expired, or on a
	// revoked grant): nothing is revoked, so the caller can still tell a
	// retry from a copy.
	SpendRefresh(ctx context.Context, refreshHash string, pair *TokenPair) (*Grant, error)
	// RevokeReused ends the grant of a refresh token rotated out earlier
	// and answers with a *ReusedError: presented again, it is a copy.
	// errInvalidGrant for a token never rotated.
	RevokeReused(ctx context.Context, refreshHash string) error
	// ReplayRefresh returns the sealed response SpendRefresh stored when it
	// rotated refreshHash out, if that was within grace, its grant is live
	// and the refresh token it issued is still unspent; the grant is the
	// token's. ErrNotFound otherwise, leaving the caller to treat the
	// token as reused.
	ReplayRefresh(ctx context.Context, refreshHash string, grace time.Duration) (*Grant, []byte, error)
	// LookupAccess resolves a live access token to its grant.
	LookupAccess(ctx context.Context, accessHash string) (*Grant, error)

	CreateDeviceCode(ctx context.Context, d *DeviceCode, deviceHash string) error
	FindDeviceCode(ctx context.Context, userCode string) (*DeviceCode, error)
	ApproveDeviceCode(ctx context.Context, userCode string, g *Grant) error
	DenyDeviceCode(ctx context.Context, userCode string) error
	// PollDeviceCode reports a device code's state to its poller, and on
	// approval spends it for pair and returns the grant.
	PollDeviceCode(ctx context.Context, deviceHash, clientID string, interval time.Duration, pair *TokenPair) (*Grant, error)

	ListGrants(ctx context.Context, tenantID uuid.UUID) ([]Grant, error)
	RevokeGrant(ctx context.Context, tenantID, grantID uuid.UUID) error
	// RevokeAgentGrants revokes every live grant acting as agentID in
	// tenantID. Revoking none is not an error: an agent may have no
	// workspace signed in.
	RevokeAgentGrants(ctx context.Context, tenantID, agentID uuid.UUID) error
	// LiveGrantOfRefresh is the live grant a refresh token was issued under,
	// whether or not the token has since been rotated out or expired: a
	// credential on disk is only ever the newest of its grant's tokens, but a
	// caller replacing it must still reach the grant. errInvalidGrant when
	// there is none.
	LiveGrantOfRefresh(ctx context.Context, refreshHash string) (*Grant, error)

	// CreateReadOnlyClient records a confidential client and the one grant
	// its tokens belong to, together.
	CreateReadOnlyClient(ctx context.Context, c *Client, g *Grant) error
	// ClientGrant is the live grant of a client with one grant (a
	// read-only client's). ErrNotFound once it is revoked.
	ClientGrant(ctx context.Context, clientID string) (*Grant, error)
	// IssueAccess stores an access token alone under grantID: the client
	// credentials grant issues no refresh token, having its secret instead.
	// ErrNotFound when the grant has been revoked, however recently.
	IssueAccess(ctx context.Context, grantID uuid.UUID, t *IssuedToken) error
	// IssueSlotGrant records g and pair together, as a credential for a slot
	// of supervisorID: g.ApprovedBy is set to whoever approved the supervisor's
	// newest live full-access grant, read in the same transaction, so a
	// supervisor whose grants have all ended issues nothing (ErrNotFound) and
	// so does one whose approver is gone (ErrNoApprover). Returns the approver.
	IssueSlotGrant(ctx context.Context, supervisorID uuid.UUID, g *Grant, pair *TokenPair) (approver uuid.UUID, err error)
	// RevokeClient revokes every grant of a client, ending its tokens.
	// ErrNotFound for a client that doesn't exist.
	RevokeClient(ctx context.Context, clientID string) error
}

// IssuedToken is a token about to be stored: its hash and expiry.
type IssuedToken struct {
	Hash      string
	ExpiresAt time.Time
}

// TokenPair is what one exchange issues. The spends below take it rather
// than leaving issuing to a second call: the credential presented and the
// tokens replacing it are committed together, or not at all. Spent in one
// transaction and issued in the next, a failure between the two would
// leave the client with neither -- and its retry would read as a replay
// and revoke the grant.
type TokenPair struct {
	Access, Refresh *IssuedToken
	// Replay is the response these tokens were sent in, sealed under the
	// refresh token they replace. Kept so that token, presented again
	// shortly after, can be answered with the same pair (see ReplayRefresh).
	Replay []byte
}

type PostgresStore struct {
	db *db.DB
}

func NewPostgresStore(database *db.DB) *PostgresStore {
	return &PostgresStore{db: database}
}

var _ Store = (*PostgresStore)(nil)

// unclaimedClientsLock serializes registrations' count-then-insert, so
// concurrent ones can't all see room under the cap. Any constant works; it
// only has to be this one's.
const unclaimedClientsLock = 0x51_0a_c1_1e

func (s *PostgresStore) CreateClient(ctx context.Context, c *Client, maxUnclaimed int) error {
	return s.db.InGlobalTx(ctx, func(q db.Querier) error {
		if _, err := q.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, unclaimedClientsLock); err != nil {
			return fmt.Errorf("oauth: lock client registration: %w", err)
		}
		var unclaimed int
		if err := q.QueryRow(ctx, `
			SELECT count(*) FROM global_oauth_clients c
			WHERE c.id <> $1 AND NOT EXISTS (SELECT 1 FROM global_oauth_grants g WHERE g.client_id = c.id)`,
			WorkspaceClientID).Scan(&unclaimed); err != nil {
			return fmt.Errorf("oauth: count unclaimed clients: %w", err)
		}
		if unclaimed >= maxUnclaimed {
			return ErrTooManyClients
		}
		if _, err := q.Exec(ctx, `
			INSERT INTO global_oauth_clients (id, name, redirect_uris, grant_types) VALUES ($1, $2, $3, $4)`,
			c.ID, c.Name, c.RedirectURIs, c.GrantTypes); err != nil {
			return fmt.Errorf("oauth: create client: %w", err)
		}
		return nil
	})
}

func (s *PostgresStore) GetClient(ctx context.Context, id string) (*Client, error) {
	var c Client
	err := s.db.Global().QueryRow(ctx, `
		SELECT id, name, redirect_uris, grant_types, coalesce(secret_hash, '') FROM global_oauth_clients WHERE id = $1`, id).
		Scan(&c.ID, &c.Name, &c.RedirectURIs, &c.GrantTypes, &c.SecretHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("oauth: get client: %w", err)
	}
	return &c, nil
}

func (s *PostgresStore) CreateAuthRequest(ctx context.Context, r *AuthRequest) error {
	_, err := s.db.Global().Exec(ctx, `
		INSERT INTO global_oauth_auth_requests (id, client_id, redirect_uri, code_challenge, state, resource, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		r.ID, r.ClientID, r.RedirectURI, r.CodeChallenge, r.State, r.Resource, r.ExpiresAt)
	if err != nil {
		return fmt.Errorf("oauth: create auth request: %w", err)
	}
	return nil
}

const authRequestColumns = `r.id, r.client_id, r.redirect_uri, r.code_challenge, r.state, r.resource, r.expires_at`

func scanAuthRequest(row pgx.Row, extra ...any) (*AuthRequest, error) {
	var r AuthRequest
	dest := append([]any{&r.ID, &r.ClientID, &r.RedirectURI, &r.CodeChallenge, &r.State, &r.Resource, &r.ExpiresAt}, extra...)
	if err := row.Scan(dest...); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("oauth: auth request: %w", err)
	}
	return &r, nil
}

func (s *PostgresStore) GetAuthRequest(ctx context.Context, id uuid.UUID) (*AuthRequest, *Client, error) {
	var c Client
	r, err := scanAuthRequest(s.db.Global().QueryRow(ctx, `
		SELECT `+authRequestColumns+`, c.id, c.name, c.redirect_uris, c.grant_types
		FROM global_oauth_auth_requests r JOIN global_oauth_clients c ON c.id = r.client_id
		WHERE r.id = $1 AND r.decided_at IS NULL AND r.expires_at > now()`, id),
		&c.ID, &c.Name, &c.RedirectURIs, &c.GrantTypes)
	if err != nil {
		return nil, nil, err
	}
	return r, &c, nil
}

// decideAuthRequest spends a pending request in one statement, so a
// double-submitted consent form cannot mint two grants.
func decideAuthRequest(ctx context.Context, q db.Querier, id uuid.UUID) (*AuthRequest, error) {
	return scanAuthRequest(q.QueryRow(ctx, `
		UPDATE global_oauth_auth_requests r SET decided_at = now()
		WHERE r.id = $1 AND r.decided_at IS NULL AND r.expires_at > now()
		RETURNING `+authRequestColumns, id))
}

// sqlStateAgentUnavailable is what the grants table's insert guard raises for
// an agent that is disabled or archived (the global_oauth_grants_agent_available trigger).
const sqlStateAgentUnavailable = "23A01"

func insertGrant(ctx context.Context, q db.Querier, g *Grant) error {
	scope := g.Scope
	if scope == "" {
		scope = ScopeAgent
	}
	if err := requireLiveTenant(ctx, q, g.TenantID); err != nil {
		return err
	}
	_, err := q.Exec(ctx, `
		INSERT INTO global_oauth_grants (id, tenant, agent_id, client_id, approved_by, resource, scope)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		g.ID, g.TenantID, g.AgentID, g.ClientID, g.ApprovedBy, g.Resource, scope)
	var pgErr interface{ SQLState() string }
	if errors.As(err, &pgErr) && pgErr.SQLState() == sqlStateAgentUnavailable {
		return ErrAgentUnavailable
	}
	if err != nil {
		return fmt.Errorf("oauth: insert grant: %w", err)
	}
	return nil
}

func (s *PostgresStore) ApproveAuthRequest(ctx context.Context, requestID uuid.UUID, g *Grant, codeHash string, codeExpires time.Time) (*AuthRequest, error) {
	var req *AuthRequest
	err := s.db.InGlobalTx(ctx, func(q db.Querier) error {
		var err error
		if req, err = decideAuthRequest(ctx, q, requestID); err != nil {
			return err
		}
		if req.ClientID != g.ClientID {
			return fmt.Errorf("oauth: approve: grant client %q is not the request's %q", g.ClientID, req.ClientID)
		}
		if err := insertGrant(ctx, q, g); err != nil {
			return err
		}
		_, err = q.Exec(ctx, `
			INSERT INTO global_oauth_codes (code_hash, grant_id, redirect_uri, code_challenge, expires_at)
			VALUES ($1, $2, $3, $4, $5)`,
			codeHash, g.ID, req.RedirectURI, req.CodeChallenge, codeExpires)
		if err != nil {
			return fmt.Errorf("oauth: insert code: %w", err)
		}
		return nil
	})
	return req, err
}

func (s *PostgresStore) DenyAuthRequest(ctx context.Context, requestID uuid.UUID) (*AuthRequest, error) {
	return decideAuthRequest(ctx, s.db.Global(), requestID)
}

const grantColumns = `g.id, g.tenant, g.agent_id, g.client_id, c.name, g.approved_by, g.resource, g.scope, g.created_at, g.last_used_at, gt.archived_at IS NOT NULL`

// grantFrom is the join every grant read goes through; only live grants.
const grantFrom = `global_oauth_grants g JOIN global_oauth_clients c ON c.id = g.client_id JOIN global_tenants gt ON gt.id = g.tenant`

func scanGrant(row pgx.Row, extra ...any) (*Grant, error) {
	var g Grant
	dest := append([]any{&g.ID, &g.TenantID, &g.AgentID, &g.ClientID, &g.ClientName, &g.ApprovedBy, &g.Resource, &g.Scope, &g.CreatedAt, &g.LastUsedAt, &g.TenantArchived}, extra...)
	if err := row.Scan(dest...); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("oauth: grant: %w", err)
	}
	return &g, nil
}

func (s *PostgresStore) revokeGrant(ctx context.Context, grantID uuid.UUID) error {
	if _, err := s.db.Global().Exec(ctx,
		`UPDATE global_oauth_grants SET revoked_at = now() WHERE id = $1 AND revoked_at IS NULL`, grantID); err != nil {
		return fmt.Errorf("oauth: revoke grant: %w", err)
	}
	return nil
}

func (s *PostgresStore) SpendCode(ctx context.Context, codeHash string, pair *TokenPair, check func(g *Grant, redirectURI, challenge string) bool) (*Grant, error) {
	var (
		g      *Grant
		failed bool
	)
	err := s.db.InGlobalTx(ctx, func(q db.Querier) error {
		var redirectURI, challenge string
		var err error
		g, err = scanGrant(q.QueryRow(ctx, `
			WITH spent AS (
				UPDATE global_oauth_codes SET used_at = now()
				WHERE code_hash = $1 AND used_at IS NULL AND expires_at > now()
				RETURNING grant_id, redirect_uri, code_challenge
			)
			SELECT `+grantColumns+`, spent.redirect_uri, spent.code_challenge
			FROM spent JOIN `+grantFrom+` ON g.id = spent.grant_id
			WHERE g.revoked_at IS NULL`, codeHash), &redirectURI, &challenge)
		if err != nil {
			return err
		}
		if !check(g, redirectURI, challenge) {
			// Commit the spend alone: a code that failed its checks
			// must not be tried again.
			failed = true
			return nil
		}
		return insertTokens(ctx, q, g.ID, pair)
	})
	if errors.Is(err, ErrNotFound) {
		var grantID uuid.UUID
		reused := s.db.Global().QueryRow(ctx,
			`SELECT grant_id FROM global_oauth_codes WHERE code_hash = $1 AND used_at IS NOT NULL`, codeHash).Scan(&grantID)
		if reused == nil {
			if err := s.revokeGrant(ctx, grantID); err != nil {
				return nil, err
			}
			return nil, &ReusedError{Kind: "code", GrantID: grantID}
		}
		return nil, errInvalidGrant
	}
	if err != nil {
		return nil, err
	}
	if failed {
		return nil, errInvalidGrant
	}
	return g, nil
}

// requireLiveTenant is the issuing side of the archived-tenant freeze: no
// grant or token is minted for a tenant that has been archived, whichever
// path asks (code, refresh, device, client credentials, slot). It runs in
// the issuing transaction, so a refusal also un-spends the code or refresh
// token that was being exchanged, and unarchiving makes them usable again.
// The tenant row is read FOR SHARE: ArchiveTenant locks it FOR UPDATE, so an
// archive either commits first (and this refuses) or waits for the issue.
// The check that refuses a token already issued is Service.Authenticate.
func requireLiveTenant(ctx context.Context, q db.Querier, tenantID uuid.UUID) error {
	var archived bool
	if err := q.QueryRow(ctx, `SELECT archived_at IS NOT NULL FROM global_tenants WHERE id = $1 FOR SHARE`, tenantID).Scan(&archived); err != nil {
		return fmt.Errorf("oauth: read tenant: %w", err)
	}
	if archived {
		return errTenantArchived
	}
	return nil
}

func requireLiveGrantTenant(ctx context.Context, q db.Querier, grantID uuid.UUID) error {
	var archived bool
	if err := q.QueryRow(ctx, `
		SELECT gt.archived_at IS NOT NULL FROM global_oauth_grants g JOIN global_tenants gt ON gt.id = g.tenant
		WHERE g.id = $1 FOR SHARE OF gt`, grantID).Scan(&archived); err != nil {
		return fmt.Errorf("oauth: read grant's tenant: %w", err)
	}
	if archived {
		return errTenantArchived
	}
	return nil
}

// insertTokens stores pair under grantID and marks the grant used, inside
// the caller's transaction.
func insertTokens(ctx context.Context, q db.Querier, grantID uuid.UUID, pair *TokenPair) error {
	if err := requireLiveGrantTenant(ctx, q, grantID); err != nil {
		return err
	}
	for kind, t := range map[string]*IssuedToken{"access": pair.Access, "refresh": pair.Refresh} {
		if _, err := q.Exec(ctx, `
			INSERT INTO global_oauth_tokens (token_hash, grant_id, kind, expires_at) VALUES ($1, $2, $3, $4)`,
			t.Hash, grantID, kind, t.ExpiresAt); err != nil {
			return fmt.Errorf("oauth: issue %s token: %w", kind, err)
		}
	}
	if _, err := q.Exec(ctx, `UPDATE global_oauth_grants SET last_used_at = now() WHERE id = $1`, grantID); err != nil {
		return fmt.Errorf("oauth: touch grant: %w", err)
	}
	return nil
}

// liveToken is the condition a usable token of a kind meets, with its
// grant not revoked.
const liveToken = `t.kind = $2 AND t.expires_at > now() AND g.revoked_at IS NULL`

func (s *PostgresStore) GrantForRefresh(ctx context.Context, refreshHash string) (*Grant, error) {
	g, err := scanGrant(s.db.Global().QueryRow(ctx, `
		SELECT `+grantColumns+` FROM global_oauth_tokens t JOIN `+grantFrom+` ON g.id = t.grant_id
		WHERE t.token_hash = $1 AND `+liveToken+` AND t.used_at IS NULL`, refreshHash, "refresh"))
	if errors.Is(err, ErrNotFound) {
		return nil, errInvalidGrant
	}
	return g, err
}

func (s *PostgresStore) SpendRefresh(ctx context.Context, refreshHash string, pair *TokenPair) (*Grant, error) {
	var g *Grant
	err := s.db.InGlobalTx(ctx, func(q db.Querier) error {
		var err error
		g, err = scanGrant(q.QueryRow(ctx, `
			WITH spent AS (
				UPDATE global_oauth_tokens t SET used_at = now(), replay_blob = $3, replaced_by = $4
				FROM global_oauth_grants g
				WHERE t.token_hash = $1 AND g.id = t.grant_id AND `+liveToken+` AND t.used_at IS NULL
				RETURNING t.grant_id
			)
			SELECT `+grantColumns+` FROM spent JOIN `+grantFrom+` ON g.id = spent.grant_id`,
			refreshHash, "refresh", pair.Replay, pair.Refresh.Hash))
		if err != nil {
			return err
		}
		// This token's own predecessor can no longer be replayed: the pair it
		// would hand out has moved on.
		if _, err := q.Exec(ctx, `UPDATE global_oauth_tokens SET replay_blob = NULL WHERE replaced_by = $1`, refreshHash); err != nil {
			return fmt.Errorf("oauth: clear replay: %w", err)
		}
		return insertTokens(ctx, q, g.ID, pair)
	})
	if errors.Is(err, ErrNotFound) {
		return nil, errRefreshSpent
	}
	return g, err
}

func (s *PostgresStore) RevokeReused(ctx context.Context, refreshHash string) error {
	// Rotated out already: whoever presents it now holds a copy, and the
	// only safe answer is to end the grant for both of them.
	var grantID uuid.UUID
	err := s.db.Global().QueryRow(ctx, `
		SELECT grant_id FROM global_oauth_tokens
		WHERE token_hash = $1 AND kind = 'refresh' AND used_at IS NOT NULL`, refreshHash).Scan(&grantID)
	if errors.Is(err, pgx.ErrNoRows) {
		return errInvalidGrant
	}
	if err != nil {
		return fmt.Errorf("oauth: look up reused refresh token: %w", err)
	}
	if err := s.revokeGrant(ctx, grantID); err != nil {
		return err
	}
	return &ReusedError{Kind: "refresh", GrantID: grantID}
}

func (s *PostgresStore) ReplayRefresh(ctx context.Context, refreshHash string, grace time.Duration) (*Grant, []byte, error) {
	var blob []byte
	g, err := scanGrant(s.db.Global().QueryRow(ctx, `
		SELECT `+grantColumns+`, t.replay_blob
		FROM global_oauth_tokens t
		JOIN global_oauth_tokens child ON child.token_hash = t.replaced_by
		JOIN `+grantFrom+` ON g.id = t.grant_id
		WHERE t.token_hash = $1 AND t.kind = 'refresh' AND t.replay_blob IS NOT NULL
		  AND t.used_at > now() - make_interval(secs => $2)
		  AND child.used_at IS NULL AND child.expires_at > now() AND g.revoked_at IS NULL`,
		refreshHash, grace.Seconds()), &blob)
	if err != nil {
		return nil, nil, err
	}
	return g, blob, nil
}

func (s *PostgresStore) LookupAccess(ctx context.Context, accessHash string) (*Grant, error) {
	return scanGrant(s.db.Global().QueryRow(ctx, `
		SELECT `+grantColumns+` FROM global_oauth_tokens t JOIN `+grantFrom+` ON g.id = t.grant_id
		WHERE t.token_hash = $1 AND `+liveToken, accessHash, "access"))
}

func (s *PostgresStore) CreateDeviceCode(ctx context.Context, d *DeviceCode, deviceHash string) error {
	_, err := s.db.Global().Exec(ctx, `
		INSERT INTO global_oauth_device_codes (id, device_code_hash, user_code, client_id, resource, expires_at, started_from)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		d.ID, deviceHash, d.UserCode, d.ClientID, d.Resource, d.ExpiresAt, inetOrNull(d.StartedFrom))
	if err != nil {
		return fmt.Errorf("oauth: create device code: %w", err)
	}
	return nil
}

// pendingDevice is a device code nobody has decided and that has not
// expired.
const pendingDevice = `d.grant_id IS NULL AND d.denied_at IS NULL AND d.expires_at > now()`

func (s *PostgresStore) FindDeviceCode(ctx context.Context, userCode string) (*DeviceCode, error) {
	var d DeviceCode
	var startedFrom *string
	err := s.db.Global().QueryRow(ctx, `
		SELECT d.id, d.user_code, d.client_id, c.name, d.resource, d.expires_at, host(d.started_from), d.created_at
		FROM global_oauth_device_codes d JOIN global_oauth_clients c ON c.id = d.client_id
		WHERE d.user_code = $1 AND `+pendingDevice, userCode).
		Scan(&d.ID, &d.UserCode, &d.ClientID, &d.ClientName, &d.Resource, &d.ExpiresAt, &startedFrom, &d.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("oauth: find device code: %w", err)
	}
	if startedFrom != nil {
		d.StartedFrom, _ = netip.ParseAddr(*startedFrom)
	}
	return &d, nil
}

// inetOrNull stores an unknown address as NULL rather than as a value
// Postgres would refuse.
func inetOrNull(a netip.Addr) *string {
	if !a.IsValid() {
		return nil
	}
	s := a.String()
	return &s
}

func (s *PostgresStore) ApproveDeviceCode(ctx context.Context, userCode string, g *Grant) error {
	return s.db.InGlobalTx(ctx, func(q db.Querier) error {
		if err := insertGrant(ctx, q, g); err != nil {
			return err
		}
		tag, err := q.Exec(ctx, `
			UPDATE global_oauth_device_codes d SET grant_id = $2
			WHERE d.user_code = $1 AND d.client_id = $3 AND `+pendingDevice, userCode, g.ID, g.ClientID)
		if err != nil {
			return fmt.Errorf("oauth: approve device code: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return nil
	})
}

func (s *PostgresStore) DenyDeviceCode(ctx context.Context, userCode string) error {
	tag, err := s.db.Global().Exec(ctx, `
		UPDATE global_oauth_device_codes d SET denied_at = now() WHERE d.user_code = $1 AND `+pendingDevice, userCode)
	if err != nil {
		return fmt.Errorf("oauth: deny device code: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PostgresStore) PollDeviceCode(ctx context.Context, deviceHash, clientID string, interval time.Duration, pair *TokenPair) (*Grant, error) {
	var (
		grantID                  *uuid.UUID
		denied, expired, tooSoon bool
	)
	// The poll is recorded whatever it finds, so "too soon" is measured
	// from the previous poll, as RFC 8628 section 3.5 has it.
	err := s.db.Global().QueryRow(ctx, `
		WITH prev AS (
			SELECT id, last_polled_at FROM global_oauth_device_codes
			WHERE device_code_hash = $1 AND client_id = $2
		)
		UPDATE global_oauth_device_codes d SET last_polled_at = now()
		FROM prev WHERE d.id = prev.id
		RETURNING d.grant_id, d.denied_at IS NOT NULL, d.expires_at <= now(),
			prev.last_polled_at IS NOT NULL AND prev.last_polled_at > now() - make_interval(secs => $3)`,
		deviceHash, clientID, interval.Seconds()).Scan(&grantID, &denied, &expired, &tooSoon)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errInvalidGrant
	}
	if err != nil {
		return nil, fmt.Errorf("oauth: poll device code: %w", err)
	}
	switch {
	case expired:
		return nil, errExpiredToken
	case denied:
		return nil, errDenied
	case tooSoon:
		return nil, errSlowDown
	case grantID == nil:
		return nil, errPending
	}
	// Approved: spend it, once, and issue in the same transaction. The
	// spend is the only check that the code has not already been
	// exchanged -- atomic, so polls arriving together cannot both pass it.
	var g *Grant
	err = s.db.InGlobalTx(ctx, func(q db.Querier) error {
		var err error
		g, err = scanGrant(q.QueryRow(ctx, `
			WITH spent AS (
				UPDATE global_oauth_device_codes SET consumed_at = now()
				WHERE device_code_hash = $1 AND consumed_at IS NULL
				RETURNING grant_id
			)
			SELECT `+grantColumns+` FROM spent JOIN `+grantFrom+` ON g.id = spent.grant_id
			WHERE g.revoked_at IS NULL`, deviceHash))
		if err != nil {
			return err
		}
		return insertTokens(ctx, q, g.ID, pair)
	})
	if errors.Is(err, ErrNotFound) {
		return nil, errInvalidGrant
	}
	return g, err
}

func (s *PostgresStore) ListGrants(ctx context.Context, tenantID uuid.UUID) ([]Grant, error) {
	var grants []Grant
	// Agents are tenant-scoped by row level security, so the listing sets
	// the tenant to name the agent each grant acts as.
	err := s.db.InGlobalTx(ctx, func(q db.Querier) error {
		if _, err := q.Exec(ctx, `SELECT set_config('app.current_tenant', $1, true)`, tenantID.String()); err != nil {
			return fmt.Errorf("oauth: scope grants listing: %w", err)
		}
		rows, err := q.Query(ctx, `
			SELECT `+grantColumns+`, COALESCE(a.name, ''), COALESCE(a.kind, '')
			FROM `+grantFrom+` LEFT JOIN agents a ON a.tenant_id = g.tenant AND a.id = g.agent_id
			WHERE g.tenant = $1 AND g.revoked_at IS NULL
			ORDER BY g.created_at DESC, g.id`, tenantID)
		if err != nil {
			return fmt.Errorf("oauth: list grants: %w", err)
		}
		grants, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (Grant, error) {
			var name, kind string
			g, err := scanGrant(row, &name, &kind)
			if err != nil {
				return Grant{}, err
			}
			g.AgentName, g.AgentKind = name, kind
			return *g, nil
		})
		return err
	})
	return grants, err
}

func (s *PostgresStore) RevokeGrant(ctx context.Context, tenantID, grantID uuid.UUID) error {
	tag, err := s.db.Global().Exec(ctx, `
		UPDATE global_oauth_grants SET revoked_at = now()
		WHERE id = $1 AND tenant = $2 AND revoked_at IS NULL`, grantID, tenantID)
	if err != nil {
		return fmt.Errorf("oauth: revoke grant: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PostgresStore) RevokeAgentGrants(ctx context.Context, tenantID, agentID uuid.UUID) error {
	if _, err := s.db.Global().Exec(ctx, `
		UPDATE global_oauth_grants SET revoked_at = now()
		WHERE agent_id = $1 AND tenant = $2 AND revoked_at IS NULL`, agentID, tenantID); err != nil {
		return fmt.Errorf("oauth: revoke agent grants: %w", err)
	}
	return nil
}

func (s *PostgresStore) LiveGrantOfRefresh(ctx context.Context, refreshHash string) (*Grant, error) {
	g, err := scanGrant(s.db.Global().QueryRow(ctx, `
		SELECT `+grantColumns+` FROM global_oauth_tokens t JOIN `+grantFrom+` ON g.id = t.grant_id
		WHERE t.token_hash = $1 AND t.kind = 'refresh' AND g.revoked_at IS NULL`, refreshHash))
	if errors.Is(err, ErrNotFound) {
		return nil, errInvalidGrant
	}
	return g, err
}

func (s *PostgresStore) CreateReadOnlyClient(ctx context.Context, c *Client, g *Grant) error {
	return s.db.InGlobalTx(ctx, func(q db.Querier) error {
		if _, err := q.Exec(ctx, `
			INSERT INTO global_oauth_clients (id, name, grant_types, secret_hash) VALUES ($1, $2, $3, $4)`,
			c.ID, c.Name, c.GrantTypes, c.SecretHash); err != nil {
			return fmt.Errorf("oauth: create read-only client: %w", err)
		}
		return insertGrant(ctx, q, g)
	})
}

func (s *PostgresStore) ClientGrant(ctx context.Context, clientID string) (*Grant, error) {
	return scanGrant(s.db.Global().QueryRow(ctx, `
		SELECT `+grantColumns+` FROM `+grantFrom+`
		WHERE g.client_id = $1 AND g.revoked_at IS NULL
		ORDER BY g.created_at DESC LIMIT 1`, clientID))
}

func (s *PostgresStore) IssueAccess(ctx context.Context, grantID uuid.UUID, t *IssuedToken) error {
	return s.db.InGlobalTx(ctx, func(q db.Querier) error {
		// Only under a live grant, read with a share lock: a revocation
		// committing between the token endpoint's read of the grant and
		// here either finishes first, and this finds nothing, or waits for
		// this -- never a token issued, and answered, under a grant already
		// revoked.
		if err := requireLiveGrantTenant(ctx, q, grantID); err != nil {
			return err
		}
		tag, err := q.Exec(ctx, `
			WITH live AS (
				SELECT id FROM global_oauth_grants WHERE id = $2 AND revoked_at IS NULL FOR SHARE
			)
			INSERT INTO global_oauth_tokens (token_hash, grant_id, kind, expires_at)
			SELECT $1, live.id, 'access', $3 FROM live`,
			t.Hash, grantID, t.ExpiresAt)
		if err != nil {
			return fmt.Errorf("oauth: issue access token: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		if _, err := q.Exec(ctx, `UPDATE global_oauth_grants SET last_used_at = now() WHERE id = $1`, grantID); err != nil {
			return fmt.Errorf("oauth: touch grant: %w", err)
		}
		return nil
	})
}

func (s *PostgresStore) RevokeClient(ctx context.Context, clientID string) error {
	var exists bool
	if err := s.db.Global().QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM global_oauth_clients WHERE id = $1)`, clientID).Scan(&exists); err != nil {
		return fmt.Errorf("oauth: find client: %w", err)
	}
	if !exists {
		return ErrNotFound
	}
	if _, err := s.db.Global().Exec(ctx, `
		UPDATE global_oauth_grants SET revoked_at = now() WHERE client_id = $1 AND revoked_at IS NULL`, clientID); err != nil {
		return fmt.Errorf("oauth: revoke client: %w", err)
	}
	return nil
}

func (s *PostgresStore) IssueSlotGrant(ctx context.Context, supervisorID uuid.UUID, g *Grant, pair *TokenPair) (uuid.UUID, error) {
	var approver uuid.UUID
	err := s.db.InGlobalTx(ctx, func(q db.Querier) error {
		var approvedBy *uuid.UUID
		err := q.QueryRow(ctx, `
			SELECT approved_by FROM global_oauth_grants
			WHERE agent_id = $1 AND tenant = $2 AND revoked_at IS NULL AND scope <> $3
			ORDER BY created_at DESC, id LIMIT 1 FOR SHARE`, supervisorID, g.TenantID, ScopeRead).Scan(&approvedBy)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("oauth: read the supervisor's grant: %w", err)
		}
		if approvedBy == nil {
			return ErrNoApprover
		}
		approver = *approvedBy
		g.ApprovedBy = approvedBy
		if err := insertGrant(ctx, q, g); err != nil {
			return err
		}
		return insertTokens(ctx, q, g.ID, pair)
	})
	return approver, err
}
