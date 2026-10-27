-- +goose Up
-- A `canceled` workflow state (tend task #672): closed without being done.
-- Terminal and hidden by default like done, so it leaves the live view and
-- unblocks dependents, but it is never counted or reported as completed.
-- Appended after someday so no existing sort_order moves.
INSERT INTO states (name, sort_order, is_terminal, hidden_by_default)
VALUES ('canceled', 7, 1, 1);

-- +goose Down
-- Canceled tasks fall back to someday: still hidden, but no longer closed.
-- Done first so the FK on tasks.state lets the row go.
UPDATE tasks SET state = 'someday', completed_at = NULL WHERE state = 'canceled';
DELETE FROM states WHERE name = 'canceled';
