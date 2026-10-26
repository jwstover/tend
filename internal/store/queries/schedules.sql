-- name: CreateSchedule :one
INSERT INTO schedules (name, workflow_id, task_id, cwd, cron_expr, next_run_at)
VALUES (?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetSchedule :one
SELECT * FROM schedules WHERE id = ?;

-- name: GetScheduleByName :one
SELECT * FROM schedules WHERE name = ?;

-- name: ListSchedules :many
SELECT * FROM schedules ORDER BY name;

-- name: UpdateSchedule :one
UPDATE schedules
SET name = ?, cwd = ?, cron_expr = ?, enabled = ?, next_run_at = ?,
    updated_at = datetime('now')
WHERE id = ?
RETURNING *;

-- name: SetScheduleEnabled :execrows
UPDATE schedules
SET enabled = ?, next_run_at = ?, updated_at = datetime('now')
WHERE id = ?;

-- name: DeleteSchedule :execrows
DELETE FROM schedules WHERE id = ?;
