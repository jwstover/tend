-- name: CreateWorkflow :one
INSERT INTO workflows (name, description)
VALUES (?, ?)
RETURNING *;

-- name: GetWorkflow :one
SELECT *
FROM workflows
WHERE id = ?;

-- name: GetWorkflowByName :one
SELECT *
FROM workflows
WHERE name = ?;

-- name: ListWorkflows :many
-- Comments in this file stay ASCII-only: sqlc v1.31.1 corrupts any query
-- that uses alias.* when its comment block contains non-ASCII (a
-- byte-vs-rune offset bug in star expansion; one em dash yields SELECp).
-- Columns are enumerated rather than written as w.* so this query
-- survives a non-ASCII comment slipping in.
SELECT w.id, w.name, w.description, w.created_at, w.updated_at,
       COALESCE(c.n, 0) AS step_count
FROM workflows w
LEFT JOIN (
  SELECT workflow_id, COUNT(*) AS n
  FROM workflow_steps
  GROUP BY workflow_id
) c ON c.workflow_id = w.id
ORDER BY w.name;

-- name: RenameWorkflow :exec
UPDATE workflows
SET name       = ?,
    updated_at = datetime('now')
WHERE id = ?;

-- name: SetWorkflowDescription :exec
UPDATE workflows
SET description = ?,
    updated_at  = datetime('now')
WHERE id = ?;

-- name: DeleteWorkflow :exec
DELETE FROM workflows
WHERE id = ?;

-- name: ListActiveRunIDsForWorkflow :many
-- Half of Store.DeleteWorkflow: a run in a non-terminal state pins its
-- definition in place.
SELECT id
FROM workflow_runs
WHERE workflow_id = ?
  AND state NOT IN ('done', 'failed', 'cancelled')
ORDER BY id;

-- name: ListActiveRunIDsForStep :many
-- Half of Store.DeleteStep: a step that has run (or is running) within a
-- run in a non-terminal state cannot be removed under it.
SELECT DISTINCT r.id
FROM workflow_step_runs sr
JOIN workflow_runs r ON r.id = sr.run_id
WHERE sr.step_id = ?
  AND r.state NOT IN ('done', 'failed', 'cancelled')
ORDER BY r.id;

-- name: CreateStep :one
-- Appends: the new step sorts after every existing step of the workflow.
INSERT INTO workflow_steps (workflow_id, name, kind, sort_order)
VALUES (
  sqlc.arg(workflow_id),
  sqlc.arg(name),
  sqlc.arg(kind),
  COALESCE((SELECT MAX(sort_order) + 1 FROM workflow_steps WHERE workflow_id = sqlc.arg(workflow_id)), 0)
)
RETURNING *;

-- name: CreateStepFull :one
-- Used by Store.DuplicateWorkflow to copy a step wholesale, sort_order
-- included.
INSERT INTO workflow_steps (workflow_id, name, kind, prompt_md, model, permission_mode, sort_order)
VALUES (?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetStep :one
SELECT *
FROM workflow_steps
WHERE id = ?;

-- name: ListSteps :many
SELECT *
FROM workflow_steps
WHERE workflow_id = ?
ORDER BY sort_order, id;

-- name: UpdateStep :exec
UPDATE workflow_steps
SET name            = ?,
    kind            = ?,
    prompt_md       = ?,
    model           = ?,
    permission_mode = ?,
    updated_at      = datetime('now')
WHERE id = ?;

-- name: SetStepPrompt :exec
UPDATE workflow_steps
SET prompt_md  = ?,
    updated_at = datetime('now')
WHERE id = ?;

-- name: SetStepKind :exec
UPDATE workflow_steps
SET kind       = ?,
    updated_at = datetime('now')
WHERE id = ?;

-- name: SetStepModel :exec
UPDATE workflow_steps
SET model      = ?,
    updated_at = datetime('now')
WHERE id = ?;

-- name: SetStepPermissionMode :exec
UPDATE workflow_steps
SET permission_mode = ?,
    updated_at      = datetime('now')
WHERE id = ?;

-- name: SetStepSortOrder :exec
UPDATE workflow_steps
SET sort_order = ?,
    updated_at = datetime('now')
WHERE id = ? AND workflow_id = ?;

-- name: DeleteStep :exec
DELETE FROM workflow_steps
WHERE id = ?;

-- name: UpsertEdge :one
-- One edge per (from_step, outcome): re-adding an outcome re-routes it.
-- DO UPDATE rather than DO NOTHING so RETURNING always yields the row.
INSERT INTO workflow_edges (from_step_id, outcome, to_step_id, max_iterations)
VALUES (?, ?, ?, ?)
ON CONFLICT(from_step_id, outcome) DO UPDATE
SET to_step_id     = excluded.to_step_id,
    max_iterations = excluded.max_iterations
RETURNING *;

-- name: ListEdgesForWorkflow :many
SELECT e.id, e.from_step_id, e.outcome, e.to_step_id, e.max_iterations
FROM workflow_edges e
JOIN workflow_steps s ON s.id = e.from_step_id
WHERE s.workflow_id = ?
ORDER BY s.sort_order, s.id, e.outcome;

-- name: ListEdgesFromStep :many
SELECT *
FROM workflow_edges
WHERE from_step_id = ?
ORDER BY outcome;

-- name: DeleteEdge :exec
DELETE FROM workflow_edges
WHERE id = ?;

-- name: CreateRun :one
INSERT INTO workflow_runs (workflow_id, task_id, cwd)
VALUES (?, ?, ?)
RETURNING *;

-- name: GetRun :one
SELECT *
FROM workflow_runs
WHERE id = ?;

-- name: ListRunsForTask :many
SELECT *
FROM workflow_runs
WHERE task_id = ?
ORDER BY started_at DESC, id DESC;

-- name: ListActiveRuns :many
SELECT *
FROM workflow_runs
WHERE state NOT IN ('done', 'failed', 'cancelled')
ORDER BY started_at DESC, id DESC;

-- name: SetRunState :execrows
-- Terminal is final: the WHERE refuses to move a run that has already
-- ended, and the caller turns zero rows into ErrRunEnded. ended_at is
-- computed by the caller (now for a terminal state, NULL otherwise)
-- because sqlc v1.31.1 leaves a named arg inside a CASE unrewritten.
UPDATE workflow_runs
SET state    = ?,
    ended_at = ?
WHERE id = ?
  AND state NOT IN ('done', 'failed', 'cancelled');

-- name: ClaimRun :execrows
-- Compare-and-swap for starting a runner: only a pending or paused run can
-- be taken to running, so two runners racing for one run see exactly one
-- success. Same idiom as ClaimSessionRecap.
UPDATE workflow_runs
SET state = 'running'
WHERE id = ? AND state IN ('pending', 'paused');

-- name: SetRunTmuxSession :exec
UPDATE workflow_runs
SET tmux_session = ?
WHERE id = ?;

-- name: SetRunCurrentStepRun :exec
UPDATE workflow_runs
SET current_step_run_id = ?
WHERE id = ?;

-- name: CreateStepRun :one
-- iteration is derived here rather than passed in, so a runner can never
-- miscount: it is one more than the number of times this step has already
-- run within this run.
INSERT INTO workflow_step_runs (run_id, step_id, iteration, session_external_id, prompt_rendered, model, permission_mode, input)
VALUES (
  sqlc.arg(run_id),
  sqlc.arg(step_id),
  (SELECT COUNT(*) + 1 FROM workflow_step_runs WHERE run_id = sqlc.arg(run_id) AND step_id = sqlc.arg(step_id)),
  sqlc.arg(session_external_id),
  sqlc.arg(prompt_rendered),
  sqlc.arg(model),
  sqlc.arg(permission_mode),
  sqlc.arg(input)
)
RETURNING *;

-- name: GetStepRun :one
SELECT *
FROM workflow_step_runs
WHERE id = ?;

-- name: ListStepRunsForRun :many
SELECT *
FROM workflow_step_runs
WHERE run_id = ?
ORDER BY started_at, id;

-- name: FinishStepRun :execrows
-- One-shot handoff: only an unfinished step run takes an outcome, so a
-- second finish_step call affects zero rows and the first outcome stands.
UPDATE workflow_step_runs
SET outcome     = ?,
    deliverable = ?,
    ended_at    = datetime('now')
WHERE id = ? AND ended_at IS NULL;

-- name: SetStepRunLogPath :exec
UPDATE workflow_step_runs
SET log_path = ?
WHERE id = ?;

-- name: SetStepRunSession :exec
UPDATE workflow_step_runs
SET session_external_id = ?
WHERE id = ?;
