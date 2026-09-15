-- +goose Up
-- Dependencies between tasks (tend task #230): "this task cannot be
-- worked on until that one is done". A row says task_id is blocked by
-- depends_on_id. It is a plain many-to-many like task_tags rather than a
-- column on tasks, because a task can wait on several others and several
-- tasks can wait on one.
--
-- Both sides cascade: a dependency is a fact about two live tasks, and
-- when either goes the fact goes with it (a deleted blocker does not keep
-- blocking anything). Self-dependency is refused by the CHECK; longer
-- cycles are refused in Go by Store.AddDependency, for the same sqlc
-- reason Store.SetParent walks the tree itself (no recursive CTE).
--
-- The dependency is deliberately NOT tied to the `blocked` workflow
-- state. The state is what the user says about the task; the rows here
-- are what the graph says, and the TUI derives "waiting on N" from them
-- so the two can disagree without either being wrong.
CREATE TABLE task_dependencies (
  task_id       INTEGER NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
  depends_on_id INTEGER NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
  created_at    TEXT    NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (task_id, depends_on_id),
  CHECK (task_id <> depends_on_id)
);

-- The primary key already serves "what does this task wait on"; the
-- reverse lookup ("what does this task hold up") needs its own index.
CREATE INDEX idx_task_dependencies_depends_on ON task_dependencies(depends_on_id);

-- +goose Down
DROP INDEX idx_task_dependencies_depends_on;
DROP TABLE task_dependencies;
