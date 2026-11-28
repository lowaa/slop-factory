-- +goose Up

-- The directory the agent's conversation started in. Claude Code and pi
-- find a session by its cwd's project, so a resume runs there even after
-- the agent gains a primary worktree; '' (older rows) resumes in the
-- primary worktree, as before.
ALTER TABLE agents ADD COLUMN conversation_dir TEXT NOT NULL DEFAULT '';

-- +goose Down

ALTER TABLE agents DROP COLUMN conversation_dir;
