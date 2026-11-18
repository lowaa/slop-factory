package engine

import (
	"errors"
	"net/url"
	"strconv"
)

// Both errors are constants on purpose: url.Error quotes the whole URL,
// password included, and DATABASE_URL ends up in startup logs. Nothing
// derived from the DSN may reach an error from this file.
var (
	ErrDSNNotURL       = errors.New("engine: DSN must be a postgres:// or postgresql:// URL")
	ErrDSNInvalid      = errors.New("engine: DSN is not a valid URL")
	ErrDSNEmptyUser    = errors.New("engine: DSN credentials need a user")
	ErrDSNCredsInQuery = errors.New("engine: DSN carries user/password as query parameters, which would override the replaced credentials")
)

// DSNCredentials is who a derived DSN connects as. MaxConns, when positive,
// sets pgxpool's pool_max_conns, overriding any in the source DSN so the
// binary's own cap wins over one in DATABASE_URL.
type DSNCredentials struct {
	User     string
	Password string
	MaxConns int
}

// WithCredentials returns dsn with its user and password replaced: an app
// role connects to the database the owner migrated, as itself. Only URL
// DSNs are accepted: a keyword/value DSN (`host=... password=...`), which
// pgx also takes, parses as a relative URL and would be silently mangled.
// Everything else in the URL, query parameters included, is kept.
func WithCredentials(dsn string, c *DSNCredentials) (string, error) {
	if c == nil || c.User == "" {
		return "", ErrDSNEmptyUser
	}
	u, err := url.Parse(dsn)
	if err != nil {
		return "", ErrDSNInvalid
	}
	if u.Scheme != "postgres" && u.Scheme != "postgresql" {
		return "", ErrDSNNotURL
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return "", ErrDSNInvalid
	}
	// pgx lets these override the userinfo, so keeping them would leave the
	// owner connecting as itself.
	if q.Has("user") || q.Has("password") {
		return "", ErrDSNCredsInQuery
	}
	u.User = url.UserPassword(c.User, c.Password)
	if c.MaxConns > 0 {
		q.Set("pool_max_conns", strconv.Itoa(c.MaxConns))
		u.RawQuery = q.Encode()
	}
	return u.String(), nil
}
