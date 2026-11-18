// Package pgerr classifies Postgres errors by SQLSTATE, so callers need not
// repeat raw state codes.
package pgerr

import (
	"errors"
	"slices"

	"github.com/jackc/pgx/v5/pgconn"
)

const (
	sqlStateForeignKeyViolation = "23503"
	sqlStateUniqueViolation     = "23505"
)

// sqlStateError is an interface rather than *pgconn.PgError: the database
// engine may wrap the driver's error in its own type.
type sqlStateError interface {
	SQLState() string
	error
}

// IsUniqueViolation reports whether err is a unique-constraint violation. With
// constraint names, only a violation of one of those constraints counts.
func IsUniqueViolation(err error, constraint ...string) bool {
	return hasState(err, sqlStateUniqueViolation, constraint)
}

// IsForeignKeyViolation reports whether err is a foreign-key violation. With
// constraint names, only a violation of one of those constraints counts.
func IsForeignKeyViolation(err error, constraint ...string) bool {
	return hasState(err, sqlStateForeignKeyViolation, constraint)
}

func hasState(err error, state string, constraint []string) bool {
	var se sqlStateError
	if !errors.As(err, &se) || se.SQLState() != state {
		return false
	}
	if len(constraint) == 0 {
		return true
	}
	var pg *pgconn.PgError
	return errors.As(err, &pg) && slices.Contains(constraint, pg.ConstraintName)
}
