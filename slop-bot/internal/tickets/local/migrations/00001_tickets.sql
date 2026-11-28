-- +goose Up
-- A principal is stored as an agent id and that agent's ticket, both NULL
-- for the user (ident.Principal).
CREATE TABLE tickets (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  title TEXT NOT NULL CHECK (title <> ''),
  body TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL CHECK (status IN (
    'preparing', 'discussion', 'open', 'in_progress', 'misconfigured', 'done', 'cancelled')),
  assignee TEXT NOT NULL DEFAULT '',
  harness TEXT NOT NULL DEFAULT '',
  model TEXT NOT NULL DEFAULT '',
  effort TEXT NOT NULL DEFAULT '',
  auto_merge INTEGER NOT NULL DEFAULT 0 CHECK (auto_merge IN (0, 1)),
  urgent INTEGER NOT NULL DEFAULT 0 CHECK (urgent IN (0, 1)),
  urgent_reason TEXT NOT NULL DEFAULT '',
  created_by_agent TEXT,
  -- The ticket whose agent filed this one: that agent's principal ticket.
  parent_id INTEGER REFERENCES tickets (id),
  -- A JSON array of repo names, the primary first.
  repos TEXT NOT NULL DEFAULT '[]',
  -- Unix nanoseconds, from the injected clock.
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  CHECK ((created_by_agent IS NULL) = (parent_id IS NULL))
);

-- The autonomy budget counts outstanding agent-filed tickets.
CREATE INDEX tickets_agent_filed ON tickets (status) WHERE created_by_agent IS NOT NULL;

CREATE TABLE comments (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  ticket_id INTEGER NOT NULL REFERENCES tickets (id),
  author_agent TEXT,
  author_ticket INTEGER REFERENCES tickets (id),
  body TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  CHECK ((author_agent IS NULL) = (author_ticket IS NULL))
);
CREATE INDEX comments_ticket ON comments (ticket_id, id);

-- ticket_id waits on blocker_id.
CREATE TABLE blockers (
  ticket_id INTEGER NOT NULL REFERENCES tickets (id),
  blocker_id INTEGER NOT NULL REFERENCES tickets (id),
  added_by_agent TEXT,
  added_by_ticket INTEGER REFERENCES tickets (id),
  created_at INTEGER NOT NULL,
  PRIMARY KEY (ticket_id, blocker_id),
  CHECK (ticket_id <> blocker_id),
  CHECK ((added_by_agent IS NULL) = (added_by_ticket IS NULL))
) WITHOUT ROWID;
CREATE INDEX blockers_blocker ON blockers (blocker_id);

-- The feed; seq is the cursor. Writes are serialised (sqlitex takes the
-- write lock at BEGIN), so seq is allocated in commit order, and a follower
-- reading past its cursor can't miss a row that commits later with a lower
-- seq. AUTOINCREMENT so a seq is never reused.
CREATE TABLE events (
  seq INTEGER PRIMARY KEY AUTOINCREMENT,
  kind TEXT NOT NULL,
  ticket_id INTEGER NOT NULL REFERENCES tickets (id),
  by_agent TEXT,
  by_ticket INTEGER REFERENCES tickets (id),
  at INTEGER NOT NULL,
  CHECK ((by_agent IS NULL) = (by_ticket IS NULL))
);

-- +goose Down
DROP TABLE events;
DROP TABLE blockers;
DROP TABLE comments;
DROP TABLE tickets;
