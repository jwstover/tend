-- +goose Up
-- Widen task_events.kind to admit 'parent', so a task moving to a
-- different parent (promoted to the top level, demoted under another
-- task, or shifted between parents) is a fact the activity log can hold,
-- the way 'project' is for project moves. SQLite cannot alter a CHECK
-- constraint, so this is the same table rebuild as migration 00008: drop
-- the three triggers on `tasks` first (renaming a table re-parses every
-- trigger, and one naming a table that does not exist at that moment
-- fails the rename), copy, rename, recreate the triggers.
--
-- Safe inside a transaction for the same reason as 00008: nothing
-- references task_events by foreign key and it holds none itself.

CREATE TABLE task_events_new (
  id         INTEGER PRIMARY KEY,
  task_id    INTEGER NOT NULL,
  task_title TEXT    NOT NULL,
  kind       TEXT    NOT NULL CHECK (kind IN ('created', 'state', 'deleted', 'project', 'parent')),
  old_value  TEXT,
  new_value  TEXT,
  created_at TEXT    NOT NULL DEFAULT (datetime('now'))
);

INSERT INTO task_events_new (id, task_id, task_title, kind, old_value, new_value, created_at)
SELECT id, task_id, task_title, kind, old_value, new_value, created_at FROM task_events;

DROP TRIGGER trg_events_task_deleted;
DROP TRIGGER trg_events_task_state;
DROP TRIGGER trg_events_task_created;

DROP TABLE task_events;

ALTER TABLE task_events_new RENAME TO task_events;

CREATE INDEX idx_task_events_created_at ON task_events(created_at);
CREATE INDEX idx_task_events_task_id    ON task_events(task_id);

-- Recreated verbatim from 00002. A parent move is deliberately NOT among
-- them, for the reason 00008 gives for project moves: Store.SetParent
-- may also re-project a whole sub-tree, and the log should hold one
-- entry for the one task the user actually moved. The store writes it.

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
-- Narrowing the CHECK again means the 'parent' rows cannot come back, so
-- they are dropped rather than silently violating the restored constraint.
CREATE TABLE task_events_old (
  id         INTEGER PRIMARY KEY,
  task_id    INTEGER NOT NULL,
  task_title TEXT    NOT NULL,
  kind       TEXT    NOT NULL CHECK (kind IN ('created', 'state', 'deleted', 'project')),
  old_value  TEXT,
  new_value  TEXT,
  created_at TEXT    NOT NULL DEFAULT (datetime('now'))
);

INSERT INTO task_events_old (id, task_id, task_title, kind, old_value, new_value, created_at)
SELECT id, task_id, task_title, kind, old_value, new_value, created_at
FROM task_events WHERE kind <> 'parent';

DROP TRIGGER trg_events_task_deleted;
DROP TRIGGER trg_events_task_state;
DROP TRIGGER trg_events_task_created;

DROP TABLE task_events;

ALTER TABLE task_events_old RENAME TO task_events;

CREATE INDEX idx_task_events_created_at ON task_events(created_at);
CREATE INDEX idx_task_events_task_id    ON task_events(task_id);

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
