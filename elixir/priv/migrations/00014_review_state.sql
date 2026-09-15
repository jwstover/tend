-- +goose Up
-- A `review` workflow state (tend task #199): work that is finished on
-- the author's side and waiting on someone else's eyes -- a PR out for
-- review, a hand-off awaiting sign-off. It is live (not terminal, not
-- hidden) so it shows in the list by default, and it sorts just before
-- doing so review-ready work reads above the work still in flight.
UPDATE states SET sort_order = sort_order + 1 WHERE sort_order >= 2;
INSERT INTO states (name, sort_order, is_terminal, hidden_by_default)
VALUES ('review', 2, 0, 0);

-- +goose Down
-- Tasks in review fall back to doing: still open, still someone's, just
-- without the distinction. Done first so the FK on tasks.state lets the
-- row go.
UPDATE tasks SET state = 'doing' WHERE state = 'review';
DELETE FROM states WHERE name = 'review';
UPDATE states SET sort_order = sort_order - 1 WHERE sort_order > 2;
