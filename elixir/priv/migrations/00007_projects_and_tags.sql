-- +goose Up
-- Projects become a first-class grouping record and the old flat
-- `tasks.project` string becomes what it always was — a tag. See
-- docs/projects-plan.md §2.
--
-- A project is a workspace: every task belongs to exactly one, and the
-- TUI's third column scopes the task list to it. Tags stay a labelling
-- mechanism and gain multi-value support, which the single TEXT column
-- could never express.
CREATE TABLE projects (
  id          INTEGER PRIMARY KEY,
  name        TEXT NOT NULL UNIQUE COLLATE NOCASE,
  sort_order  INTEGER NOT NULL DEFAULT 0,
  archived_at TEXT,                                  -- non-null hides it from the projects column
  created_at  TEXT NOT NULL DEFAULT (datetime('now')),
  updated_at  TEXT NOT NULL DEFAULT (datetime('now'))
);

-- Row 1 is the default capture target and the fallback every delete path
-- reassigns to, so it is seeded here rather than created by the app and
-- must never be deleted (Store.DeleteProject refuses).
--
-- sort_order -1 pins it above every user-created project (which take the
-- column default, 0, and then sort by name). It is the bucket everything
-- lands in and falls back to, so it belongs next to the All row rather
-- than floating around wherever the alphabet puts it.
INSERT INTO projects (id, name, sort_order) VALUES (1, 'Unsorted', -1);

CREATE TABLE tags (
  id   INTEGER PRIMARY KEY,
  name TEXT NOT NULL UNIQUE COLLATE NOCASE
);

CREATE TABLE task_tags (
  task_id INTEGER NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
  tag_id  INTEGER NOT NULL REFERENCES tags(id)  ON DELETE CASCADE,
  PRIMARY KEY (task_id, tag_id)
);

CREATE INDEX idx_task_tags_tag ON task_tags(tag_id);

-- Key/value bag for state that must outlive a TUI process. First tenant
-- is active_project_id (docs/projects-plan.md §3): `tend add` running in a
-- bare shell has no TUI selection to read, so the selection is persisted
-- here. showCompleted and the standup toggles are obvious later tenants.
CREATE TABLE settings (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
);

-- No REFERENCES clause, deliberately. SQLite refuses to add a REFERENCES
-- column with a non-NULL default while foreign keys are on, and they are
-- (see the DSN in store.Open):
--
--   ALTER TABLE tasks ADD COLUMN pid INTEGER NOT NULL DEFAULT 1 REFERENCES projects(id);
--   Runtime error: Cannot add a REFERENCES column with non-NULL default value
--
-- The alternatives were a nullable FK (which abandons the "every task has
-- a project" invariant the whole feature rests on) or a 12-step table
-- rebuild (which needs PRAGMA foreign_keys=OFF, impossible inside a
-- transaction, and whose intermediate DROP TABLE tasks would cascade-
-- delete every agent_sessions row). Referential integrity for the one
-- operation that can break it — deleting a project — lives in
-- Store.DeleteProject, which reassigns that project's tasks to Unsorted
-- inside a transaction before deleting the row. Precedent: task_events.
-- task_id and agent_sessions.status are both deliberately un-keyed.
--
-- DEFAULT 1 also keeps `INSERT INTO tasks (title) VALUES (?)` valid, so
-- the hot capture path in CreateTask needs no change.
ALTER TABLE tasks ADD COLUMN project_id INTEGER NOT NULL DEFAULT 1;

CREATE INDEX idx_tasks_project ON tasks(project_id);

-- Old flat project strings become tags attached to the same tasks. OR
-- IGNORE on both inserts because the NOCASE collation folds 'Home' and
-- 'home' into one tag, so DISTINCT can still yield colliding names.
INSERT OR IGNORE INTO tags (name)
SELECT DISTINCT trim(project) FROM tasks
WHERE project IS NOT NULL AND trim(project) <> '';

INSERT OR IGNORE INTO task_tags (task_id, tag_id)
SELECT t.id, g.id
FROM tasks t JOIN tags g ON g.name = trim(t.project)
WHERE t.project IS NOT NULL AND trim(t.project) <> '';

-- Every task keeps project_id = 1 (Unsorted); the project list is built by
-- hand from there rather than inferred from the old strings.
ALTER TABLE tasks DROP COLUMN project;

-- +goose Down
-- Lossy by definition: a task carrying several tags gets only its first
-- (alphabetically) back into the restored single-value column.
ALTER TABLE tasks ADD COLUMN project TEXT;

UPDATE tasks
SET project = (
  SELECT g.name
  FROM task_tags tt JOIN tags g ON g.id = tt.tag_id
  WHERE tt.task_id = tasks.id
  ORDER BY g.name
  LIMIT 1
);

DROP INDEX idx_tasks_project;
ALTER TABLE tasks DROP COLUMN project_id;

DROP TABLE settings;
DROP INDEX idx_task_tags_tag;
DROP TABLE task_tags;
DROP TABLE tags;
DROP TABLE projects;
