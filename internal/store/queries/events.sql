-- name: ListEventsBetween :many
SELECT *
FROM task_events
WHERE created_at >= sqlc.arg(start_at)
  AND created_at <= sqlc.arg(end_at)
ORDER BY created_at, id;

-- name: CreateTaskEvent :exec
-- The one event the store writes itself rather than leaving to a trigger.
-- A project move applies to a whole sub-tree, so a per-row trigger would
-- log one entry per descendant; only the task the user acted on belongs
-- in the log.
INSERT INTO task_events (task_id, task_title, kind, old_value, new_value)
VALUES (?, ?, ?, ?, ?);
