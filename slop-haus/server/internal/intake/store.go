package intake

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/lowaa/slop-factory/go-db/pgerr"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/hookkit"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket/model"
)

// Credential is what authenticating a delivery needs of its source: read
// before any tenant is known.
type Credential struct {
	Tenant     uuid.UUID
	Kind       AuthKind
	TokenHash  []byte
	Generation int
}

// NewCredential is a source's credential as it is first stored, or
// rotated to: a token's hash, or for HMAC nothing but the generation.
type NewCredential struct {
	Kind      AuthKind
	TokenHash []byte
}

// Store is the persistence Service needs.
type Store interface {
	// CreateSource records a source and its credential for ctx's tenant.
	CreateSource(ctx context.Context, id uuid.UUID, spec *Spec, cred *NewCredential, createdBy uuid.UUID) (*Source, error)
	// ListSources is ctx's tenant's sources, revoked ones included, newest
	// first.
	ListSources(ctx context.Context) ([]Source, error)
	// LiveSource is one of ctx's tenant's sources that is not revoked, or
	// ErrNotFound.
	LiveSource(ctx context.Context, id uuid.UUID) (*Source, error)
	// Rotate replaces a live source's credential, answering the new
	// generation, or ErrNotFound.
	Rotate(ctx context.Context, id uuid.UUID, cred *NewCredential) (int, error)
	// Revoke revokes one of ctx's tenant's live sources for good, or
	// answers ErrNotFound.
	Revoke(ctx context.Context, id uuid.UUID) error
	// CredentialOf is a live source's credential, read before any tenant is
	// known; ErrNotFound for one unknown or revoked.
	CredentialOf(ctx context.Context, id uuid.UUID) (*Credential, error)
	// BeginDelivery records a delivery as processing, reporting false if it
	// was already recorded -- unless its handling failed, in which case it
	// is taken up again.
	BeginDelivery(ctx context.Context, d *Delivery) (bool, error)
	// FinishDelivery records what became of a delivery.
	FinishDelivery(ctx context.Context, d *Delivery) error
	// RecordRefusal records a delivery that never began: rate limited.
	RecordRefusal(ctx context.Context, d *Delivery) error
	// ListDeliveries is ctx's tenant's most recent deliveries, newest first.
	ListDeliveries(ctx context.Context, limit int) ([]Delivery, error)
}

type PostgresStore struct {
	db *db.DB
}

func NewPostgresStore(database *db.DB) *PostgresStore {
	return &PostgresStore{db: database}
}

var _ Store = (*PostgresStore)(nil)

const sourceColumns = `id, name, adapter, trust, auth_kind, default_status, default_priority, max_priority,
	role_names, role_match, paths, reopen_policy, resolve_closes, created_by, created_by_deleted, created_at, revoked_at, last_delivery_at`

func scanSource(row pgx.Row) (*Source, error) {
	var s Source
	var trust, auth, status, prio, maxPrio, match, reopen string
	if err := row.Scan(&s.ID, &s.Name, &s.Adapter, &trust, &auth, &status, &prio, &maxPrio,
		&s.RoleNames, &match, &s.Paths, &reopen, &s.ResolveCloses, &s.CreatedBy, &s.CreatedByDeleted, &s.CreatedAt, &s.RevokedAt, &s.LastDeliveryAt); err != nil {
		return nil, err
	}
	s.Trust, s.AuthKind = Trust(trust), AuthKind(auth)
	s.DefaultStatus, s.DefaultPriority, s.MaxPriority = ticket.Status(status), model.Priority(prio), model.Priority(maxPrio)
	s.RoleMatch, s.ReopenPolicy = model.RoleMatch(match), ReopenPolicy(reopen)
	return &s, nil
}

func (s *PostgresStore) CreateSource(ctx context.Context, id uuid.UUID, spec *Spec, cred *NewCredential, createdBy uuid.UUID) (*Source, error) {
	var src *Source
	err := s.db.InTx(ctx, func(q db.Querier) error {
		// The credential first: it is the global row that says which
		// tenant the id is, and fails on nothing the source row would not.
		if _, err := q.Exec(ctx, `
			INSERT INTO global_intake_credentials (source_id, tenant, kind, token_hash)
			VALUES ($1, current_tenant(), $2, $3)`, id, string(cred.Kind), cred.TokenHash); err != nil {
			return fmt.Errorf("create intake credential: %w", err)
		}
		var err error
		src, err = scanSource(q.QueryRow(ctx, `
			INSERT INTO intake_sources (id, name, adapter, trust, auth_kind, default_status, default_priority,
			                            max_priority, role_names, role_match, paths, reopen_policy, resolve_closes, created_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
			RETURNING `+sourceColumns,
			id, spec.Name, spec.Adapter, string(spec.Trust), string(spec.AuthKind), string(spec.DefaultStatus),
			string(spec.DefaultPriority), string(spec.MaxPriority), nonNil(spec.RoleNames), string(spec.RoleMatch),
			nonNil(spec.Paths), string(spec.ReopenPolicy), spec.ResolveCloses, createdBy))
		if err != nil {
			return fmt.Errorf("create intake source: %w", err)
		}
		return nil
	})
	if err != nil {
		if pgerr.IsUniqueViolation(err) {
			return nil, fmt.Errorf("%w: a live source is already named %q", ErrInvalidSource, spec.Name)
		}
		return nil, err
	}
	return src, nil
}

func (s *PostgresStore) ListSources(ctx context.Context) ([]Source, error) {
	rows, err := s.db.Query(ctx, `SELECT `+sourceColumns+` FROM intake_sources ORDER BY created_at DESC, id`)
	if err != nil {
		return nil, fmt.Errorf("list intake sources: %w", err)
	}
	defer rows.Close()
	out := []Source{}
	for rows.Next() {
		src, err := scanSource(rows)
		if err != nil {
			return nil, fmt.Errorf("scan intake source: %w", err)
		}
		out = append(out, *src)
	}
	return out, rows.Err()
}

func (s *PostgresStore) LiveSource(ctx context.Context, id uuid.UUID) (*Source, error) {
	src, err := scanSource(s.db.QueryRow(ctx,
		`SELECT `+sourceColumns+` FROM intake_sources WHERE id = $1 AND revoked_at IS NULL`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get intake source: %w", err)
	}
	return src, nil
}

func (s *PostgresStore) Rotate(ctx context.Context, id uuid.UUID, cred *NewCredential) (int, error) {
	var generation int
	err := s.db.QueryRow(ctx, `
		UPDATE global_intake_credentials
		   SET token_hash = $2, generation = generation + 1
		 WHERE source_id = $1 AND tenant = current_tenant() AND revoked_at IS NULL
		 RETURNING generation`, id, cred.TokenHash).Scan(&generation)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("rotate intake credential: %w", err)
	}
	return generation, nil
}

func (s *PostgresStore) Revoke(ctx context.Context, id uuid.UUID) error {
	return s.db.InTx(ctx, func(q db.Querier) error {
		tag, err := q.Exec(ctx, `
			UPDATE global_intake_credentials SET revoked_at = now()
			 WHERE source_id = $1 AND tenant = current_tenant() AND revoked_at IS NULL`, id)
		if err != nil {
			return fmt.Errorf("revoke intake credential: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		if _, err := q.Exec(ctx, `UPDATE intake_sources SET revoked_at = now() WHERE id = $1 AND revoked_at IS NULL`, id); err != nil {
			return fmt.Errorf("revoke intake source: %w", err)
		}
		return nil
	})
}

func (s *PostgresStore) CredentialOf(ctx context.Context, id uuid.UUID) (*Credential, error) {
	var c Credential
	var kind string
	err := s.db.Global().QueryRow(ctx, `
		SELECT tenant, kind, token_hash, generation FROM global_intake_credentials
		 WHERE source_id = $1 AND revoked_at IS NULL`, id).Scan(&c.Tenant, &kind, &c.TokenHash, &c.Generation)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("resolve intake source: %w", err)
	}
	c.Kind = AuthKind(kind)
	return &c, nil
}

// BeginDelivery's upsert is the dedupe, as githubhook's: a second copy of a
// delivery racing the first waits on the first's row, sees it processing and
// backs off. Only a delivery recorded as failed, or abandoned, is handed out
// again.
func (s *PostgresStore) BeginDelivery(ctx context.Context, d *Delivery) (bool, error) {
	var begun bool
	err := s.db.InTx(ctx, func(q db.Querier) error {
		err := q.QueryRow(ctx, `
			INSERT INTO intake_deliveries (source_id, delivery_id, event, key, outcome)
			VALUES ($1, $2, $3, $4, 'processing')
			ON CONFLICT (tenant_id, source_id, delivery_id) DO UPDATE
			   SET outcome = 'processing', detail = '', downgraded = false, ticket_id = NULL,
			       event = EXCLUDED.event, key = EXCLUDED.key, received_at = now()
			 WHERE intake_deliveries.outcome = 'error'
			    OR (intake_deliveries.outcome = 'processing' AND intake_deliveries.received_at < now() - $5::interval)
			RETURNING true`, d.SourceID, d.ID, d.Event, d.Key, hookkit.AbandonedAfter.String()).Scan(&begun)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("record intake delivery: %w", err)
		}
		if _, err := q.Exec(ctx, `UPDATE intake_sources SET last_delivery_at = now() WHERE id = $1`, d.SourceID); err != nil {
			return fmt.Errorf("record intake source use: %w", err)
		}
		return nil
	})
	return begun, err
}

func (s *PostgresStore) FinishDelivery(ctx context.Context, d *Delivery) error {
	_, err := s.db.Exec(ctx, `
		UPDATE intake_deliveries SET outcome = $3, detail = $4, ticket_id = $5, downgraded = $6, event = $7, key = $8
		 WHERE source_id = $1 AND delivery_id = $2`,
		d.SourceID, d.ID, string(d.Outcome), d.Detail, d.TicketID, d.Downgraded, d.Event, d.Key)
	if err != nil {
		return fmt.Errorf("record intake delivery outcome: %w", err)
	}
	return nil
}

func (s *PostgresStore) RecordRefusal(ctx context.Context, d *Delivery) error {
	_, err := s.db.Exec(ctx, `
		INSERT INTO intake_deliveries (source_id, delivery_id, outcome, detail)
		VALUES ($1, $2, $3, $4) ON CONFLICT DO NOTHING`, d.SourceID, d.ID, string(d.Outcome), d.Detail)
	if err != nil {
		return fmt.Errorf("record refused intake delivery: %w", err)
	}
	return nil
}

func (s *PostgresStore) ListDeliveries(ctx context.Context, limit int) ([]Delivery, error) {
	rows, err := s.db.Query(ctx, `
		SELECT delivery_id, source_id, event, key, outcome, downgraded, detail, ticket_id, received_at
		  FROM intake_deliveries
		 ORDER BY received_at DESC, delivery_id
		 LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("list intake deliveries: %w", err)
	}
	defer rows.Close()
	out := []Delivery{}
	for rows.Next() {
		var d Delivery
		var outcome string
		if err := rows.Scan(&d.ID, &d.SourceID, &d.Event, &d.Key, &outcome, &d.Downgraded, &d.Detail, &d.TicketID, &d.ReceivedAt); err != nil {
			return nil, fmt.Errorf("scan intake delivery: %w", err)
		}
		d.Outcome = Outcome(outcome)
		out = append(out, d)
	}
	return out, rows.Err()
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
