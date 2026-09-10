-- name: CreateSession :one
-- workflow_step_run_id is NULL for an ordinary session and set when the
-- session runs a workflow step (Store.CreateStepRunSession).
--
-- The row is written at launch, right before the terminal handoff, so
-- hooks fired during the session's very first run have a row to land on.
-- status starts at 'starting' with status_updated_at set (same %f
-- precision as SetSessionStatus below, since it is the poller's CAS
-- token): claude is being started but nothing has been observed yet,
-- and the settle floor in pollSessions counts from now rather than from
-- the epoch a NULL would read as.
INSERT INTO agent_sessions (task_id, external_id, cwd, label, tmux_session, workflow_step_run_id, status, status_updated_at)
VALUES (?, ?, ?, ?, ?, ?, 'starting', strftime('%Y-%m-%d %H:%M:%f', 'now'))
RETURNING *;

-- name: DeleteSession :exec
-- For a launch that failed before it ever became a session: the row was
-- written ahead of the handoff (CreateSession), and a broken launch must
-- not leave it behind as a phantom.
DELETE FROM agent_sessions
WHERE id = ?;

-- name: ListSessionsForTask :many
SELECT *
FROM agent_sessions
WHERE task_id = ?
ORDER BY last_active_at DESC, id DESC;

-- name: ListSessionsForProject :many
-- Every session on every task of one project (or of all projects, when
-- project_id is NULL -- the projects column's All row), most recently
-- active first: the agents view's list. agent_sessions has no project
-- column of its own, so the scope comes through the owning task; the
-- task's title and state ride along so a row can say which task the
-- session belongs to without a second read per session.
SELECT sqlc.embed(s), t.title AS task_title, t.state AS task_state
FROM agent_sessions s
JOIN tasks t ON t.id = s.task_id
WHERE (sqlc.narg(project_id) IS NULL OR t.project_id = sqlc.narg(project_id))
ORDER BY s.last_active_at DESC, s.id DESC;

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
-- status_updated_at uses strftime with %f (millisecond precision), not
-- plain datetime('now') (whole-second precision), because it doubles as
-- the freshness token section 8.3's poller CAS compares against
-- (SetSessionWorkingIfUnchanged / SetSessionIdleIfUnchanged below). Two
-- real writes a hook and a poll tick apart routinely land in the same
-- wall-clock second under real load -- confirmed directly: two
-- back-to-back datetime('now') calls in the same test process produced
-- byte-identical strings -- which would make the CAS's "did anything
-- change" check blind to a same-second race and let a poller guess
-- silently win against a hook it should always lose to. last_active_at
-- has no such requirement and keeps second precision.
UPDATE agent_sessions
SET status = ?, status_updated_at = strftime('%Y-%m-%d %H:%M:%f', 'now'), last_active_at = datetime('now')
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
SET status = 'working', status_updated_at = strftime('%Y-%m-%d %H:%M:%f', 'now')
WHERE external_id = ? AND status_updated_at IS ?;

-- name: SetSessionIdleIfUnchanged :execrows
-- The other half of the poller's CAS pair: takes 'working' back down when
-- a later tick no longer sees working chrome. Without this, a 'working'
-- write that raced a Stop hook's own write within the same
-- datetime('now') second -- or simply observed one trailing frame of
-- stale chrome -- has no way back down until the *next* hook fires,
-- which can be an arbitrarily long wait. The caller only ever invokes
-- this when it just read status = 'working' itself, so the CAS here
-- guards the same way SetSessionWorkingIfUnchanged does: a hook landing
-- between the read and this write moves status_updated_at first, and
-- this UPDATE affects zero rows, leaving the hook's fresher status
-- (idle, blocked, ended -- whatever it set) standing untouched.
UPDATE agent_sessions
SET status = 'idle', status_updated_at = strftime('%Y-%m-%d %H:%M:%f', 'now')
WHERE external_id = ? AND status_updated_at IS ?;

-- name: SetSessionEndedIfUnchanged :execrows
-- Closes the SessionEnd gap: a host that dies takes its chance to fire
-- the hook with it, so the poller is the only thing left that will ever
-- learn the session is gone (via tmux has-session, checked by the
-- caller before this runs). Same CAS contract as the working/idle pair
-- above -- a hook landing between the caller's has-session check and
-- this write moves status_updated_at first, so this UPDATE affects zero
-- rows and the hook's own status is left standing.
UPDATE agent_sessions
SET status = 'ended', status_updated_at = strftime('%Y-%m-%d %H:%M:%f', 'now')
WHERE external_id = ? AND status_updated_at IS ?;
