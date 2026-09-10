-- +goose Up
-- Why a run failed, for the runner (tend task #179): the runner is a
-- process in a tmux session that closes when it exits, so a failure
-- message that only went to its pane would vanish with it. '' while the
-- run has not failed. A plain column add with a non-NULL default is the
-- shape SQLite accepts (see migration 00007 for the one it refuses).
ALTER TABLE workflow_runs ADD COLUMN error TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE workflow_runs DROP COLUMN error;
