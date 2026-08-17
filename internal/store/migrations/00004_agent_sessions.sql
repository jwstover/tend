-- +goose Up
-- Claude Code sessions launched or resumed against a task, so a task
-- carries pointers to its own agent work the same way it carries its
-- body and sub-tasks. Cascades with the task (see docs/agent-sessions-plan.md
-- §2): unlike log_entries/task_events, a session has no reason to survive
-- its task — only tend's pointer to the underlying transcript is lost.
CREATE TABLE agent_sessions (
  id             INTEGER PRIMARY KEY,
  task_id        INTEGER NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
  external_id    TEXT    NOT NULL,        -- the claude --session-id UUID
  cwd            TEXT    NOT NULL,        -- working directory it ran/resumes in
  label          TEXT    NOT NULL,        -- task title, snapshotted at launch
  started_at     TEXT    NOT NULL DEFAULT (datetime('now')),
  last_active_at TEXT    NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX idx_agent_sessions_task_id ON agent_sessions(task_id);

-- +goose Down
DROP INDEX idx_agent_sessions_task_id;
DROP TABLE agent_sessions;
