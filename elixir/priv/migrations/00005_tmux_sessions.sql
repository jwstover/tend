-- +goose Up
-- Backing for tmux-backed launch/attach (docs/agent-sessions-plan.md §8.1).
--
-- tmux_session stores the name explicitly rather than re-deriving it from
-- external_id at read time, so the naming scheme can change later without a
-- backfill. Empty for rows written before this migration and for sessions
-- launched on a host with no tmux — both mean "not attachable", which is
-- exactly what has-session would report anyway.
--
-- needs_recap marks a session that was backgrounded rather than exited:
-- detaching and exiting are both a clean exit to tea.ExecProcess, so the
-- TUI distinguishes them with `tmux has-session` and skips the recap while
-- the session is still live — firing `claude -p --resume` against a running
-- session would put two processes on one session id and one transcript.
-- Nothing drains this flag yet; Phase 4.2's SessionEnd hook does (§8.2).
ALTER TABLE agent_sessions ADD COLUMN tmux_session TEXT NOT NULL DEFAULT '';
ALTER TABLE agent_sessions ADD COLUMN needs_recap INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE agent_sessions DROP COLUMN needs_recap;
ALTER TABLE agent_sessions DROP COLUMN tmux_session;
