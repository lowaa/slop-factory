package legaldocs

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/lowaa/slop-factory/go-db/engine"
)

// PostgresStore is the Store over the table in package schema.
type PostgresStore struct {
	eng engine.Engine
}

// NewPostgresStore reads and writes through eng, which must connect as the
// app's role. The store does not close eng.
func NewPostgresStore(eng engine.Engine) *PostgresStore {
	return &PostgresStore{eng: eng}
}

const summaryColumns = `kind, version, locale, effective, material, reviewed, reviewed_by, reviewed_on`

// scanVersion reads a row of summaryColumns, plus the body if body is not nil.
func scanVersion(row engine.Row, body *string) (*Version, error) {
	v := &Version{}
	var on *time.Time
	dest := []any{&v.Kind, &v.Version, &v.Locale, &v.Effective, &v.Material, &v.Reviewed, &v.ReviewedBy, &on}
	if body != nil {
		dest = append(dest, body)
	}
	if err := row.Scan(dest...); err != nil {
		return nil, err
	}
	v.Effective = utcDate(v.Effective)
	if on != nil {
		d := utcDate(*on)
		v.ReviewedOn = &d
	}
	if body != nil {
		v.Body = *body
	}
	return v, nil
}

func (s *PostgresStore) Publish(ctx context.Context, v *Version) error {
	// DO NOTHING rather than DO UPDATE: the stored row is never touched,
	// and two replicas publishing at startup both succeed.
	_, err := s.eng.ExecContext(ctx, `INSERT INTO global_legal_document_versions
		(kind, version, locale, effective, material, reviewed, reviewed_by, reviewed_on, body, content_sha256)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT (kind, version, locale) DO NOTHING`,
		v.Kind, v.Version, v.Locale, v.Effective, v.Material, v.Reviewed, v.ReviewedBy, v.ReviewedOn, v.Body, v.ContentHash())
	if err != nil {
		return storeErr("publish", err)
	}
	var stored string
	err = s.eng.QueryRowContext(ctx, `SELECT content_sha256 FROM global_legal_document_versions
		WHERE kind = $1 AND version = $2 AND locale = $3`, v.Kind, v.Version, v.Locale).Scan(&stored)
	if err != nil {
		return storeErr("publish", err)
	}
	if stored != v.ContentHash() {
		return ErrVersionChanged
	}
	return nil
}

func (s *PostgresStore) Get(ctx context.Context, kind, version, locale string) (*Version, error) {
	var body string
	v, err := scanVersion(s.eng.QueryRowContext(ctx, `SELECT `+summaryColumns+`, body
		FROM global_legal_document_versions WHERE kind = $1 AND version = $2 AND locale = $3`, kind, version, locale), &body)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, storeErr("read version", err)
	}
	return v, nil
}

func (s *PostgresStore) List(ctx context.Context, kind, locale string) ([]*Version, error) {
	rows, err := s.eng.QueryContext(ctx, `SELECT `+summaryColumns+`
		FROM global_legal_document_versions WHERE kind = $1 AND locale = $2
		ORDER BY effective, version`, kind, locale)
	if err != nil {
		return nil, storeErr("list versions", err)
	}
	defer rows.Close()
	var out []*Version
	for rows.Next() {
		v, err := scanVersion(rows, nil)
		if err != nil {
			return nil, storeErr("list versions", err)
		}
		out = append(out, v)
	}
	return out, storeErr("list versions", rows.Err())
}
