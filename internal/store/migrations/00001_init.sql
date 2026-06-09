-- +goose Up
CREATE TABLE states (
  name              TEXT PRIMARY KEY,
  sort_order        INTEGER NOT NULL,
  is_terminal       INTEGER NOT NULL DEFAULT 0,
  hidden_by_default INTEGER NOT NULL DEFAULT 0
);

INSERT INTO states (name, sort_order, is_terminal, hidden_by_default) VALUES
  ('inbox',   0, 0, 0),
  ('todo',    1, 0, 0),
  ('doing',   2, 0, 0),
  ('blocked', 3, 0, 0),
  ('done',    4, 1, 0),
  ('someday', 5, 0, 1);

CREATE TABLE tasks (
  id           INTEGER PRIMARY KEY,
  title        TEXT NOT NULL,
  body_md      TEXT NOT NULL DEFAULT '',
  state        TEXT NOT NULL DEFAULT 'inbox' REFERENCES states(name),
  parent_id    INTEGER REFERENCES tasks(id) ON DELETE CASCADE,
  project      TEXT,
  priority     INTEGER,
  due          TEXT,
  snooze_until TEXT,
  created_at   TEXT NOT NULL DEFAULT (datetime('now')),
  updated_at   TEXT NOT NULL DEFAULT (datetime('now')),
  completed_at TEXT
);

CREATE INDEX idx_tasks_state  ON tasks(state);
CREATE INDEX idx_tasks_parent ON tasks(parent_id);

-- +goose Down
DROP INDEX idx_tasks_parent;
DROP INDEX idx_tasks_state;
DROP TABLE tasks;
DROP TABLE states;
