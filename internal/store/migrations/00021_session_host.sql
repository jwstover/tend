-- +goose Up
-- The program hosting a session's terminal when it is not tend: "devhub"
-- for a session started by `tend session prepare` and run in devhub's
-- terminal runtime. tend cannot attach to, resume, kill or poll such a
-- session (it is in no tmux of tend's), so the TUI refuses those and
-- points at the host. Empty for every session tend launched itself.
ALTER TABLE agent_sessions ADD COLUMN host TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE agent_sessions DROP COLUMN host;
