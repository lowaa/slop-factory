package githubapp

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/codehost"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/tenantstate"
)

// Record is a tenant's stored credential. PrivateKeyEnc is the sealed key:
// the one field that is secret, and the only one nothing but Service reads.
type Record struct {
	ID    uuid.UUID
	AppID int64
	// InstallationID is 0 while the App is created but not installed (or the
	// install awaits an org owner's approval): the credential is pending.
	InstallationID int64
	// AppSlug names the App on GitHub; empty for one connected by hand.
	AppSlug       string
	PrivateKeyEnc []byte
	KeyID         string
	Fingerprint   string
	CreatedBy     *uuid.UUID
	CreatedAt     time.Time
	UpdatedAt     time.Time
	VerifiedAt    *time.Time

	// WebhookSecretEnc is the sealed secret slop-haus set on the App's
	// webhook; nil until one was applied. Like PrivateKeyEnc, nothing but
	// the webhook configurer reads it.
	WebhookSecretEnc   []byte
	WebhookSecretKeyID string
	// WebhookURL is the URL slop-haus last set on GitHub, empty if never.
	WebhookURL       string
	WebhookAppliedAt *time.Time
}

// Pending is a credential whose App has no installation yet.
func (r *Record) Pending() bool { return r.InstallationID == 0 }

// Store keeps the tenant's credential, one row. Every call runs in the
// context's tenant, which row level security enforces.
type Store interface {
	SetupStates

	// Get returns an error wrapping codehost.ErrNotConnected for no row.
	Get(ctx context.Context) (*Record, error)
	// Put adds or replaces the tenant's credential, returning it as stored.
	Put(ctx context.Context, rec *Record) (*Record, error)
	// SetInstallation fills in a pending credential's installation; it
	// returns ErrCredentialChanged if the row is no longer that pending one.
	SetInstallation(ctx context.Context, id uuid.UUID, installationID int64) (*Record, error)
	// Reseal swaps a row's sealed key, only if the row is still the one the
	// blob was made for (a replace in between changes its id).
	Reseal(ctx context.Context, id uuid.UUID, blob []byte, keyID string) (bool, error)
	// WithWebhookLock runs fn holding the tenant's webhook lock, which
	// serializes every webhook change across servers: a change is made on
	// GitHub and then stored, and two interleaved would leave GitHub with
	// one secret and the store with the other. It holds a transaction open
	// for fn's duration, which is a few GitHub calls; the store reads and
	// writes fn makes must use the ctx it is given, which carries that
	// transaction.
	WithWebhookLock(ctx context.Context, fn func(ctx context.Context) error) error
	// SetWebhook records a webhook configuration GitHub accepted, only if
	// the row is still credential id.
	SetWebhook(ctx context.Context, id uuid.UUID, secretEnc []byte, keyID, url string) (bool, error)
	// ResealWebhook swaps the sealed webhook secret, only if the row is
	// still credential id.
	ResealWebhook(ctx context.Context, id uuid.UUID, blob []byte, keyID string) (bool, error)
	// InstallationHeldByOther reports whether another tenant has a webhook
	// secret stored for the installation this tenant's credential names.
	// GitHub keeps one secret per App, so a second tenant setting its own
	// would silence the first.
	InstallationHeldByOther(ctx context.Context) (bool, error)
	// Tenants lists every tenant, for the work that runs with no request to
	// take one from.
	Tenants(ctx context.Context) ([]uuid.UUID, error)
	// MarkVerified records a successful check against GitHub, and reports
	// whether credential id still exists to record it on.
	MarkVerified(ctx context.Context, id uuid.UUID) (bool, error)
	// Delete removes the credential, reporting whether there was one.
	Delete(ctx context.Context) (bool, error)
}

type PostgresStore struct{ db *db.DB }

type lockedTxKey struct{}

// q is the transaction WithWebhookLock holds open when ctx came from it,
// else the pool. Reading through the pool from inside the lock would need a
// second connection while the first is held, and with as many concurrent
// lock holders as the pool has connections none can get one: a deadlock.
func (s *PostgresStore) q(ctx context.Context) db.Querier {
	if q, ok := ctx.Value(lockedTxKey{}).(db.Querier); ok {
		return q
	}
	return s.db
}

func NewPostgresStore(database *db.DB) *PostgresStore { return &PostgresStore{db: database} }

var _ Store = (*PostgresStore)(nil)

const recordColumns = `id, app_id, coalesce(installation_id, 0), coalesce(app_slug, ''), private_key_enc, key_id,
	key_fingerprint, created_by, created_at, updated_at, verified_at,
	webhook_secret_enc, coalesce(webhook_secret_key_id, ''), coalesce(webhook_url, ''), webhook_applied_at`

func scanRecord(row pgx.Row) (*Record, error) {
	var r Record
	err := row.Scan(&r.ID, &r.AppID, &r.InstallationID, &r.AppSlug, &r.PrivateKeyEnc, &r.KeyID, &r.Fingerprint,
		&r.CreatedBy, &r.CreatedAt, &r.UpdatedAt, &r.VerifiedAt,
		&r.WebhookSecretEnc, &r.WebhookSecretKeyID, &r.WebhookURL, &r.WebhookAppliedAt)
	return &r, err
}

func (s *PostgresStore) Get(ctx context.Context) (*Record, error) {
	r, err := scanRecord(s.q(ctx).QueryRow(ctx, `SELECT `+recordColumns+` FROM codehost_credentials`))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, codehost.ErrNotConnected
	}
	if err != nil {
		return nil, fmt.Errorf("githubapp: get credential: %w", err)
	}
	return r, nil
}

// Put replaces the row whole, id included: the sealed key is bound to the
// id, so a replace is a new row in everything but its place.
func (s *PostgresStore) Put(ctx context.Context, rec *Record) (*Record, error) {
	r, err := scanRecord(s.q(ctx).QueryRow(ctx, `
		INSERT INTO codehost_credentials
		    (id, provider, app_id, installation_id, app_slug, private_key_enc, key_id, key_fingerprint, created_by)
		VALUES ($1, 'github_app', $2, nullif($3::bigint, 0), nullif($4, ''), $5, $6, $7, $8)
		ON CONFLICT (tenant_id) DO UPDATE
		   SET id = EXCLUDED.id, app_id = EXCLUDED.app_id, installation_id = EXCLUDED.installation_id,
		       app_slug = EXCLUDED.app_slug,
		       private_key_enc = EXCLUDED.private_key_enc, key_id = EXCLUDED.key_id,
		       key_fingerprint = EXCLUDED.key_fingerprint, created_by = EXCLUDED.created_by,
		       updated_at = now(), verified_at = NULL,
		       webhook_secret_enc = NULL, webhook_secret_key_id = NULL, webhook_url = NULL, webhook_applied_at = NULL
		RETURNING `+recordColumns,
		rec.ID, rec.AppID, rec.InstallationID, rec.AppSlug, rec.PrivateKeyEnc, rec.KeyID, rec.Fingerprint, rec.CreatedBy))
	if err != nil {
		return nil, fmt.Errorf("githubapp: put credential: %w", err)
	}
	return r, nil
}

// SetInstallation records the installation a pending credential waits for,
// only if the row is still the credential id (a replace in between changes
// its id) and still pending. It bumps updated_at, which versions the
// credential's meaning, and clears verified_at.
func (s *PostgresStore) SetInstallation(ctx context.Context, id uuid.UUID, installationID int64) (*Record, error) {
	r, err := scanRecord(s.q(ctx).QueryRow(ctx, `
		UPDATE codehost_credentials SET installation_id = $2, updated_at = now(), verified_at = NULL
		 WHERE id = $1 AND installation_id IS NULL
		RETURNING `+recordColumns, id, installationID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrCredentialChanged
	}
	if err != nil {
		return nil, fmt.Errorf("githubapp: set installation: %w", err)
	}
	return r, nil
}

// Reseal leaves updated_at alone: it versions the credential's meaning,
// which a re-encryption does not change, and cached tokens are keyed by it.
func (s *PostgresStore) Reseal(ctx context.Context, id uuid.UUID, blob []byte, keyID string) (bool, error) {
	tag, err := s.q(ctx).Exec(ctx,
		`UPDATE codehost_credentials SET private_key_enc = $2, key_id = $3 WHERE id = $1`, id, blob, keyID)
	if err != nil {
		return false, fmt.Errorf("githubapp: reseal credential: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

func (s *PostgresStore) WithWebhookLock(ctx context.Context, fn func(ctx context.Context) error) error {
	return s.db.InTx(ctx, func(q db.Querier) error {
		if _, err := q.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended(current_tenant()::text || '/github-app-webhook', 0))`); err != nil {
			return fmt.Errorf("githubapp: lock webhook: %w", err)
		}
		// Held after the tenant's own, in that order only, so two tenants
		// sharing an installation queue here without deadlocking. Row level
		// security shows this tenant's credential alone, so a tenant takes at
		// most its own installation's key, never two. The first to finish has
		// stored its secret by the time the second looks.
		if _, err := q.Exec(ctx, `
			SELECT pg_advisory_xact_lock(hashtextextended('github-app-installation/' || installation_id::text, 0))
			  FROM codehost_credentials WHERE installation_id IS NOT NULL`); err != nil {
			return fmt.Errorf("githubapp: lock installation: %w", err)
		}
		return fn(context.WithValue(ctx, lockedTxKey{}, q))
	})
}

func (s *PostgresStore) InstallationHeldByOther(ctx context.Context) (bool, error) {
	var held bool
	err := s.q(ctx).QueryRow(ctx, `
		SELECT EXISTS (
		    SELECT 1 FROM global_github_app_installations g
		     WHERE g.tenant <> current_tenant()
		       AND g.installation_id = (SELECT installation_id FROM codehost_credentials))`).Scan(&held)
	if err != nil {
		return false, fmt.Errorf("githubapp: check installation: %w", err)
	}
	return held, nil
}

func (s *PostgresStore) Tenants(ctx context.Context) ([]uuid.UUID, error) {
	return tenantstate.ActiveIDs(ctx, s.db.Global())
}

func (s *PostgresStore) SetWebhook(ctx context.Context, id uuid.UUID, secretEnc []byte, keyID, url string) (bool, error) {
	tag, err := s.q(ctx).Exec(ctx, `
		UPDATE codehost_credentials
		   SET webhook_secret_enc = $2, webhook_secret_key_id = $3, webhook_url = $4, webhook_applied_at = now()
		 WHERE id = $1`, id, secretEnc, keyID, url)
	if err != nil {
		return false, fmt.Errorf("githubapp: set webhook: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

func (s *PostgresStore) ResealWebhook(ctx context.Context, id uuid.UUID, blob []byte, keyID string) (bool, error) {
	tag, err := s.q(ctx).Exec(ctx,
		`UPDATE codehost_credentials SET webhook_secret_enc = $2, webhook_secret_key_id = $3
		  WHERE id = $1 AND webhook_secret_enc IS NOT NULL`, id, blob, keyID)
	if err != nil {
		return false, fmt.Errorf("githubapp: reseal webhook secret: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

func (s *PostgresStore) MarkVerified(ctx context.Context, id uuid.UUID) (bool, error) {
	tag, err := s.q(ctx).Exec(ctx, `UPDATE codehost_credentials SET verified_at = now() WHERE id = $1`, id)
	if err != nil {
		return false, fmt.Errorf("githubapp: mark verified: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

func (s *PostgresStore) Delete(ctx context.Context) (bool, error) {
	tag, err := s.q(ctx).Exec(ctx, `DELETE FROM codehost_credentials`)
	if err != nil {
		return false, fmt.Errorf("githubapp: delete credential: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}
