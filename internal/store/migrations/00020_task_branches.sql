-- +goose Up
-- Explicit links between a task and the branches its work lives on (tend
-- task #792, part of the diff viewer #790). This is the only source the
-- diff views trust: there is deliberately no branch-name detection.
--
-- Cascades with the task, like agent_sessions: a link has no reason to
-- outlive the task it describes.
--
-- TODO(owner): a merged branch's diff stays viewable later only if the
-- merge-base and head SHAs are stored; leave them out until a view needs it.
CREATE TABLE task_branches (
  id         INTEGER PRIMARY KEY,
  task_id    INTEGER NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
  repo_path  TEXT NOT NULL,   -- absolute path to the repo (or worktree) the branch lives in
  branch     TEXT NOT NULL,   -- branch name, e.g. task-645/sub-653-systemd-timer
  created_at TEXT NOT NULL DEFAULT (datetime('now')),
  UNIQUE (task_id, repo_path, branch)
);

CREATE INDEX task_branches_task ON task_branches(task_id);

-- +goose Down
DROP INDEX task_branches_task;
DROP TABLE task_branches;
