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

-- name: DueSchedules :many
SELECT * FROM schedules
WHERE enabled = 1 AND next_run_at <= ?
ORDER BY next_run_at, id;

-- name: AdvanceSchedule :execrows
UPDATE schedules
SET next_run_at = ?, updated_at = datetime('now')
WHERE id = ? AND enabled = 1 AND next_run_at = ?;

-- name: CreateScheduledRun :one
INSERT INTO workflow_runs (workflow_id, task_id, cwd, schedule_id)
VALUES (?, ?, ?, ?)
RETURNING *;

-- name: SetScheduleLastRun :exec
UPDATE schedules SET last_run_id = ?, updated_at = datetime('now') WHERE id = ?;

-- name: GetScheduleLastRunForTx :one
SELECT r.* FROM schedules s
JOIN workflow_runs r ON r.id = s.last_run_id
WHERE s.id = ?;

-- name: GetPreviousScheduledRun :one
SELECT * FROM workflow_runs
WHERE schedule_id = ? AND id < ?
ORDER BY id DESC
LIMIT 1;

-- name: GetLastFinishedStepDeliverable :one
SELECT deliverable FROM workflow_step_runs
WHERE run_id = ? AND ended_at IS NOT NULL
ORDER BY id DESC
LIMIT 1;

-- name: GetRunScheduleID :one
SELECT schedule_id FROM workflow_runs WHERE id = ?;
