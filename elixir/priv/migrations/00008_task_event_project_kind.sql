-- +goose Up
-- Widen task_events.kind to admit 'project', so a task moving between
-- projects is a fact the activity log can hold (docs/projects-plan.md
-- Phase 4). SQLite cannot alter a CHECK constraint, so this is a table
-- rebuild.
--
-- Unlike the rebuild rejected in migration 00007, this one is safe inside
-- a transaction: nothing references task_events by foreign key and it
-- holds none itself, so foreign_keys can stay on and no cascade fires.
--
-- The ordering below matters, and not obviously. Renaming a table makes
-- SQLite re-parse every trigger in the schema to fix up references; a
-- trigger whose body names a table that does not exist at that moment
-- makes the rename fail outright. The three triggers on `tasks` insert
-- into task_events, so they have to be dropped before the old table goes
-- and recreated after the new one takes its name.

CREATE TABLE task_events_new (
  id         INTEGER PRIMARY KEY,
  task_id    INTEGER NOT NULL,
  task_title TEXT    NOT NULL,
  kind       TEXT    NOT NULL CHECK (kind IN ('created', 'state', 'deleted', 'project')),
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

-- Recreated verbatim from 00002. A project move is deliberately NOT among
-- them: a trigger fires once per row, and Store.SetProject moves a whole
-- sub-tree, so the log would fill with one entry per descendant for a
-- single user action. The store writes that event itself, for the one
-- task the user actually moved.

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
-- Narrowing the CHECK again means the 'project' rows cannot come back, so
-- they are dropped rather than silently violating the restored constraint.
CREATE TABLE task_events_old (
  id         INTEGER PRIMARY KEY,
  task_id    INTEGER NOT NULL,
  task_title TEXT    NOT NULL,
  kind       TEXT    NOT NULL CHECK (kind IN ('created', 'state', 'deleted')),
  old_value  TEXT,
  new_value  TEXT,
  created_at TEXT    NOT NULL DEFAULT (datetime('now'))
);

INSERT INTO task_events_old (id, task_id, task_title, kind, old_value, new_value, created_at)
SELECT id, task_id, task_title, kind, old_value, new_value, created_at
FROM task_events WHERE kind <> 'project';

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
