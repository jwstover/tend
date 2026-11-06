-- name: GetSetting :one
SELECT value
FROM settings
WHERE key = ?;

-- name: SetSetting :exec
INSERT INTO settings (key, value)
VALUES (?, ?)
ON CONFLICT(key) DO UPDATE SET value = excluded.value;

-- name: DeleteSetting :exec
DELETE FROM settings
WHERE key = ?;
