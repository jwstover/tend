-- name: ListEventsBetween :many
SELECT *
FROM task_events
WHERE created_at >= sqlc.arg(start_at)
  AND created_at <= sqlc.arg(end_at)
ORDER BY created_at, id;
