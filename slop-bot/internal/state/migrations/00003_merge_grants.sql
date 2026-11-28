-- +goose Up

-- The user's go-ahead to merge a ticket without AutoMerge (`slop-bot tell
-- <agent> --merge`), which the merge guard checks. It is the ticket's, not
-- an agent's, so a resumed or replacement agent keeps it; it is cleared when
-- the ticket ends, so a reopened ticket needs a new one.
CREATE TABLE merge_grants (
  ticket_id TEXT PRIMARY KEY CHECK (ticket_id <> ''),
  granted_by TEXT NOT NULL CHECK (granted_by <> ''),
  granted_at INTEGER NOT NULL
);

-- +goose Down
DROP TABLE merge_grants;
