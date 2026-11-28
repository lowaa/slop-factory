package local

import "github.com/lowaa/slop-factory/slop-bot/internal/sqlitex"

// DB is the System's database, for tests that break a table to see the
// error a read or write reports.
func (s *System) DB() *sqlitex.DB { return s.db }
