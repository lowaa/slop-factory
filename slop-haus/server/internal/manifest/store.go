package manifest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
)

// Manifest is a tenant's stored declaration.
type Manifest struct {
	Repos []Repo
	// Revision counts the tenant's saves, clears included. A proposal made
	// against one revision is not applied to another.
	Revision  int64
	UpdatedAt time.Time
	// UpdatedBy is nil when the owner who last saved it has since been deleted.
	UpdatedBy *uuid.UUID
	// Replaced is the repos a save overwrote, read under the revision lock
	// so it is exactly what this save replaced. Set by saves only.
	Replaced []Repo
}

// Store keeps the tenant's manifest, a single row.
type Store interface {
	// Get returns nil when the tenant has declared none.
	Get(ctx context.Context) (*Manifest, error)
	// Revision is the tenant's current revision: zero before its first save,
	// and kept through a clear.
	Revision(ctx context.Context) (int64, error)
	// Put replaces the manifest; the repos are already validated.
	Put(ctx context.Context, repos []Repo, by uuid.UUID) (*Manifest, error)
	// Delete returns the tenant to declaring none.
	Delete(ctx context.Context) error

	// The rest run in a transaction the caller holds, to save a manifest
	// together with something else.

	// LockRevision reads the revision and holds it until the transaction
	// ends, so no save lands between a check and the write that follows.
	LockRevision(ctx context.Context, q db.Querier) (int64, error)
	// PutAt is Put, refused with ErrStaleRevision unless the revision is base.
	PutAt(ctx context.Context, q db.Querier, repos []Repo, by uuid.UUID, base int64) (*Manifest, error)
}

// PostgresStore runs every query in the context's tenant; row level
// security hides every other tenant's manifest.
type PostgresStore struct {
	db *db.DB
}

func NewPostgresStore(database *db.DB) *PostgresStore { return &PostgresStore{db: database} }

var _ Store = (*PostgresStore)(nil)

func (s *PostgresStore) Get(ctx context.Context) (*Manifest, error) {
	var body []byte
	m := &Manifest{}
	err := s.db.QueryRow(ctx, `
		SELECT m.repos, m.updated_at, m.updated_by, COALESCE(r.revision, 0)
		  FROM workspace_manifests m
		  LEFT JOIN workspace_manifest_revisions r ON r.tenant_id = m.tenant_id`).Scan(&body, &m.UpdatedAt, &m.UpdatedBy, &m.Revision)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("manifest: get: %w", err)
	}
	if m.Repos, err = decodeRepos(body); err != nil {
		return nil, fmt.Errorf("manifest: decode: %w", err)
	}
	return m, nil
}

func (s *PostgresStore) Revision(ctx context.Context) (int64, error) {
	var rev int64
	err := s.db.QueryRow(ctx, `SELECT COALESCE((SELECT revision FROM workspace_manifest_revisions), 0)`).Scan(&rev)
	if err != nil {
		return 0, fmt.Errorf("manifest: revision: %w", err)
	}
	return rev, nil
}

func (s *PostgresStore) Put(ctx context.Context, repos []Repo, by uuid.UUID) (*Manifest, error) {
	var m *Manifest
	err := s.db.InTx(ctx, func(q db.Querier) error {
		if _, err := lockRevision(ctx, q); err != nil {
			return err
		}
		var err error
		m, err = put(ctx, q, repos, by, nil)
		return err
	})
	return m, err
}

func (s *PostgresStore) PutAt(ctx context.Context, q db.Querier, repos []Repo, by uuid.UUID, base int64) (*Manifest, error) {
	return put(ctx, q, repos, by, &base)
}

func (s *PostgresStore) LockRevision(ctx context.Context, q db.Querier) (int64, error) {
	return lockRevision(ctx, q)
}

func (s *PostgresStore) Delete(ctx context.Context) error {
	return s.db.InTx(ctx, func(q db.Querier) error {
		if _, err := lockRevision(ctx, q); err != nil {
			return err
		}
		if _, err := bump(ctx, q, nil); err != nil {
			return err
		}
		if _, err := q.Exec(ctx, `DELETE FROM workspace_manifests`); err != nil {
			return fmt.Errorf("manifest: delete: %w", err)
		}
		return nil
	})
}

// lockRevision creates the tenant's revision row if it has none, so there is
// always a row to lock (a tenant's first save has no manifest row to lock),
// and holds it.
func lockRevision(ctx context.Context, q db.Querier) (int64, error) {
	if _, err := q.Exec(ctx, `INSERT INTO workspace_manifest_revisions (revision) VALUES (0) ON CONFLICT DO NOTHING`); err != nil {
		return 0, fmt.Errorf("manifest: lock revision: %w", err)
	}
	var rev int64
	if err := q.QueryRow(ctx, `SELECT revision FROM workspace_manifest_revisions FOR UPDATE`).Scan(&rev); err != nil {
		return 0, fmt.Errorf("manifest: lock revision: %w", err)
	}
	return rev, nil
}

// bump moves the revision on, only from base when one is given.
func bump(ctx context.Context, q db.Querier, base *int64) (int64, error) {
	var rev int64
	err := q.QueryRow(ctx, `
		UPDATE workspace_manifest_revisions SET revision = revision + 1
		 WHERE $1::bigint IS NULL OR revision = $1
		RETURNING revision`, base).Scan(&rev)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrStaleRevision
	}
	if err != nil {
		return 0, fmt.Errorf("manifest: bump revision: %w", err)
	}
	return rev, nil
}

func put(ctx context.Context, q db.Querier, repos []Repo, by uuid.UUID, base *int64) (*Manifest, error) {
	body, err := json.Marshal(repos)
	if err != nil {
		return nil, fmt.Errorf("manifest: encode: %w", err)
	}
	rev, err := bump(ctx, q, base)
	if err != nil {
		return nil, err
	}
	m := &Manifest{Repos: repos, Revision: rev, UpdatedBy: &by}
	var previous []byte
	switch err := q.QueryRow(ctx, `SELECT repos FROM workspace_manifests`).Scan(&previous); {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return nil, fmt.Errorf("manifest: read previous: %w", err)
	default:
		if m.Replaced, err = decodeRepos(previous); err != nil {
			return nil, fmt.Errorf("manifest: decode previous: %w", err)
		}
	}
	if err := q.QueryRow(ctx, `
		INSERT INTO workspace_manifests (repos, updated_by) VALUES ($1, $2)
		ON CONFLICT (tenant_id) DO UPDATE
		   SET repos = EXCLUDED.repos, updated_by = EXCLUDED.updated_by, updated_at = now()
		RETURNING updated_at`, body, by).Scan(&m.UpdatedAt); err != nil {
		return nil, fmt.Errorf("manifest: put: %w", err)
	}
	return m, nil
}

// decodeRepos reads stored repos. Rows written before enforce_independent_apps
// was split carry only that flag, which switched on both features: it sets
// both new ones unless the entry already says otherwise, so no stored
// manifest changes behaviour. Proposals are decoded strictly elsewhere and
// do not get this: one still carrying the old field fails at approval and is
// proposed again.
func decodeRepos(body []byte) ([]Repo, error) {
	var repos []Repo
	if err := json.Unmarshal(body, &repos); err != nil {
		return nil, err
	}
	var legacy []struct {
		Enforce bool  `json:"enforce_independent_apps"`
		Batch   *bool `json:"batch_within_apps"`
		Skip    *bool `json:"skip_retest_across_apps"`
	}
	if err := json.Unmarshal(body, &legacy); err != nil {
		return nil, err
	}
	for i := range repos {
		if !legacy[i].Enforce {
			continue
		}
		repos[i].BatchWithinApps = repos[i].BatchWithinApps || legacy[i].Batch == nil
		repos[i].SkipRetestAcrossApps = repos[i].SkipRetestAcrossApps || legacy[i].Skip == nil
	}
	return repos, nil
}
