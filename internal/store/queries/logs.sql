-- name: CreateLogEntry :one
INSERT INTO log_entries (task_id, body)
VALUES (?, ?)
RETURNING *;

-- name: ListLogEntriesForTask :many
SELECT *
FROM log_entries
WHERE task_id = ?
ORDER BY created_at DESC, id DESC;

-- name: ListLogEntriesBetween :many
-- The task title comes along for display; COALESCE keeps the column
-- non-null when the note is freestanding or its task was deleted.
SELECT le.id, le.task_id, le.body, le.created_at,
       COALESCE(t.title, '') AS task_title
FROM log_entries le
LEFT JOIN tasks t ON t.id = le.task_id
WHERE le.created_at >= sqlc.arg(start_at)
  AND le.created_at <= sqlc.arg(end_at)
ORDER BY le.created_at, le.id;
