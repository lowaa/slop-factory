-- +goose Up

-- Times are INTEGER unix milliseconds (UTC), so ordering and the pruning
-- comparisons are numeric: SQLite has no time type, and text timestamps
-- order correctly only while every writer formats them identically.

CREATE TABLE agents (
  -- 'user' names the user in messages, so no agent may take it.
  id TEXT PRIMARY KEY CHECK (id <> '' AND id <> 'user'),
  ticket_id TEXT NOT NULL CHECK (ticket_id <> ''),
  harness TEXT NOT NULL CHECK (harness <> ''),
  driver TEXT NOT NULL CHECK (driver <> ''),
  model TEXT NOT NULL DEFAULT '',
  effort TEXT NOT NULL DEFAULT '',
  conversation_id TEXT NOT NULL DEFAULT '',
  mode TEXT NOT NULL DEFAULT 'programmatic' CHECK (mode IN ('programmatic', 'attached')),
  lease_holder TEXT NOT NULL DEFAULT '',
  lease_expires_at INTEGER,
  tui_session TEXT NOT NULL DEFAULT '',
  pool TEXT NOT NULL CHECK (pool IN ('normal', 'urgent')),
  state TEXT NOT NULL CHECK (state IN (
    'starting', 'refining', 'working', 'idle', 'waiting_user', 'discussion',
    'waiting_merge', 'attached', 'released', 'done', 'stalled', 'exited', 'crashed'
  )),
  state_since INTEGER NOT NULL,
  progress_note TEXT NOT NULL DEFAULT '',
  nudge_count INTEGER NOT NULL DEFAULT 0 CHECK (nudge_count >= 0),
  -- What the agent declared from inside an attached TUI, for its hand-back.
  declared TEXT NOT NULL DEFAULT '',
  declared_summary TEXT NOT NULL DEFAULT '',
  -- control.HashToken of the agent's token (hex sha256); the plaintext is
  -- never stored.
  token_hash TEXT NOT NULL UNIQUE CHECK (length(token_hash) = 64 AND token_hash NOT GLOB '*[^0-9a-f]*'),
  created_at INTEGER NOT NULL,
  ended_at INTEGER,
  -- An attached agent always names its lease holder and expiry, and a
  -- programmatic one has neither, so a lease can't outlive its mode.
  CHECK (
    (mode = 'attached' AND lease_holder <> '' AND lease_expires_at IS NOT NULL)
    OR (mode = 'programmatic' AND lease_holder = '' AND lease_expires_at IS NULL)
  )
);
-- One agent per ticket for the ticket's whole life: a second live agent for
-- the same ticket is a scheduler bug the database refuses.
CREATE UNIQUE INDEX agents_live_ticket ON agents (ticket_id) WHERE ended_at IS NULL;
CREATE INDEX agents_ended_at ON agents (ended_at) WHERE ended_at IS NOT NULL;

CREATE TABLE agent_worktrees (
  agent_id TEXT NOT NULL REFERENCES agents (id) ON DELETE CASCADE,
  repo TEXT NOT NULL CHECK (repo <> ''),
  path TEXT NOT NULL CHECK (path <> ''),
  branch TEXT NOT NULL DEFAULT '',
  is_primary INTEGER NOT NULL DEFAULT 0 CHECK (is_primary IN (0, 1)),
  PRIMARY KEY (agent_id, repo),
  UNIQUE (agent_id, path)
);
-- The primary worktree is the harness's cwd, so there is at most one.
CREATE UNIQUE INDEX agent_worktrees_one_primary ON agent_worktrees (agent_id) WHERE is_primary = 1;

CREATE TABLE agent_state_changes (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  agent_id TEXT NOT NULL REFERENCES agents (id) ON DELETE CASCADE,
  -- NULL for the agent's first state, at creation.
  from_state TEXT,
  to_state TEXT NOT NULL,
  reason TEXT NOT NULL DEFAULT '',
  at INTEGER NOT NULL
);
CREATE INDEX agent_state_changes_agent ON agent_state_changes (agent_id, id);
CREATE INDEX agent_state_changes_at ON agent_state_changes (at);

-- Senders and recipients are 'user' or an agent id. They aren't foreign
-- keys: the inbox history outlives pruned agent rows.
CREATE TABLE messages (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  sender TEXT NOT NULL CHECK (sender <> ''),
  recipient TEXT NOT NULL CHECK (recipient <> ''),
  kind TEXT NOT NULL CHECK (kind IN ('tell', 'ask', 'answer', 'nudge', 'system')),
  body TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  status TEXT NOT NULL DEFAULT 'queued' CHECK (status IN ('queued', 'delivered', 'read')),
  delivery_path TEXT NOT NULL DEFAULT '',
  delivered_at INTEGER,
  read_at INTEGER,
  CHECK ((status = 'queued') = (delivered_at IS NULL)),
  CHECK ((status = 'read') = (read_at IS NOT NULL))
);
CREATE INDEX messages_recipient_status ON messages (recipient, status, id);
CREATE INDEX messages_pair ON messages (sender, recipient, created_at);
CREATE INDEX messages_created_at ON messages (created_at);

-- An ask is a message (its body is the question) with these extras.
CREATE TABLE asks (
  message_id INTEGER PRIMARY KEY REFERENCES messages (id) ON DELETE CASCADE,
  options TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(options) AND json_type(options) = 'array'),
  recommendation TEXT NOT NULL DEFAULT '',
  closed_at INTEGER,
  answer_message_id INTEGER REFERENCES messages (id) ON DELETE SET NULL
);
CREATE INDEX asks_open ON asks (message_id) WHERE closed_at IS NULL;

CREATE TABLE events (
  -- AUTOINCREMENT, not a plain rowid: seq is a follower's cursor, so it must
  -- never be reused, even after pruning deletes the newest rows.
  seq INTEGER PRIMARY KEY AUTOINCREMENT,
  at INTEGER NOT NULL,
  type TEXT NOT NULL CHECK (type <> ''),
  agent_id TEXT NOT NULL DEFAULT '',
  ticket_id TEXT NOT NULL DEFAULT '',
  payload TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(payload))
);
CREATE INDEX events_at ON events (at);

CREATE TABLE cursors (
  system TEXT PRIMARY KEY CHECK (system <> ''),
  position TEXT NOT NULL,
  updated_at INTEGER NOT NULL
);

-- A pool is ordered by lane, then position: every front-lane entry (0, an
-- answered agent resuming) before every back-lane one (1, new work), and
-- first come first served within a lane. A push takes one past the largest
-- position waiting in any pool, so it orders only the rows waiting now:
-- numbers are reused once the queue drains.
CREATE TABLE queue (
  ticket_id TEXT PRIMARY KEY CHECK (ticket_id <> ''),
  pool TEXT NOT NULL CHECK (pool IN ('normal', 'urgent')),
  lane INTEGER NOT NULL CHECK (lane IN (0, 1)),
  position INTEGER NOT NULL UNIQUE,
  enqueued_at INTEGER NOT NULL
);
CREATE INDEX queue_order ON queue (pool, lane, position);

CREATE TABLE transcripts (
  path TEXT PRIMARY KEY CHECK (path <> ''),
  agent_id TEXT NOT NULL CHECK (agent_id <> ''),
  ticket_id TEXT NOT NULL DEFAULT '',
  retain_until INTEGER NOT NULL,
  created_at INTEGER NOT NULL
);
CREATE INDEX transcripts_retain_until ON transcripts (retain_until);
CREATE INDEX transcripts_agent ON transcripts (agent_id);

CREATE TABLE settings (
  key TEXT PRIMARY KEY CHECK (key <> ''),
  value TEXT NOT NULL,
  updated_at INTEGER NOT NULL
);

-- +goose Down
DROP TABLE settings;
DROP TABLE transcripts;
DROP TABLE queue;
DROP TABLE cursors;
DROP TABLE events;
DROP TABLE asks;
DROP TABLE messages;
DROP TABLE agent_state_changes;
DROP TABLE agent_worktrees;
DROP TABLE agents;
