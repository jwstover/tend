-- +goose Up
-- Scheduled workflow runs (tend task #645). A schedule is a timer on a
-- workflow run: it binds a workflow to a task (its home) with a cron
-- expression, and the tick creates an ordinary pending run when it is due.
-- Deleting the workflow or the task deletes its schedules; the runs a
-- schedule already created stay, with schedule_id set NULL.
CREATE TABLE schedules (
  id             INTEGER PRIMARY KEY,
  name           TEXT    NOT NULL UNIQUE COLLATE NOCASE,
  workflow_id    INTEGER NOT NULL REFERENCES workflows(id) ON DELETE CASCADE,
  task_id        INTEGER NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
  cwd            TEXT    NOT NULL,
  cron_expr      TEXT    NOT NULL,
  enabled        INTEGER NOT NULL DEFAULT 1,
  overlap_policy TEXT    NOT NULL DEFAULT 'skip' CHECK (overlap_policy IN ('skip')),
  last_run_id    INTEGER REFERENCES workflow_runs(id) ON DELETE SET NULL,
  -- UTC in the datetime('now') layout, so `next_run_at <= ?` compares as text.
  next_run_at    TEXT    NOT NULL,
  created_at     TEXT    NOT NULL DEFAULT (datetime('now')),
  updated_at     TEXT    NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX idx_schedules_due ON schedules(enabled, next_run_at);

-- Which schedule started a run; NULL for a run started by hand.
ALTER TABLE workflow_runs ADD COLUMN schedule_id INTEGER REFERENCES schedules(id) ON DELETE SET NULL;

-- +goose Down
ALTER TABLE workflow_runs DROP COLUMN schedule_id;
DROP INDEX idx_schedules_due;
DROP TABLE schedules;
