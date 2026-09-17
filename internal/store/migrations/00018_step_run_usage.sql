-- +goose Up
-- What a step run cost, as claude's result event reports it (tend task
-- #23). The point is the split, not the total: cache_read against
-- cache_creation is how a workflow's prompt-cache hit rate becomes an
-- observed number rather than a guess, which is the measurement the
-- session-continuity work is gated on.
--
-- Stored rather than derived because the step's stream-json log is
-- cleanup-eligible and the run view must not need a filesystem scan to
-- draw a row. Summed across every claude process that ran the step, the
-- way usage.ParseStepLog sums the log's result events. 0 for a gate, and
-- for step runs from before this column existed.
ALTER TABLE workflow_step_runs ADD COLUMN input_tokens INTEGER NOT NULL DEFAULT 0;
ALTER TABLE workflow_step_runs ADD COLUMN output_tokens INTEGER NOT NULL DEFAULT 0;
ALTER TABLE workflow_step_runs ADD COLUMN cache_creation_tokens INTEGER NOT NULL DEFAULT 0;
ALTER TABLE workflow_step_runs ADD COLUMN cache_read_tokens INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE workflow_step_runs DROP COLUMN cache_read_tokens;
ALTER TABLE workflow_step_runs DROP COLUMN cache_creation_tokens;
ALTER TABLE workflow_step_runs DROP COLUMN output_tokens;
ALTER TABLE workflow_step_runs DROP COLUMN input_tokens;
