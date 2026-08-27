-- name: CreateProject :one
INSERT INTO projects (name)
VALUES (?)
RETURNING *;

-- name: GetProject :one
SELECT *
FROM projects
WHERE id = ?;

-- name: GetProjectByName :one
SELECT *
FROM projects
WHERE name = ?;

-- name: ListProjects :many
-- live_count is live TOP-LEVEL tasks, matching the population the list view
-- renders as rows, so the number beside a project is what selecting it
-- produces, not a larger figure that counts sub-tasks the list hides.
SELECT p.*, COALESCE(c.live, 0) AS live_count
FROM projects p
LEFT JOIN (
  SELECT t.project_id AS pid, COUNT(*) AS live
  FROM tasks t
  JOIN states s ON s.name = t.state
  WHERE s.is_terminal = 0
    AND s.hidden_by_default = 0
    AND t.parent_id IS NULL
    AND (t.snooze_until IS NULL OR t.snooze_until <= date('now'))
  GROUP BY t.project_id
) c ON c.pid = p.id
ORDER BY p.sort_order, p.name;

-- name: RenameProject :exec
UPDATE projects
SET name       = ?,
    updated_at = datetime('now')
WHERE id = ?;

-- name: SetProjectArchived :exec
UPDATE projects
SET archived_at = ?,
    updated_at  = datetime('now')
WHERE id = ?;

-- name: DeleteProject :exec
DELETE FROM projects
WHERE id = ?;

-- name: ReassignProjectTasks :exec
-- Half of Store.DeleteProject's transaction: project_id carries no foreign
-- key (see 00007's comment), so orphan prevention is explicit here.
UPDATE tasks
SET project_id = sqlc.arg(to_project_id),
    updated_at = datetime('now')
WHERE project_id = sqlc.arg(from_project_id);
