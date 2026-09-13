-- +goose Up
-- A project's default working directory (tend task #190). A project is
-- typically scoped to one application checkout, so a new Claude session on
-- any of its tasks almost always wants to start there. This is the value
-- the TUI's cwd prompt is prefilled with when a task has no earlier
-- session to copy from; it is a default, never a constraint.
--
-- '' rather than NULL for "unset", matching workflow_steps.model and
-- permission_mode: there is no third state worth modelling, and a
-- NOT NULL column with a default keeps every existing INSERT valid.
ALTER TABLE projects ADD COLUMN cwd TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE projects DROP COLUMN cwd;
