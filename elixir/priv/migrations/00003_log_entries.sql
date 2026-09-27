-- +goose Up
-- Manual standup notes: quick free-form entries captured from the TUI
-- (N / n in the standup view, U on a task) or `tend log`. task_id is
-- optional context, not a foreign key — like task_events, notes must
-- outlive the tasks they mention.
CREATE TABLE log_entries (
  id         INTEGER PRIMARY KEY,
  task_id    INTEGER,
  body       TEXT    NOT NULL,
  created_at TEXT    NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX idx_log_entries_created_at ON log_entries(created_at);

-- +goose Down
DROP INDEX idx_log_entries_created_at;
DROP TABLE log_entries;
