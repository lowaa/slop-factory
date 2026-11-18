package legaldocs

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgconn"
)

var (
	// ErrNotFound: no such version, or none in force yet.
	ErrNotFound = errors.New("legaldocs: not found")
	// ErrVersionChanged: a published version was published again with
	// different content. Changing a document means a new version.
	ErrVersionChanged = errors.New("legaldocs: a published version cannot change")
)

// Store keeps published versions. A published version is never changed or
// removed.
type Store interface {
	// Publish records v. Publishing the same content again is a no-op;
	// different content under a published (kind, version, locale) is
	// ErrVersionChanged and leaves the stored version as it was.
	Publish(ctx context.Context, v *Version) error
	// Get returns one version, whatever its effective date.
	Get(ctx context.Context, kind, version, locale string) (*Version, error)
	// List returns every version of kind in locale, oldest first by
	// (effective, version), without the body: a history is read on every
	// public request and consent's, and the bodies can be large.
	List(ctx context.Context, kind, locale string) ([]*Version, error)
}

// opError is an error whose text is safe to log: it names the operation
// and a class of failure, never a value.
type opError struct {
	msg   string
	cause error
}

func (e *opError) Error() string { return e.msg }
func (e *opError) Unwrap() error { return e.cause }

func storeErr(op string, err error) error {
	if err == nil {
		return nil
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return &opError{msg: fmt.Sprintf("legaldocs: %s: database error (sqlstate %s)", op, pgErr.Code), cause: err}
	}
	return &opError{msg: fmt.Sprintf("legaldocs: %s: database error", op), cause: err}
}

// errText is err as a log line may carry it.
func errText(err error) string {
	var oe *opError
	switch {
	case errors.As(err, &oe):
		return oe.msg
	case errors.Is(err, context.Canceled):
		return "request canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline exceeded"
	}
	return "unclassified error"
}
