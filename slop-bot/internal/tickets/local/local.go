// Package local is local mode's TicketingSystem: tickets, comments,
// blockers and the event feed in a SQLite database of their own.
//
// The feed's wake-ups are in-process: a write wakes this System's follower
// as it commits. Every write is meant to go through the daemon's one
// System (the CLI and agents reach it over the control socket), so that is
// every write. A write by another process to the same file is still in the
// log, and is delivered at this System's next write or Start.
package local

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"sync"

	"github.com/lowaa/slop-factory/go-clock"
	"github.com/uptrace/bun"

	"github.com/lowaa/slop-factory/slop-bot/internal/ident"
	"github.com/lowaa/slop-factory/slop-bot/internal/sqlitex"
	"github.com/lowaa/slop-factory/slop-bot/internal/tickets"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// Migrations is the database's goose migrations.
func Migrations() fs.FS {
	sub, err := fs.Sub(migrationFiles, "migrations")
	if err != nil {
		panic(err) // the embed pattern above guarantees the directory
	}
	return sub
}

// Options configures Open.
type Options struct {
	// Path is the database file, under $XDG_DATA_HOME in production.
	Path   string
	Config tickets.Config
	// Logger and Clock are required.
	Logger *slog.Logger
	Clock  clock.Clock
}

// System is the local TicketingSystem. Close it when done.
type System struct {
	db    *sqlitex.DB
	log   *slog.Logger
	clock clock.Clock
	cfg   tickets.Config
	wake  *waker
	feed  *feed
}

var _ tickets.TicketingSystem = (*System)(nil)

// Open opens (creating and migrating if needed) the database at opts.Path.
func Open(ctx context.Context, opts *Options) (*System, error) {
	if opts == nil || opts.Logger == nil || opts.Clock == nil {
		return nil, errors.New("local: Open needs Logger and Clock")
	}
	db, err := sqlitex.Open(ctx, &sqlitex.Options{Path: opts.Path, Migrations: Migrations(), Logger: opts.Logger})
	if err != nil {
		return nil, err
	}
	s := &System{db: db, log: opts.Logger, clock: opts.Clock, cfg: opts.Config, wake: newWaker()}
	s.feed = newFeed(s)
	return s, nil
}

// Close stops the feed if it is running and closes the database.
func (s *System) Close() error {
	stopErr := s.Stop(context.Background())
	return errors.Join(stopErr, s.db.Close())
}

// write runs fn in one transaction and, once it commits, wakes the feed.
func (s *System) write(ctx context.Context, fn func(ctx context.Context, tx bun.Tx) error) error {
	if err := s.db.RunInTx(ctx, nil, fn); err != nil {
		return err
	}
	s.wake.signal()
	return nil
}

// read runs fn in one read transaction, so a ticket, its blockers and its
// comments come from one state of the database, not from either side of a
// write that landed between two queries.
func (s *System) read(ctx context.Context, fn func(ctx context.Context, tx bun.Tx) error) error {
	return s.db.RunInTx(ctx, &sql.TxOptions{ReadOnly: true}, fn)
}

func (s *System) now() int64 { return nanos(s.clock.Now()) }

// actor is a validated principal, in the columns it is stored as.
type actor struct {
	by     ident.Principal
	agent  *string
	ticket *int64
}

// actorFor validates by, and that an agent's ticket exists: an agent is
// only ever an agent of a real ticket.
func actorFor(ctx context.Context, tx bun.IDB, by ident.Principal) (*actor, error) {
	if err := tickets.CheckPrincipal(by); err != nil {
		return nil, err
	}
	agent, ticket, err := principalCols(by)
	if err != nil {
		return nil, err
	}
	if ticket != nil {
		exists, err := tx.NewSelect().Model((*ticketRow)(nil)).Where("id = ?", *ticket).Exists(ctx)
		if err != nil {
			return nil, fmt.Errorf("local: reading agent's ticket: %w", err)
		}
		if !exists {
			return nil, fmt.Errorf("%w: %s's ticket does not exist", tickets.ErrInvalidPrincipal, by)
		}
	}
	return &actor{by: by, agent: agent, ticket: ticket}, nil
}

// event appends to the feed, in the transaction of the change it records.
func (s *System) event(ctx context.Context, tx bun.IDB, a *actor, kind tickets.EventKind, ticket int64) error {
	row := &eventRow{Kind: string(kind), TicketID: ticket, ByAgent: a.agent, ByTicket: a.ticket, At: s.now()}
	if _, err := tx.NewInsert().Model(row).Exec(ctx); err != nil {
		return fmt.Errorf("local: recording %s event: %w", kind, err)
	}
	return nil
}

func (s *System) insertComment(ctx context.Context, tx bun.IDB, a *actor, ticket int64, body string) (*commentRow, error) {
	row := &commentRow{TicketID: ticket, AuthorAgent: a.agent, AuthorTicket: a.ticket, Body: body, CreatedAt: s.now()}
	if _, err := tx.NewInsert().Model(row).Exec(ctx); err != nil {
		return nil, fmt.Errorf("local: adding comment: %w", err)
	}
	return row, nil
}

// waker wakes the feed's follower when a write commits. Each signal closes
// the current channel and replaces it, so any number of waits see it, and a
// follower that takes the channel before reading the log can't miss a
// write that commits after its read.
type waker struct {
	mu sync.Mutex
	ch chan struct{}
}

func newWaker() *waker { return &waker{ch: make(chan struct{})} }

func (w *waker) wait() <-chan struct{} {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.ch
}

func (w *waker) signal() {
	w.mu.Lock()
	defer w.mu.Unlock()
	close(w.ch)
	w.ch = make(chan struct{})
}
