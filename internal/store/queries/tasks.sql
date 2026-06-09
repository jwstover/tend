-- name: CreateTask :one
INSERT INTO tasks (title)
VALUES (?)
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
