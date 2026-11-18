package consent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/lowaa/slop-factory/go-db/engine"
)

// PostgresStore is the Store over the tables in package schema.
type PostgresStore struct {
	eng engine.Engine
}

// NewPostgresStore reads and writes through eng, which must connect as the
// app's role. The store does not close eng.
func NewPostgresStore(eng engine.Engine) *PostgresStore {
	return &PostgresStore{eng: eng}
}

const eventColumns = `id, user_id, purpose, granted, document_kind, document_version, source,
	decided_at, recorded_at, merged_from_anonymous, COALESCE(merge_token, ''), COALESCE(session_id, '')`

func scanEvent(row pgx.Row) (*Event, error) {
	e := &Event{}
	var source string
	err := row.Scan(&e.ID, &e.UserID, &e.Purpose, &e.Granted, &e.DocumentKind, &e.DocumentVersion, &source,
		&e.DecidedAt, &e.RecordedAt, &e.MergedFromAnonymous, &e.MergeToken, &e.SessionID)
	e.Source = Source(source)
	return e, err
}

func (s *PostgresStore) Current(ctx context.Context, userID uuid.UUID) (map[string]*Event, error) {
	return currentOf(ctx, s.eng, userID)
}

// currentOf: the latest event per purpose, by decided_at then id.
func currentOf(ctx context.Context, q engine.Querier, userID uuid.UUID) (map[string]*Event, error) {
	rows, err := q.QueryContext(ctx, `SELECT DISTINCT ON (purpose) `+eventColumns+`
		FROM global_consent_events WHERE user_id = $1
		ORDER BY purpose, decided_at DESC, id DESC`, userID)
	if err != nil {
		return nil, storeErr("read current", err)
	}
	defer rows.Close()
	out := map[string]*Event{}
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			return nil, storeErr("read current", err)
		}
		out[e.Purpose] = e
	}
	if err := rows.Err(); err != nil {
		return nil, storeErr("read current", err)
	}
	return out, nil
}

func (s *PostgresStore) History(ctx context.Context, userID uuid.UUID) ([]*Event, error) {
	rows, err := s.eng.QueryContext(ctx, `SELECT `+eventColumns+`
		FROM global_consent_events WHERE user_id = $1 ORDER BY id`, userID)
	if err != nil {
		return nil, storeErr("read history", err)
	}
	defer rows.Close()
	var out []*Event
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			return nil, storeErr("read history", err)
		}
		out = append(out, e)
	}
	return out, storeErr("read history", rows.Err())
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func (s *PostgresStore) Apply(ctx context.Context, userID uuid.UUID, plan func(map[string]*Event) (*Plan, error)) (*Applied, error) {
	tx, err := s.eng.BeginTx(ctx)
	if err != nil {
		return nil, storeErr("begin", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(context.WithoutCancel(ctx))
		}
	}()

	// One user's writes run one after another. NO KEY UPDATE conflicts with
	// itself but not with the key-share lock the events' foreign key takes,
	// so it orders concurrent decisions without blocking other inserts.
	var locked uuid.UUID
	err = tx.QueryRowContext(ctx, `SELECT id FROM global_users WHERE id = $1 FOR NO KEY UPDATE`, userID).Scan(&locked)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrUnknownUser
	}
	if err != nil {
		return nil, storeErr("lock user", err)
	}

	current, err := currentOf(ctx, tx, userID)
	if err != nil {
		return nil, err
	}
	p, err := plan(current)
	if err != nil {
		return nil, err
	}

	applied := &Applied{}
	if p.MergeToken != "" {
		tag, err := tx.ExecContext(ctx,
			`INSERT INTO global_consent_merge_tokens (token_hash) VALUES ($1) ON CONFLICT DO NOTHING`, hashToken(p.MergeToken))
		if err != nil {
			return nil, storeErr("claim merge token", err)
		}
		applied.TokenAlreadyMerged = tag.RowsAffected() == 0
	}
	for _, ne := range p.Events {
		if applied.TokenAlreadyMerged && ne.MergedFromAnonymous {
			continue
		}
		var token any
		if ne.MergeToken != "" {
			token = hashToken(ne.MergeToken)
		}
		var session any
		if ne.SessionID != "" {
			session = ne.SessionID
		}
		e, err := scanEvent(tx.QueryRowContext(ctx, `INSERT INTO global_consent_events
			(user_id, purpose, granted, document_kind, document_version, source, decided_at,
			 merged_from_anonymous, merge_token, session_id)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) RETURNING `+eventColumns,
			userID, ne.Purpose, ne.Granted, ne.DocumentKind, ne.DocumentVersion, string(ne.Source), ne.DecidedAt,
			ne.MergedFromAnonymous, token, session))
		if err != nil {
			return nil, storeErr("append event", err)
		}
		applied.Events = append(applied.Events, e)
	}
	// Not the caller's context: a commit the database has already accepted
	// must not be reported as failed because the request was cancelled
	// meanwhile, or a retry would append the decision twice.
	if err := tx.Commit(context.WithoutCancel(ctx)); err != nil {
		return nil, storeErr("commit", err)
	}
	committed = true
	return applied, nil
}

var _ Store = (*PostgresStore)(nil)
