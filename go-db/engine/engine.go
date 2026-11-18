// Package engine defines the database engine abstraction: the only call
// surface the rest of this module (tenancy, dbtest, and eventually
// application code) is allowed to use to talk to the database.
//
// Postgres is the only implementation, but repository-style code should be
// written against Querier/Engine/Tx, not against the Postgres type or pgx
// directly.
package engine

import "context"

// Querier is the minimal set of methods needed to run queries. Both a raw
// pooled connection and a transaction satisfy it, so code can be written
// against Querier without caring whether it is inside a transaction.
type Querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) Row
}

// IsolationLevel is a transaction isolation level. The zero value is the
// database's default (read committed, for Postgres).
type IsolationLevel string

const (
	IsolationDefault        IsolationLevel = ""
	IsolationRepeatableRead IsolationLevel = "repeatable read"
	IsolationSerializable   IsolationLevel = "serializable"
)

// TxOptions configures a transaction. The zero value is the same
// transaction BeginTx starts.
//
// These have to be fixed when the transaction begins -- Postgres rejects
// SET TRANSACTION ISOLATION LEVEL after the first query -- which is why they
// are a BeginTx option rather than something a caller can issue itself:
// a tenant-scoped transaction has already run its set_config by the time
// the caller sees it.
type TxOptions struct {
	Isolation IsolationLevel
	// ReadOnly makes the server reject writes, rather than leaving a
	// read-only intent to review.
	ReadOnly bool
}

// Tx is a Querier that can be committed or rolled back.
type Tx interface {
	Querier
	Commit(ctx context.Context) error
	Rollback(ctx context.Context) error
}

// Engine represents a connected database (pool). It is itself a Querier for
// one-off, non-transactional statements, and can also hand out transactions
// via BeginTx.
type Engine interface {
	Querier

	Connect(ctx context.Context) error
	Close() error
	BeginTx(ctx context.Context) (Tx, error)
	BeginTxWithOptions(ctx context.Context, opts TxOptions) (Tx, error)
	Ping(ctx context.Context) error

	// ConnString returns the connection details this Engine was configured
	// with, so packages that need their own handle to the same database
	// (e.g. migrations, which needs a database/sql-style connection for
	// golang-migrate) don't need a separately maintained DSN.
	ConnString() string
}
