// Package state is slop-bot's own database, state.db: the daemon's records
// that belong to no ticketing system and must survive a restart (agents,
// the inbox, the event log, the queue, ...). See the design doc, "slop-bot's
// own state database".
//
// Each table has a store type implemented on bun, and small interfaces so a
// consumer depends only on the methods it uses. The daemon is the only
// writer.
package state

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"time"

	clock "github.com/lowaa/slop-factory/go-clock"
	"github.com/lowaa/slop-factory/slop-bot/internal/sqlitex"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// Migrations is state.db's goose migration set.
func Migrations() fs.FS {
	sub, err := fs.Sub(migrationFiles, "migrations")
	if err != nil {
		// Only a wrong literal above can get here, and every test would fail.
		panic(err)
	}
	return sub
}

var (
	// ErrNotFound is returned when the row asked for doesn't exist.
	ErrNotFound = errors.New("state: not found")
	// ErrConflict is returned when a write's precondition no longer holds:
	// the agent isn't in the state the caller expected, or it already has
	// what the caller tried to add.
	ErrConflict = errors.New("state: conflict")
	// ErrEnded is returned when a write targets an agent that has ended.
	ErrEnded = errors.New("state: agent has ended")
	// ErrClosed is returned by a follower whose EventLog was closed.
	ErrClosed = errors.New("state: closed")
)

// Options configures Open.
type Options struct {
	// Path is the state.db file.
	Path   string
	Logger *slog.Logger
	// Clock stamps every row. Required.
	Clock clock.Clock
}

// State is every store over one state.db.
type State struct {
	Agents      *Agents
	Messages    *Messages
	Events      *Events
	Queue       *Queue
	Cursors     *Cursors
	Transcripts *Transcripts
	Settings    *Settings
	Pruner      *Pruner

	// db is set only when State opened it, and so owns it.
	db *sqlitex.DB
}

// Open opens and migrates state.db, and returns its stores. Close the State
// when done.
func Open(ctx context.Context, opts *Options) (*State, error) {
	if opts == nil || opts.Clock == nil {
		return nil, errors.New("state: Open needs a Clock")
	}
	db, err := sqlitex.Open(ctx, &sqlitex.Options{Path: opts.Path, Migrations: Migrations(), Logger: opts.Logger})
	if err != nil {
		return nil, err
	}
	s := New(db, opts.Clock)
	s.db = db
	return s, nil
}

// New returns the stores over db, which must hold state.db's migrations.
// The caller keeps ownership of db; Close still ends the event followers.
func New(db *sqlitex.DB, clk clock.Clock) *State {
	return &State{
		Agents:      &Agents{db: db, clock: clk},
		Messages:    &Messages{db: db, clock: clk},
		Events:      newEvents(db, clk),
		Queue:       &Queue{db: db, clock: clk},
		Cursors:     &Cursors{db: db, clock: clk},
		Transcripts: &Transcripts{db: db, clock: clk},
		Settings:    &Settings{db: db, clock: clk},
		Pruner:      &Pruner{db: db},
	}
}

// Close wakes every event follower with ErrClosed, then closes the database
// if Open opened it.
func (s *State) Close() error {
	s.Events.Close()
	if s.db == nil {
		return nil
	}
	if err := s.db.Close(); err != nil {
		return fmt.Errorf("state: closing: %w", err)
	}
	return nil
}

// Times are stored as unix milliseconds; see the migration for why.
func toMillis(t time.Time) int64 { return t.UnixMilli() }

func fromMillis(ms int64) time.Time { return time.UnixMilli(ms).UTC() }

func fromMillisPtr(ms *int64) *time.Time {
	if ms == nil {
		return nil
	}
	t := fromMillis(*ms)
	return &t
}

// now is the clock's time at the precision it is stored at, so what a write
// returns equals what a later read returns.
func now(clk clock.Clock) time.Time { return fromMillis(toMillis(clk.Now())) }
