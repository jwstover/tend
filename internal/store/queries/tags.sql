-- name: ListTags :many
SELECT *
FROM tags
ORDER BY name;

-- name: UpsertTag :one
-- DO UPDATE rather than DO NOTHING so RETURNING always yields the row:
-- with DO NOTHING a conflicting insert returns no rows at all.
INSERT INTO tags (name)
VALUES (?)
ON CONFLICT(name) DO UPDATE SET name = excluded.name
RETURNING *;

-- name: AttachTag :exec
INSERT OR IGNORE INTO task_tags (task_id, tag_id)
VALUES (?, ?);

-- name: ClearTaskTags :exec
DELETE FROM task_tags
WHERE task_id = ?;

-- name: ListTagsForTask :many
SELECT g.name
FROM task_tags tt
JOIN tags g ON g.id = tt.tag_id
WHERE tt.task_id = ?
ORDER BY g.name;

-- name: ListAllTaskTags :many
-- Batch load for the list view: one query for every visible row's tags,
-- collapsed into a map[taskID][]string, rather than N+1 per-row queries.
-- Same idiom as ListChildCounts.
SELECT tt.task_id, g.name
FROM task_tags tt
JOIN tags g ON g.id = tt.tag_id
ORDER BY tt.task_id, g.name;

-- name: DeleteOrphanTags :exec
-- Tags are implicit: they exist because a task carries them. Dropping the
-- last reference drops the tag, so the tag list can't accumulate ghosts.
DELETE FROM tags
WHERE id NOT IN (SELECT tag_id FROM task_tags);
