-- name: CreateTask :one
INSERT INTO tasks (title, project_id)
VALUES (?, ?)
RETURNING *;

-- name: CreateTaskWithBody :one
INSERT INTO tasks (title, body_md, project_id)
VALUES (?, ?, ?)
RETURNING *;

-- name: CreateChildTask :one
-- The child's project is read off the parent rather than passed in: a
-- sub-task sitting in a different project from its parent is incoherent,
-- and doing it in one statement keeps AddChild a single round trip.
INSERT INTO tasks (title, parent_id, project_id)
SELECT sqlc.arg(title), p.id, p.project_id
FROM tasks p
WHERE p.id = sqlc.arg(parent_id)
RETURNING *;

-- name: GetTask :one
SELECT *
FROM tasks
WHERE id = ?;

-- name: ListLiveTasks :many
SELECT t.*
FROM tasks t
JOIN states s ON s.name = t.state
WHERE s.is_terminal = 0
  AND s.hidden_by_default = 0
  AND (t.snooze_until IS NULL OR t.snooze_until <= date('now'))
  AND (sqlc.narg(project_id) IS NULL OR t.project_id = sqlc.narg(project_id))
ORDER BY s.sort_order, t.priority IS NULL, t.priority, t.id;

-- name: ListLiveWithCompletedTasks :many
-- Like ListLiveTasks but also surfaces completed (done) tasks, for when the
-- list view has the completed section toggled on.
SELECT t.*
FROM tasks t
JOIN states s ON s.name = t.state
WHERE (s.is_terminal = 0 OR t.state = 'done')
  AND s.hidden_by_default = 0
  AND (t.snooze_until IS NULL OR t.snooze_until <= date('now'))
  AND (sqlc.narg(project_id) IS NULL OR t.project_id = sqlc.narg(project_id))
ORDER BY s.sort_order, t.priority IS NULL, t.priority, t.id;

-- name: ListInboxTasks :many
SELECT t.*
FROM tasks t
WHERE t.state = 'inbox'
  AND t.parent_id IS NULL
  AND (sqlc.narg(project_id) IS NULL OR t.project_id = sqlc.narg(project_id))
ORDER BY t.id;

-- name: ListChildTasks :many
SELECT *
FROM tasks
WHERE parent_id = ?
ORDER BY id;

-- name: ListChildCounts :many
SELECT parent_id,
       COUNT(*)                                                   AS total,
       CAST(SUM(CASE WHEN state = 'done' THEN 1 ELSE 0 END) AS INTEGER) AS done
FROM tasks
WHERE parent_id IS NOT NULL
GROUP BY parent_id;

-- name: CountInboxTasks :one
SELECT COUNT(*)
FROM tasks t
WHERE t.state = 'inbox'
  AND t.parent_id IS NULL
  AND (sqlc.narg(project_id) IS NULL OR t.project_id = sqlc.narg(project_id))
;

-- name: SetTaskState :exec
UPDATE tasks
SET state        = sqlc.arg(state),
    completed_at = CASE WHEN sqlc.arg(state) = 'done' THEN datetime('now') ELSE NULL END,
    updated_at   = datetime('now')
WHERE id = sqlc.arg(id);

-- name: SetTasksProject :exec
-- Moving a task moves its whole sub-tree: a child sitting in a different
-- project from its parent is incoherent. The descendant ids are collected
-- in Go (see Store.SetProject) rather than by a recursive CTE, because
-- sqlc v1.31.1 cannot parse WITH RECURSIVE at all -- neither feeding an
-- UPDATE ("relation \"tree\" does not exist") nor a plain SELECT
-- ("*ast.ResTarget has nil name").
UPDATE tasks
SET project_id = sqlc.arg(project_id),
    updated_at = datetime('now')
WHERE id IN (sqlc.slice(ids));

-- name: ListChildIDs :many
-- One level of the sub-tree walk that stands in for the recursive CTE.
SELECT id
FROM tasks
WHERE parent_id = ?;

-- name: SetTaskPriority :exec
UPDATE tasks
SET priority   = ?,
    updated_at = datetime('now')
WHERE id = ?;

-- name: SetTaskDue :exec
UPDATE tasks
SET due        = ?,
    updated_at = datetime('now')
WHERE id = ?;

-- name: SetTaskTitle :exec
UPDATE tasks
SET title      = ?,
    updated_at = datetime('now')
WHERE id = ?;

-- name: SetTaskBody :exec
UPDATE tasks
SET body_md    = ?,
    updated_at = datetime('now')
WHERE id = ?;

-- name: DeleteTask :exec
DELETE FROM tasks
WHERE id = ?;
