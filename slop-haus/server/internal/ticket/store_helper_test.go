package ticket_test

import (
	"github.com/lowaa/slop-factory/slop-haus/server/internal/db"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/landing"
	"github.com/lowaa/slop-factory/slop-haus/server/internal/ticket"
)

// newStore is the store as the server wires it: with landing's SQL queue,
// which ticket cannot import itself. A test's own WithMergeQueue wins.
func newStore(database *db.DB, opts ...ticket.StoreOption) *ticket.PostgresStore {
	return ticket.NewPostgresStore(database, append([]ticket.StoreOption{ticket.WithMergeQueue(landing.NewDequeuer())}, opts...)...)
}
