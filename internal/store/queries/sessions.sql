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
