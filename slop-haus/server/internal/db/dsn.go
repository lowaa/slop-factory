package db

import "github.com/lowaa/slop-factory/go-db/engine"

// WithCredentials returns dsn with its user and password replaced: the app
// role connects to the same database the owner migrated, as itself. Its
// errors never contain any part of dsn.
func WithCredentials(dsn, user, password string) (string, error) {
	return engine.WithCredentials(dsn, &engine.DSNCredentials{User: user, Password: password})
}
