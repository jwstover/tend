-- +goose Up
-- A step's advisor model (tend task #32): claude's `--advisor`, forwarded
-- the same way model and permission_mode are -- '' meaning "inherit
-- whatever the user's own advisorModel setting says", never tend's
-- business to interpret further.
ALTER TABLE workflow_steps ADD COLUMN advisor_model TEXT NOT NULL DEFAULT '';

-- workflow_step_runs records what actually ran, the same as model and
-- permission_mode do.
ALTER TABLE workflow_step_runs ADD COLUMN advisor_model TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE workflow_step_runs DROP COLUMN advisor_model;
ALTER TABLE workflow_steps DROP COLUMN advisor_model;
