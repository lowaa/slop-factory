package engine

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Row, Rows, and Result are re-exports of the pgx types rather than
// hand-rolled wrappers: only Postgres is planned as a backend, so the extra
// indirection of a wrapper type buys nothing today. This file is the only
// one in the module that imports pgx directly; every other package refers
// to these names through the engine package.
type (
	Row    = pgx.Row
	Rows   = pgx.Rows
	Result = pgconn.CommandTag
)

// Postgres is the pgx-backed Engine implementation.
type Postgres struct {
	connString string
	pool       *pgxpool.Pool
}

// NewPostgres constructs a Postgres engine for the given connection string.
// Connect must be called before it is used.
func NewPostgres(connString string) *Postgres {
	return &Postgres{connString: connString}
}

func (p *Postgres) Connect(ctx context.Context) error {
	pool, err := pgxpool.New(ctx, p.connString)
	if err != nil {
		return fmt.Errorf("engine: connect: %w", err)
	}
	p.pool = pool
	return nil
}

func (p *Postgres) Close() error {
	if p.pool != nil {
		p.pool.Close()
	}
	return nil
}

func (p *Postgres) Ping(ctx context.Context) error {
	return p.pool.Ping(ctx)
}

func (p *Postgres) ConnString() string {
	return p.connString
}

func (p *Postgres) ExecContext(ctx context.Context, query string, args ...any) (Result, error) {
	return p.pool.Exec(ctx, query, args...)
}

func (p *Postgres) QueryContext(ctx context.Context, query string, args ...any) (Rows, error) {
	return p.pool.Query(ctx, query, args...)
}

func (p *Postgres) QueryRowContext(ctx context.Context, query string, args ...any) Row {
	return p.pool.QueryRow(ctx, query, args...)
}

func (p *Postgres) BeginTx(ctx context.Context) (Tx, error) {
	return p.BeginTxWithOptions(ctx, TxOptions{})
}

func (p *Postgres) BeginTxWithOptions(ctx context.Context, opts TxOptions) (Tx, error) {
	var pgxOpts pgx.TxOptions
	switch opts.Isolation {
	case IsolationDefault:
	case IsolationRepeatableRead:
		pgxOpts.IsoLevel = pgx.RepeatableRead
	case IsolationSerializable:
		pgxOpts.IsoLevel = pgx.Serializable
	default:
		return nil, fmt.Errorf("engine: unknown isolation level %q", opts.Isolation)
	}
	if opts.ReadOnly {
		pgxOpts.AccessMode = pgx.ReadOnly
	}
	tx, err := p.pool.BeginTx(ctx, pgxOpts)
	if err != nil {
		return nil, fmt.Errorf("engine: begin tx: %w", err)
	}
	return &postgresTx{tx: tx}, nil
}

// postgresTx adapts a pgx.Tx to the engine.Tx interface.
type postgresTx struct {
	tx pgx.Tx
}

func (t *postgresTx) ExecContext(ctx context.Context, query string, args ...any) (Result, error) {
	return t.tx.Exec(ctx, query, args...)
}

func (t *postgresTx) QueryContext(ctx context.Context, query string, args ...any) (Rows, error) {
	return t.tx.Query(ctx, query, args...)
}

func (t *postgresTx) QueryRowContext(ctx context.Context, query string, args ...any) Row {
	return t.tx.QueryRow(ctx, query, args...)
}

func (t *postgresTx) Commit(ctx context.Context) error {
	return t.tx.Commit(ctx)
}

func (t *postgresTx) Rollback(ctx context.Context) error {
	return t.tx.Rollback(ctx)
}
