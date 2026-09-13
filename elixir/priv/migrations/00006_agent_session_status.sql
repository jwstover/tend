-- +goose Up
-- Backing for hook-driven session status (docs/agent-sessions-plan.md §8.2).
--
-- status is what a launched session's Claude Code hooks report about itself:
-- `Stop` means it finished a turn and is sitting at the prompt (idle),
-- `Notification` means it is waiting on the user (blocked), `SessionEnd`
-- means the process is gone (ended). 'working' exists for §8.3's capture-pane
-- poller to write; no hook can report it, because Claude Code fires nothing
-- mid-tool-call. 'unknown' is the honest default: a session tend has never
-- heard from.
--
-- Deliberately not a foreign key to a status table the way tasks.state is.
-- These values are a cache of something observed out-of-band, not a workflow
-- the user moves rows through, and a row whose status tend can't interpret
-- should render as unknown rather than fail to insert.
--
-- status_updated_at is nullable rather than defaulting to now: it is the
-- tiebreaker §8.3 needs between an authoritative hook event and the poller's
-- guess, so "never observed" has to be distinguishable from "observed at row
-- creation time".
ALTER TABLE agent_sessions ADD COLUMN status TEXT NOT NULL DEFAULT 'unknown';
ALTER TABLE agent_sessions ADD COLUMN status_updated_at TEXT;

-- +goose Down
ALTER TABLE agent_sessions DROP COLUMN status_updated_at;
ALTER TABLE agent_sessions DROP COLUMN status;
