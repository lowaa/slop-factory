package consent

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgconn"
)

// opError is an error whose text is safe to log and return: it names the
// operation and a class of failure, never a value. It is the only kind of
// error this package logs in full. Anything else is logged as unclassified,
// because a Store or Documents written by an app can put anything in its
// errors, a user id included.
type opError struct {
	msg   string
	cause error // kept for errors.Is only, never printed
}

func (e *opError) Error() string { return e.msg }
func (e *opError) Unwrap() error { return e.cause }

// errText is err as a log line may carry it.
func errText(err error) string {
	var oe *opError
	if errors.As(err, &oe) {
		return oe.msg
	}
	if errors.Is(err, context.Canceled) {
		return "request canceled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "deadline exceeded"
	}
	return "unclassified error"
}

// storeErr names what failed without carrying the database's message: a
// constraint violation's detail quotes the row's values, and an error that
// reaches a log or a response must not carry a user id or a decision.
func storeErr(op string, err error) error {
	if err == nil {
		return nil
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return &opError{msg: fmt.Sprintf("consent: %s: database error (sqlstate %s)", op, pgErr.Code), cause: err}
	}
	return &opError{msg: fmt.Sprintf("consent: %s: database error", op), cause: err}
}
