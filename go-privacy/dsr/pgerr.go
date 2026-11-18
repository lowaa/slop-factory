package dsr

import (
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgconn"
)

// opError names an operation and a class of failure, never a value: a
// constraint violation's detail quotes the row, which here is a person's
// email and message. It is the only kind of error this package logs.
type opError struct {
	msg   string
	cause error // for errors.Is only, never printed
}

func (e *opError) Error() string { return e.msg }
func (e *opError) Unwrap() error { return e.cause }

func storeErr(op string, err error) error {
	if err == nil {
		return nil
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return &opError{msg: fmt.Sprintf("dsr: %s: database error (sqlstate %s)", op, pgErr.Code), cause: err}
	}
	return &opError{msg: fmt.Sprintf("dsr: %s: database error", op), cause: err}
}
