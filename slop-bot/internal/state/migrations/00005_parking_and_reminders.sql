-- +goose Up

-- When the agent was stopped to free a process past concurrency.max_live,
-- NULL while it has one. Its state still says what it waits on; this says
-- it has no process, so a restart leaves it stopped and it is resumed by
-- id only when it next has work.
ALTER TABLE agents ADD COLUMN parked_at INTEGER;

-- When the user was reminded of this unanswered ask, so the reminder goes
-- once per ask, across restarts too.
ALTER TABLE asks ADD COLUMN reminded_at INTEGER;

-- +goose Down

ALTER TABLE asks DROP COLUMN reminded_at;
ALTER TABLE agents DROP COLUMN parked_at;
