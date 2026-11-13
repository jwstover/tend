-- name: CreateTask :one
INSERT INTO tasks (title, project_id)
VALUES (?, ?)
RETURNING *;

-- name: CreateTaskWithBody :one
INSERT INTO tasks (title, body_md, project_id)
VALUES (?, ?, ?)
RETURNING *;

-- name: CreateChildTask :one
-- The child's project is read off the parent rather than passed in: a
-- sub-task sitting in a different project from its parent is incoherent,
-- and doing it in one statement keeps AddChild a single round trip.
INSERT INTO tasks (title, parent_id, project_id)
SELECT sqlc.arg(title), p.id, p.project_id
FROM tasks p
WHERE p.id = sqlc.arg(parent_id)
RETURNING *;

-- name: GetTask :one
SELECT *
FROM tasks
WHERE id = ?;

-- name: ListLiveTasks :many
SELECT t.*
FROM tasks t
JOIN states s ON s.name = t.state
WHERE s.is_terminal = 0
  AND s.hidden_by_default = 0
  AND (t.snooze_until IS NULL OR t.snooze_until <= date('now'))
  AND (sqlc.narg(project_id) IS NULL OR t.project_id = sqlc.narg(project_id))
ORDER BY s.sort_order, t.priority IS NULL, t.priority, t.id;

-- name: ListLiveWithCompletedTasks :many
-- Like ListLiveTasks but also surfaces completed (done) tasks, canceled tasks and
-- hidden-by-default states (someday), for when the list view has the
-- completed section toggled on. Snoozed tasks stay hidden either way.
SELECT t.*
FROM tasks t
JOIN states s ON s.name = t.state
WHERE (s.is_terminal = 0 OR t.state = 'done' OR s.hidden_by_default = 1)
  AND (t.snooze_until IS NULL OR t.snooze_until <= date('now'))
  AND (sqlc.narg(project_id) IS NULL OR t.project_id = sqlc.narg(project_id))
ORDER BY s.sort_order, t.priority IS NULL, t.priority, t.id;

-- name: ListInboxTasks :many
SELECT t.*
FROM tasks t
WHERE t.state = 'inbox'
  AND t.parent_id IS NULL
  AND (sqlc.narg(project_id) IS NULL OR t.project_id = sqlc.narg(project_id))
ORDER BY t.id;

-- name: ListChildTasks :many
SELECT *
FROM tasks
WHERE parent_id = ?
ORDER BY id;

-- name: ListChildCounts :many
SELECT parent_id,
       COUNT(*)                                                   AS total,
       CAST(SUM(CASE WHEN state = 'done' THEN 1 ELSE 0 END) AS INTEGER) AS done
FROM tasks
WHERE parent_id IS NOT NULL
GROUP BY parent_id;

-- name: CountInboxTasks :one
SELECT COUNT(*)
FROM tasks t
WHERE t.state = 'inbox'
  AND t.parent_id IS NULL
  AND (sqlc.narg(project_id) IS NULL OR t.project_id = sqlc.narg(project_id))
;

-- name: SetTaskState :exec
UPDATE tasks
SET state        = sqlc.arg(state),
    completed_at = CASE WHEN sqlc.arg(state) = 'done' OR sqlc.arg(state) = 'canceled' THEN datetime('now') ELSE NULL END,
    updated_at   = datetime('now')
WHERE id = sqlc.arg(id);

-- name: SetTasksProject :exec
-- Moving a task moves its whole sub-tree: a child sitting in a different
-- project from its parent is incoherent. The descendant ids are collected
-- in Go (see Store.SetProject) rather than by a recursive CTE, because
-- sqlc v1.31.1 cannot parse WITH RECURSIVE at all -- neither feeding an
-- UPDATE ("relation \"tree\" does not exist") nor a plain SELECT
-- ("*ast.ResTarget has nil name").
UPDATE tasks
SET project_id = sqlc.arg(project_id),
    updated_at = datetime('now')
WHERE id IN (sqlc.slice(ids));

-- name: ListChildIDs :many
-- One level of the sub-tree walk that stands in for the recursive CTE.
SELECT id
FROM tasks
WHERE parent_id = ?;

-- name: SetTaskParent :exec
-- Reparents one task; NULL promotes it to the top level. Cycle checks
-- and the sub-tree's project follow-along live in Store.SetParent, for
-- the same sqlc reason as SetTasksProject.
UPDATE tasks
SET parent_id  = sqlc.narg(parent_id),
    updated_at = datetime('now')
WHERE id = sqlc.arg(id);

-- name: ListProjectTasks :many
-- Every task in a project at any depth and in any state: the candidate
-- list for the TUI's move-to-parent picker, which the scoped live list
-- and per-branch child cache cannot supply.
SELECT *
FROM tasks
WHERE project_id = ?
ORDER BY id;

-- name: SetTaskPriority :exec
UPDATE tasks
SET priority   = ?,
    updated_at = datetime('now')
WHERE id = ?;

-- name: SetTaskDue :exec
UPDATE tasks
SET due        = ?,
    updated_at = datetime('now')
WHERE id = ?;

-- name: SetTaskTitle :exec
UPDATE tasks
SET title      = ?,
    updated_at = datetime('now')
WHERE id = ?;

-- name: SetTaskBody :exec
UPDATE tasks
SET body_md    = ?,
    updated_at = datetime('now')
WHERE id = ?;

-- name: SetTaskBodyIfUnchanged :execrows
-- Replaces the body only while it is still the one the caller read, so a
-- save never wipes out a change made since (an agent's append_task_body).
UPDATE tasks
SET body_md    = sqlc.arg(body),
    updated_at = datetime('now')
WHERE id = sqlc.arg(id) AND body_md = sqlc.arg(old_body);

-- name: AppendTaskBody :exec
-- Appends text as a new paragraph: an empty body just becomes the text,
-- otherwise trailing whitespace is trimmed and a blank line separates the
-- old body from the new text. Done in SQL so the append is atomic.
UPDATE tasks
SET body_md    = CASE
                   WHEN trim(body_md, ' ' || char(9, 10, 13)) = '' THEN sqlc.arg(text)
                   ELSE rtrim(body_md, ' ' || char(9, 10, 13)) || char(10, 10) || sqlc.arg(text)
                 END,
    updated_at = datetime('now')
WHERE id = sqlc.arg(id);

-- name: DeleteTask :exec
DELETE FROM tasks
WHERE id = ?;

-- name: SearchTasks :many
-- Global search: every task in every project, whatever its state (done,
-- canceled, someday and snoozed included), whose title, body or a tag
-- contains the terms, case-insensitively. Up to three terms (an unused
-- slot is ''), and a task must match all of them; each term may hit any
-- of the three fields. instr, not LIKE, so a typed % or _ is literal.
-- Title hits sort first, then newest.
SELECT t.*
FROM tasks t
WHERE (instr(lower(t.title), lower(sqlc.arg(term1))) > 0 OR instr(lower(t.body_md), lower(sqlc.arg(term1))) > 0
       OR EXISTS (SELECT 1 FROM task_tags tt JOIN tags g ON g.id = tt.tag_id
                  WHERE tt.task_id = t.id AND instr(lower(g.name), lower(sqlc.arg(term1))) > 0))
  AND (instr(lower(t.title), lower(sqlc.arg(term2))) > 0 OR instr(lower(t.body_md), lower(sqlc.arg(term2))) > 0
       OR EXISTS (SELECT 1 FROM task_tags tt JOIN tags g ON g.id = tt.tag_id
                  WHERE tt.task_id = t.id AND instr(lower(g.name), lower(sqlc.arg(term2))) > 0))
  AND (instr(lower(t.title), lower(sqlc.arg(term3))) > 0 OR instr(lower(t.body_md), lower(sqlc.arg(term3))) > 0
       OR EXISTS (SELECT 1 FROM task_tags tt JOIN tags g ON g.id = tt.tag_id
                  WHERE tt.task_id = t.id AND instr(lower(g.name), lower(sqlc.arg(term3))) > 0))
ORDER BY instr(lower(t.title), lower(?1)) > 0 DESC, t.updated_at DESC, t.id DESC
LIMIT sqlc.arg(max_rows);
