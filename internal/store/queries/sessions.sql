-- name: CreateSession :one
INSERT INTO agent_sessions (task_id, external_id, cwd, label, tmux_session)
VALUES (?, ?, ?, ?, ?)
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

-- name: UpdateSessionLabel :exec
UPDATE agent_sessions
SET label = ?
WHERE external_id = ?;

-- name: SetSessionNeedsRecap :exec
UPDATE agent_sessions
SET needs_recap = ?
WHERE external_id = ?;

-- name: SetSessionStatus :exec
UPDATE agent_sessions
SET status = ?, status_updated_at = datetime('now'), last_active_at = datetime('now')
WHERE external_id = ?;

-- name: ListSessionsNeedingRecap :many
SELECT *
FROM agent_sessions
WHERE needs_recap = 1
ORDER BY last_active_at DESC, id DESC;

-- name: ClaimSessionRecap :execrows
UPDATE agent_sessions
SET needs_recap = 0
WHERE external_id = ? AND needs_recap = 1;

-- name: ListSessionStatuses :many
-- Ordered oldest-first so a caller building a per-task map ends up with
-- the most-recently-active session's status per task (plan section 8.4).
SELECT task_id, status
FROM agent_sessions
ORDER BY last_active_at ASC, id ASC;

-- name: ListSessionsWithTmux :many
-- Candidates for section 8.3's capture-pane poller: only sessions that
-- were launched under tmux at all, and not ones already known to have
-- ended (a session that already reported ended has nothing to poll).
SELECT *
FROM agent_sessions
WHERE tmux_session != '' AND status != 'ended'
ORDER BY last_active_at DESC, id DESC;

-- name: SetSessionWorkingIfUnchanged :execrows
-- Compare-and-swap write for section 8.3's poller: only takes effect if
-- status_updated_at is still what the poller observed right before it
-- captured the pane. A hook (Stop/Notification/SessionEnd) firing in
-- between moves the timestamp first, so this UPDATE affects zero rows
-- and the hook's authoritative status wins. Same idiom as
-- ClaimSessionRecap's compare-and-clear.
UPDATE agent_sessions
SET status = 'working', status_updated_at = datetime('now')
WHERE external_id = ? AND status_updated_at IS ?;
