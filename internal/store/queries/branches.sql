-- name: LinkBranch :exec
-- OR IGNORE so linking a branch that is already linked is a no-op; the
-- store reads the row back either way.
INSERT OR IGNORE INTO task_branches (task_id, repo_path, branch)
VALUES (?, ?, ?);

-- name: GetTaskBranch :one
SELECT *
FROM task_branches
WHERE task_id = ? AND repo_path = ? AND branch = ?;

-- name: UnlinkBranch :exec
DELETE FROM task_branches
WHERE task_id = ? AND repo_path = ? AND branch = ?;

-- name: ListTaskBranches :many
-- Oldest link first.
SELECT *
FROM task_branches
WHERE task_id = ?
ORDER BY created_at, id;

-- name: RelinkBranch :execrows
UPDATE task_branches
SET branch = ?
WHERE id = ?;
