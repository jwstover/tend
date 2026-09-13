-- +goose Up
-- The block the runner appends to claude's system prompt for an agent
-- step (tend task #197): where the step sits in its workflow and how it
-- must hand off through finish_step. Recorded on the step run, beside
-- prompt_rendered, so the row keeps saying exactly what ran -- the
-- author's prompt_md stays clean and the contract lives here. '' for a
-- gate, and for step runs from before this column existed.
ALTER TABLE workflow_step_runs ADD COLUMN system_prompt TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE workflow_step_runs DROP COLUMN system_prompt;
