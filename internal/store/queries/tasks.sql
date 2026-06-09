-- name: CreateTask :one
INSERT INTO tasks (title)
VALUES (?)
RETURNING *;

-- name: CreateChildTask :one
INSERT INTO tasks (title, parent_id)
VALUES (?, ?)
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
ORDER BY s.sort_order, t.priority IS NULL, t.priority, t.id;

-- name: ListInboxTasks :many
SELECT *
FROM tasks
WHERE state = 'inbox'
ORDER BY id;

-- name: ListChildTasks :many
SELECT *
FROM tasks
WHERE parent_id = ?
ORDER BY id;

-- name: SetTaskState :exec
UPDATE tasks
SET state        = sqlc.arg(state),
    completed_at = CASE WHEN sqlc.arg(state) = 'done' THEN datetime('now') ELSE NULL END,
    updated_at   = datetime('now')
WHERE id = sqlc.arg(id);

-- name: SetTaskProject :exec
UPDATE tasks
SET project    = ?,
    updated_at = datetime('now')
WHERE id = ?;

-- name: SetTaskDue :exec
UPDATE tasks
SET due        = ?,
    updated_at = datetime('now')
WHERE id = ?;

-- name: SetTaskBody :exec
UPDATE tasks
SET body_md    = ?,
    updated_at = datetime('now')
WHERE id = ?;
