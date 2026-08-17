-- name: CreateSession :one
INSERT INTO agent_sessions (task_id, external_id, cwd, label)
VALUES (?, ?, ?, ?)
RETURNING *;

-- name: ListSessionsForTask :many
SELECT *
FROM agent_sessions
WHERE task_id = ?
ORDER BY last_active_at DESC, id DESC;

-- name: TouchSession :exec
UPDATE agent_sessions
SET last_active_at = datetime('now')
WHERE id = ?;
