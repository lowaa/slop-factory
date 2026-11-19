package githubapp

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// HostWideBinding is the binding of the credential a migrated tenant has: it
// covers every repo on github.com.
const HostWideBinding = "github.com"

var (
	ErrAppNotFound         = errors.New("githubapp: no such App")
	ErrAppAlreadyConnected = errors.New("githubapp: a GitHub App with that app id is already connected")
	ErrCredentialNotFound  = errors.New("githubapp: no such credential")
	ErrBindingTaken        = errors.New("githubapp: another credential already has that binding")
	ErrInstallationTaken   = errors.New("githubapp: that installation is already a credential")
	ErrInvalidBinding      = errors.New("githubapp: a binding is a lowercase host, host/owner or host/owner/repo")
)

// App is a GitHub App the tenant connected. Everything GitHub shares across
// the App's installations lives here, once: the key, and the webhook URL and
// secret every installation's deliveries are signed with. The sealed fields
// are bound to ID.
type App struct {
	ID            uuid.UUID
	AppID         int64
	AppSlug       string
	PrivateKeyEnc []byte
	KeyID         string
	Fingerprint   string
	CreatedBy     *uuid.UUID
	CreatedAt     time.Time
	UpdatedAt     time.Time
	VerifiedAt    *time.Time

	WebhookSecretEnc   []byte
	WebhookSecretKeyID string
	WebhookURL         string
	WebhookAppliedAt   *time.Time
}

// Credential is one installation of an App and the binding that says which
// repos it serves. InstallationID is 0 while it is pending.
type Credential struct {
	ID             uuid.UUID
	AppRef         uuid.UUID
	InstallationID int64
	Binding        string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

func (c *Credential) Pending() bool { return c.InstallationID == 0 }

// AppStore keeps the tenant's Apps and their credentials.
type AppStore interface {
	ListApps(ctx context.Context) ([]*App, error)
	// GetApp returns ErrAppNotFound for no such App.
	GetApp(ctx context.Context, id uuid.UUID) (*App, error)
	// PutApp adds an App, or replaces the key and webhook of the one with
	// app.ID in place. A different App row with the same app_id is refused
	// with ErrAppAlreadyConnected.
	PutApp(ctx context.Context, app *App) (*App, error)
	// DeleteApp removes the App and its credentials, reporting whether there
	// was one.
	DeleteApp(ctx context.Context, id uuid.UUID) (bool, error)

	ListCredentials(ctx context.Context) ([]*Credential, error)
	// GetCredential and GetCredentialByBinding return ErrCredentialNotFound.
	GetCredential(ctx context.Context, id uuid.UUID) (*Credential, error)
	GetCredentialByBinding(ctx context.Context, binding string) (*Credential, error)
	// PutCredential is keyed on the tenant's binding. A credential of the
	// same App with that binding is replaced in place (a pending one gains
	// its installation, keeping its id); a credential of another App, or
	// with another installation, is ErrBindingTaken. An installation that is
	// already another credential is ErrInstallationTaken. An App that is not
	// there is ErrAppNotFound.
	PutCredential(ctx context.Context, c *Credential) (*Credential, error)
	// SetCredentialBinding re-binds a credential.
	SetCredentialBinding(ctx context.Context, id uuid.UUID, binding string) (*Credential, error)
	// DeleteCredential removes one credential and leaves its App.
	DeleteCredential(ctx context.Context, id uuid.UUID) (bool, error)
}

var _ AppStore = (*PostgresStore)(nil)

const appColumns = `id, app_id, coalesce(app_slug, ''), private_key_enc, key_id, key_fingerprint, created_by,
	created_at, updated_at, verified_at, webhook_secret_enc, coalesce(webhook_secret_key_id, ''),
	coalesce(webhook_url, ''), webhook_applied_at`

func scanApp(row pgx.Row) (*App, error) {
	var a App
	err := row.Scan(&a.ID, &a.AppID, &a.AppSlug, &a.PrivateKeyEnc, &a.KeyID, &a.Fingerprint, &a.CreatedBy,
		&a.CreatedAt, &a.UpdatedAt, &a.VerifiedAt, &a.WebhookSecretEnc, &a.WebhookSecretKeyID,
		&a.WebhookURL, &a.WebhookAppliedAt)
	return &a, err
}

const credentialColumns = `id, app_ref, coalesce(installation_id, 0), binding, created_at, updated_at` // #nosec G101 -- column names, not a credential

func scanCredential(row pgx.Row) (*Credential, error) {
	var c Credential
	err := row.Scan(&c.ID, &c.AppRef, &c.InstallationID, &c.Binding, &c.CreatedAt, &c.UpdatedAt)
	return &c, err
}

// mapStoreError turns the constraints that carry a rule into its error.
func mapStoreError(err error) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return err
	}
	switch pgErr.ConstraintName {
	case "codehost_apps_tenant_id_app_id_key":
		return ErrAppAlreadyConnected
	case "codehost_credentials_tenant_id_binding_key":
		return ErrBindingTaken
	case "codehost_credentials_tenant_id_installation_id_key":
		return ErrInstallationTaken
	case "codehost_credentials_binding_check":
		return ErrInvalidBinding
	case "codehost_credentials_tenant_id_app_ref_fkey":
		return ErrAppNotFound
	}
	return err
}

func (s *PostgresStore) ListApps(ctx context.Context) ([]*App, error) {
	rows, err := s.q(ctx).Query(ctx, `SELECT `+appColumns+` FROM codehost_apps ORDER BY created_at, id`)
	if err != nil {
		return nil, fmt.Errorf("githubapp: list apps: %w", err)
	}
	defer rows.Close()
	var out []*App
	for rows.Next() {
		a, err := scanApp(rows)
		if err != nil {
			return nil, fmt.Errorf("githubapp: scan app: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *PostgresStore) GetApp(ctx context.Context, id uuid.UUID) (*App, error) {
	a, err := scanApp(s.q(ctx).QueryRow(ctx, `SELECT `+appColumns+` FROM codehost_apps WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrAppNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("githubapp: get app: %w", err)
	}
	return a, nil
}

func (s *PostgresStore) PutApp(ctx context.Context, app *App) (*App, error) {
	a, err := scanApp(s.q(ctx).QueryRow(ctx, `
		INSERT INTO codehost_apps (id, provider, app_id, app_slug, private_key_enc, key_id, key_fingerprint, created_by,
		                           webhook_secret_enc, webhook_secret_key_id, webhook_url, webhook_applied_at)
		VALUES ($1, 'github_app', $2, nullif($3, ''), $4, $5, $6, $7, $8, $9, nullif($10, ''), $11)
		ON CONFLICT (tenant_id, id) DO UPDATE
		   SET app_id = EXCLUDED.app_id, app_slug = EXCLUDED.app_slug,
		       private_key_enc = EXCLUDED.private_key_enc, key_id = EXCLUDED.key_id,
		       key_fingerprint = EXCLUDED.key_fingerprint,
		       webhook_secret_enc = EXCLUDED.webhook_secret_enc, webhook_secret_key_id = EXCLUDED.webhook_secret_key_id,
		       webhook_url = EXCLUDED.webhook_url, webhook_applied_at = EXCLUDED.webhook_applied_at,
		       updated_at = now(), verified_at = NULL
		RETURNING `+appColumns,
		app.ID, app.AppID, app.AppSlug, app.PrivateKeyEnc, app.KeyID, app.Fingerprint, app.CreatedBy,
		app.WebhookSecretEnc, nullableText(app.WebhookSecretEnc, app.WebhookSecretKeyID), app.WebhookURL, app.WebhookAppliedAt))
	if err != nil {
		return nil, fmt.Errorf("githubapp: put app: %w", mapStoreError(err))
	}
	return a, nil
}

// nullableText is the key id that goes with a sealed secret: null when there
// is none, as the table's check requires.
func nullableText(blob []byte, text string) *string {
	if blob == nil {
		return nil
	}
	return &text
}

func (s *PostgresStore) DeleteApp(ctx context.Context, id uuid.UUID) (bool, error) {
	tag, err := s.q(ctx).Exec(ctx, `DELETE FROM codehost_apps WHERE id = $1`, id)
	if err != nil {
		return false, fmt.Errorf("githubapp: delete app: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

func (s *PostgresStore) ListCredentials(ctx context.Context) ([]*Credential, error) {
	rows, err := s.q(ctx).Query(ctx, `SELECT `+credentialColumns+` FROM codehost_credentials ORDER BY created_at, id`)
	if err != nil {
		return nil, fmt.Errorf("githubapp: list credentials: %w", err)
	}
	defer rows.Close()
	var out []*Credential
	for rows.Next() {
		c, err := scanCredential(rows)
		if err != nil {
			return nil, fmt.Errorf("githubapp: scan credential: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *PostgresStore) getCredential(ctx context.Context, where string, arg any) (*Credential, error) {
	c, err := scanCredential(s.q(ctx).QueryRow(ctx, `SELECT `+credentialColumns+` FROM codehost_credentials WHERE `+where, arg))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrCredentialNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("githubapp: get credential: %w", err)
	}
	return c, nil
}

func (s *PostgresStore) GetCredential(ctx context.Context, id uuid.UUID) (*Credential, error) {
	return s.getCredential(ctx, `id = $1`, id)
}

func (s *PostgresStore) GetCredentialByBinding(ctx context.Context, binding string) (*Credential, error) {
	return s.getCredential(ctx, `binding = $1`, binding)
}

func (s *PostgresStore) PutCredential(ctx context.Context, c *Credential) (*Credential, error) {
	out, err := scanCredential(s.q(ctx).QueryRow(ctx, `
		INSERT INTO codehost_credentials (id, app_ref, installation_id, binding)
		VALUES ($1, $2, nullif($3::bigint, 0), $4)
		ON CONFLICT (tenant_id, binding) DO UPDATE
		   SET installation_id = EXCLUDED.installation_id, updated_at = now()
		 WHERE codehost_credentials.app_ref = EXCLUDED.app_ref
		   AND (codehost_credentials.installation_id IS NULL
		        OR codehost_credentials.installation_id = EXCLUDED.installation_id)
		RETURNING `+credentialColumns, c.ID, c.AppRef, c.InstallationID, c.Binding))
	if errors.Is(err, pgx.ErrNoRows) {
		// The conflict was another credential's binding, which the update's
		// condition would not take over.
		return nil, ErrBindingTaken
	}
	if err != nil {
		return nil, fmt.Errorf("githubapp: put credential: %w", mapStoreError(err))
	}
	return out, nil
}

func (s *PostgresStore) SetCredentialBinding(ctx context.Context, id uuid.UUID, binding string) (*Credential, error) {
	c, err := scanCredential(s.q(ctx).QueryRow(ctx,
		`UPDATE codehost_credentials SET binding = $2, updated_at = now() WHERE id = $1 RETURNING `+credentialColumns, id, binding))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrCredentialNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("githubapp: set binding: %w", mapStoreError(err))
	}
	return c, nil
}

func (s *PostgresStore) DeleteCredential(ctx context.Context, id uuid.UUID) (bool, error) {
	tag, err := s.q(ctx).Exec(ctx, `DELETE FROM codehost_credentials WHERE id = $1`, id)
	if err != nil {
		return false, fmt.Errorf("githubapp: delete credential: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}
