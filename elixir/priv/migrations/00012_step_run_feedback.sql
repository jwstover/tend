-- +goose Up
-- What a step was told on a loop-back, for the MCP step tools (tend task
-- #180). The runner already rendered Feedback into the prompt, but only
-- input was persisted, so a session asking get_workflow_step for its
-- feedback had nothing to read back. Recorded alongside input so the
-- step run keeps saying what actually ran. '' when the step was reached
-- over a forward edge.
ALTER TABLE workflow_step_runs ADD COLUMN feedback TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE workflow_step_runs DROP COLUMN feedback;
