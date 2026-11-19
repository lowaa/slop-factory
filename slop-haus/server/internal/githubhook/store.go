package githubhook

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/lowaa/slop-factory/go-auth/login"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/hookkit"
)

// Store is the persistence Service needs.
type Store interface {
	// AppInstallations are the tenants with a GitHub App installation id and
	// a webhook secret stored, each with the App whose secret signs the
	// installation's deliveries, read before any tenant is known. More than
	// one is possible: nothing stops two tenants connecting the same
	// installation, and whose secret signed the delivery tells them apart.
	AppInstallations(ctx context.Context, installationID int64) ([]AppInstallation, error)
	// ClaimEvent takes the right to apply the review named by key
	// in ctx's tenant for delivery deliveryID, reporting false if another
	// delivery holds or has used it.
	ClaimEvent(ctx context.Context, key, deliveryID string) (bool, error)
	// FinishEvent ends deliveryID's claim: kept if applied, given up
	// otherwise. A claim another delivery has since taken over is left
	// alone.
	FinishEvent(ctx context.Context, key, deliveryID string, applied bool) error
	// MemberForGitHubUser is the member of ctx's tenant whose account has
	// GitHub user githubID linked to it, or nil for none.
	MemberForGitHubUser(ctx context.Context, githubID int64) (*uuid.UUID, error)
	// BeginDelivery records a delivery as processing, reporting false if it
	// was already recorded -- unless its handling failed, in which case it
	// is taken up again.
	BeginDelivery(ctx context.Context, d *Delivery) (bool, error)
	// FinishDelivery records what became of a delivery.
	FinishDelivery(ctx context.Context, d *Delivery) error
	// ListDeliveries is ctx's tenant's most recent deliveries, newest
	// first.
	ListDeliveries(ctx context.Context, limit int) ([]Delivery, error)
}

// AppInstallation is one tenant's side of an installation: the App it belongs
// to, whose sealed webhook secret this is. The secret was sealed for AppID, one
// per App however many installations the App has.
type AppInstallation struct {
	Tenant       uuid.UUID
	CredentialID uuid.UUID
	AppID        uuid.UUID
	SecretEnc    []byte
}

type PostgresStore struct {
	db *db.DB
}

func NewPostgresStore(database *db.DB) *PostgresStore {
	return &PostgresStore{db: database}
}

var _ Store = (*PostgresStore)(nil)

// appInstallationsLimit bounds the tenants one installation id can name,
// which is one in practice (an installation belongs to one App, and an App
// has one webhook secret): the lookup runs before the signature is checked,
// and each candidate costs a decryption.
const appInstallationsLimit = 20

func (s *PostgresStore) AppInstallations(ctx context.Context, installationID int64) ([]AppInstallation, error) {
	rows, err := s.db.Global().Query(ctx, `
		SELECT tenant, credential_id, app_ref, webhook_secret_enc FROM global_github_app_installations
		 WHERE installation_id = $1
		 ORDER BY tenant
		 LIMIT $2`, installationID, appInstallationsLimit)
	if err != nil {
		return nil, fmt.Errorf("resolve github app installation: %w", err)
	}
	defer rows.Close()
	var out []AppInstallation
	for rows.Next() {
		var a AppInstallation
		if err := rows.Scan(&a.Tenant, &a.CredentialID, &a.AppID, &a.SecretEnc); err != nil {
			return nil, fmt.Errorf("scan github app installation: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ClaimEvent's conflict clause takes over a claim that is stale (its
// delivery died mid-way) or the same delivery's own (retrying after an
// error), and nothing else: an applied key stays applied.
func (s *PostgresStore) ClaimEvent(ctx context.Context, key, deliveryID string) (bool, error) {
	var claimed bool
	err := s.db.QueryRow(ctx, `
		INSERT INTO github_event_keys (event_key, delivery_id)
		VALUES ($1, $2)
		ON CONFLICT (tenant_id, event_key) DO UPDATE
		   SET delivery_id = EXCLUDED.delivery_id, claimed_at = now()
		 WHERE github_event_keys.state = 'claimed'
		   AND (github_event_keys.delivery_id = EXCLUDED.delivery_id
		        OR github_event_keys.claimed_at < now() - $3::interval)
		RETURNING true`, key, deliveryID, hookkit.AbandonedAfter.String()).Scan(&claimed)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("claim github event: %w", err)
	}
	return claimed, nil
}

func (s *PostgresStore) FinishEvent(ctx context.Context, key, deliveryID string, applied bool) error {
	var err error
	if applied {
		_, err = s.db.Exec(ctx, `UPDATE github_event_keys SET state = 'applied' WHERE event_key = $1 AND delivery_id = $2`, key, deliveryID)
	} else {
		_, err = s.db.Exec(ctx, `DELETE FROM github_event_keys WHERE event_key = $1 AND delivery_id = $2 AND state = 'claimed'`, key, deliveryID)
	}
	if err != nil {
		return fmt.Errorf("finish github event: %w", err)
	}
	return nil
}

// MemberForGitHubUser matches on GitHub's numeric user id, the subject of a
// "github" identity: a login can be renamed, and then registered by someone
// else. The "github" provider name is reserved for GitHub itself (see
// config.ValidateOIDCProviderName), so no other issuer's subject can pose
// as one. An account that is not active -- never activated -- does not
// count.
func (s *PostgresStore) MemberForGitHubUser(ctx context.Context, githubID int64) (*uuid.UUID, error) {
	var userID uuid.UUID
	err := s.db.QueryRow(ctx, `
		SELECT i.user_id
		  FROM global_user_identities i
		  JOIN global_tenant_memberships m ON m.user_id = i.user_id AND m.tenant = current_tenant()
		  JOIN global_users u ON u.id = i.user_id AND u.is_active
		 WHERE i.provider = $1 AND i.subject = $2`,
		login.GitHubProviderName, strconv.FormatInt(githubID, 10)).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("find github reviewer: %w", err)
	}
	return &userID, nil
}

// BeginDelivery's upsert is the dedupe: a second copy of a delivery racing
// the first waits on the first's row, then sees it processing and backs
// off. Only a delivery recorded as failed, or abandoned, is handed out
// again.
func (s *PostgresStore) BeginDelivery(ctx context.Context, d *Delivery) (bool, error) {
	var begun bool
	err := s.db.QueryRow(ctx, `
		INSERT INTO github_deliveries (delivery_id, installation_id, event, action, outcome)
		VALUES ($1, $5, $2, $3, 'processing')
		ON CONFLICT (tenant_id, delivery_id) DO UPDATE
		   SET outcome = 'processing', detail = '', ticket_id = NULL, attempt_number = NULL,
		       installation_id = EXCLUDED.installation_id, event = EXCLUDED.event,
		       action = EXCLUDED.action, received_at = now()
		 WHERE github_deliveries.outcome = 'error'
		    OR (github_deliveries.outcome = 'processing' AND github_deliveries.received_at < now() - $4::interval)
		RETURNING true`, d.ID, d.Event, d.Action, hookkit.AbandonedAfter.String(), d.InstallationID).Scan(&begun)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("record github delivery: %w", err)
	}
	return begun, nil
}

func (s *PostgresStore) FinishDelivery(ctx context.Context, d *Delivery) error {
	_, err := s.db.Exec(ctx, `
		UPDATE github_deliveries SET outcome = $2, detail = $3, ticket_id = $4, attempt_number = $5
		 WHERE delivery_id = $1`, d.ID, string(d.Outcome), d.Detail, d.TicketID, d.AttemptNumber)
	if err != nil {
		return fmt.Errorf("record github delivery outcome: %w", err)
	}
	return nil
}

func (s *PostgresStore) ListDeliveries(ctx context.Context, limit int) ([]Delivery, error) {
	rows, err := s.db.Query(ctx, `
		SELECT delivery_id, installation_id, event, action, outcome, detail, ticket_id, attempt_number, received_at
		  FROM github_deliveries
		 WHERE installation_id IS NOT NULL
		 ORDER BY received_at DESC, delivery_id
		 LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("list github deliveries: %w", err)
	}
	defer rows.Close()
	out := []Delivery{}
	for rows.Next() {
		var d Delivery
		var outcome string
		if err := rows.Scan(&d.ID, &d.InstallationID, &d.Event, &d.Action, &outcome, &d.Detail, &d.TicketID, &d.AttemptNumber, &d.ReceivedAt); err != nil {
			return nil, fmt.Errorf("scan github delivery: %w", err)
		}
		d.Outcome = Outcome(outcome)
		out = append(out, d)
	}
	return out, rows.Err()
}

// LatestAppDelivery is ctx's tenant's newest delivery that came through the
// GitHub App, restricted to the given events when there are any; nil if none.
func (s *PostgresStore) LatestAppDelivery(ctx context.Context, events []string) (*Delivery, error) {
	var d Delivery
	var outcome string
	err := s.db.QueryRow(ctx, `
		SELECT delivery_id, event, action, outcome, received_at
		  FROM github_deliveries
		 WHERE installation_id IS NOT NULL
		   AND (coalesce(cardinality($1::text[]), 0) = 0 OR event = ANY($1))
		 ORDER BY received_at DESC, delivery_id
		 LIMIT 1`, events).Scan(&d.ID, &d.Event, &d.Action, &outcome, &d.ReceivedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("latest github app delivery: %w", err)
	}
	d.Outcome = Outcome(outcome)
	return &d, nil
}
