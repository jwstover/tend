-- +goose Up
-- OpenTelemetry events claude exported while running a workflow step (tend
-- task #31): api_request with its per-call tokens and query_source
-- (main/subagent/auxiliary), api_error with its status code, and the
-- session.count metric whose start_type says whether a session resumed.
-- Attribution the transcripts and the result event do not have.
--
-- Keyed by step run and cascading with it like the other record tables.
-- Only the runner's receiver writes it. It is enrichment, forward-only and
-- lossy (a step can exit before its exporter flushes): the token columns
-- added in 00018 stay authoritative and nothing reads these rows for
-- correctness.
CREATE TABLE workflow_step_run_events (
  id INTEGER PRIMARY KEY,
  step_run_id INTEGER NOT NULL REFERENCES workflow_step_runs(id) ON DELETE CASCADE,
  name TEXT NOT NULL,
  session_id TEXT NOT NULL DEFAULT '',
  model TEXT NOT NULL DEFAULT '',
  query_source TEXT NOT NULL DEFAULT '',
  agent_name TEXT NOT NULL DEFAULT '',
  skill_name TEXT NOT NULL DEFAULT '',
  mcp_server_name TEXT NOT NULL DEFAULT '',
  input_tokens INTEGER NOT NULL DEFAULT 0,
  output_tokens INTEGER NOT NULL DEFAULT 0,
  cache_read_tokens INTEGER NOT NULL DEFAULT 0,
  cache_creation_tokens INTEGER NOT NULL DEFAULT 0,
  cost_usd REAL NOT NULL DEFAULT 0,
  duration_ms INTEGER NOT NULL DEFAULT 0,
  status_code INTEGER NOT NULL DEFAULT 0,
  -- every attribute as a JSON object of strings; json_extract reaches what is not promoted
  attributes TEXT NOT NULL DEFAULT '{}',
  occurred_at TEXT NOT NULL,
  received_at TEXT NOT NULL
);
CREATE INDEX workflow_step_run_events_step_run ON workflow_step_run_events(step_run_id, occurred_at);

-- +goose Down
DROP INDEX workflow_step_run_events_step_run;
DROP TABLE workflow_step_run_events;
