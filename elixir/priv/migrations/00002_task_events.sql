-- +goose Up
-- Append-only activity log behind `tend standup`. Events are raw facts
-- (state went from X to Y); verbs like "started" are derived at render
-- time. task_id is deliberately not a foreign key and task_title is
-- snapshotted: the log must outlive the tasks it describes.
CREATE TABLE task_events (
  id         INTEGER PRIMARY KEY,
  task_id    INTEGER NOT NULL,
  task_title TEXT    NOT NULL,
  kind       TEXT    NOT NULL CHECK (kind IN ('created', 'state', 'deleted')),
  old_value  TEXT,
  new_value  TEXT,
  created_at TEXT    NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX idx_task_events_created_at ON task_events(created_at);
CREATE INDEX idx_task_events_task_id    ON task_events(task_id);

-- Triggers rather than Go-layer writes: OLD/NEW give the previous state
-- without a read-before-write transaction, capture stays a single
-- statement, and cascade-deleted sub-tasks get events too.

-- +goose StatementBegin
CREATE TRIGGER trg_events_task_created AFTER INSERT ON tasks
BEGIN
  INSERT INTO task_events (task_id, task_title, kind, new_value)
  VALUES (NEW.id, NEW.title, 'created', NEW.state);
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER trg_events_task_state AFTER UPDATE OF state ON tasks
WHEN OLD.state <> NEW.state
BEGIN
  INSERT INTO task_events (task_id, task_title, kind, old_value, new_value)
  VALUES (NEW.id, NEW.title, 'state', OLD.state, NEW.state);
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER trg_events_task_deleted AFTER DELETE ON tasks
BEGIN
  INSERT INTO task_events (task_id, task_title, kind, old_value)
  VALUES (OLD.id, OLD.title, 'deleted', OLD.state);
END;
-- +goose StatementEnd

-- +goose Down
DROP TRIGGER trg_events_task_deleted;
DROP TRIGGER trg_events_task_state;
DROP TRIGGER trg_events_task_created;
DROP INDEX idx_task_events_task_id;
DROP INDEX idx_task_events_created_at;
DROP TABLE task_events;
