-- name: AddDependency :exec
-- OR IGNORE so recording a dependency that already exists is a no-op
-- rather than a constraint error: SetDependencies re-adds the survivors.
INSERT OR IGNORE INTO task_dependencies (task_id, depends_on_id)
VALUES (?, ?);

-- name: RemoveDependency :exec
DELETE FROM task_dependencies
WHERE task_id = ? AND depends_on_id = ?;

-- name: ClearDependencies :exec
-- Drops everything a task waits on (not what waits on it).
DELETE FROM task_dependencies
WHERE task_id = ?;

-- name: ListBlockers :many
-- The tasks a task waits on, in the order they were recorded.
SELECT t.*
FROM task_dependencies d
JOIN tasks t ON t.id = d.depends_on_id
WHERE d.task_id = ?
ORDER BY d.created_at, t.id;

-- name: ListBlocking :many
-- The reverse: the tasks that wait on this one.
SELECT t.*
FROM task_dependencies d
JOIN tasks t ON t.id = d.task_id
WHERE d.depends_on_id = ?
ORDER BY d.created_at, t.id;

-- name: ListAllDependencies :many
-- Every edge, for the cycle check in Store.AddDependency. The table is
-- as small as the user's own task graph, so walking it in Go is cheaper
-- to reason about than a recursive CTE sqlc cannot parse anyway.
SELECT task_id, depends_on_id
FROM task_dependencies
ORDER BY task_id, depends_on_id;

-- name: ListBlockerCounts :many
-- Batch load for the list view: per task, how many tasks it waits on and
-- how many of those are still open (not done). Same idiom as
-- ListChildCounts -- one query for every visible row, not N+1.
SELECT d.task_id,
       COUNT(*)                                                             AS total,
       CAST(SUM(CASE WHEN b.state <> 'done' THEN 1 ELSE 0 END) AS INTEGER) AS open
FROM task_dependencies d
JOIN tasks b ON b.id = d.depends_on_id
GROUP BY d.task_id;

-- name: ListOpenTasks :many
-- Every task not yet done, in every project and at any depth: the
-- candidate list for the TUI's dependency picker. A done task cannot
-- usefully block anything, so it is left out; someday and snoozed tasks
-- stay in, since "wait for that someday thing" is a real dependency.
SELECT *
FROM tasks
WHERE state <> 'done'
ORDER BY project_id, id;
