-- +goose Up

-- What the agent was last told about its ticket's blockers, so a notice
-- goes out on the change itself: the ticket feed is at-least-once, and a
-- redelivered or unrelated event must not repeat one.
ALTER TABLE agents ADD COLUMN blocked_told INTEGER NOT NULL DEFAULT 0 CHECK (blocked_told IN (0, 1));
-- Resumes after the harness died since the agent last finished a turn: it
-- bounds a crash loop, across daemon restarts too.
ALTER TABLE agents ADD COLUMN recoveries INTEGER NOT NULL DEFAULT 0 CHECK (recoveries >= 0);
-- A released agent slop-bot gave up on (it wouldn't start, or kept dying),
-- as against one a person took over: only the latter keeps new agents off
-- its ticket.
ALTER TABLE agents ADD COLUMN given_up INTEGER NOT NULL DEFAULT 0 CHECK (given_up IN (0, 1));

-- +goose Down

ALTER TABLE agents DROP COLUMN given_up;
ALTER TABLE agents DROP COLUMN recoveries;
ALTER TABLE agents DROP COLUMN blocked_told;
