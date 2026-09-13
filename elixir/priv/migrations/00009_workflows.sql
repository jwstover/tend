-- +goose Up
-- Agent workflows: reusable multi-step agent procedures a task can run
-- (tend task #171). Definitions are ordinary rows behind sqlc, not files;
-- the TUI edits them the way it edits a task body.
--
-- Three definition tables and two record tables. The definition is read
-- live at each step start (no snapshot), so what actually ran is written
-- to workflow_step_runs at the time it ran. Deleting a workflow or step
-- still referenced by a run in a non-terminal state is refused by the
-- Store, the same way Store.DeleteProject stands in for a foreign key.

CREATE TABLE workflows (
  id          INTEGER PRIMARY KEY,
  name        TEXT NOT NULL UNIQUE COLLATE NOCASE,
  description TEXT NOT NULL DEFAULT '',
  created_at  TEXT NOT NULL DEFAULT (datetime('now')),
  updated_at  TEXT NOT NULL DEFAULT (datetime('now'))
);

-- A step is either an agent step (a headless claude session driven by
-- prompt_md) or a gate (a pause for a human decision; prompt_md, if set,
-- is the text shown to the reviewer). model and permission_mode are
-- stored as '' rather than NULL for "inherit the default": the values
-- are opaque to tend and only ever forwarded to claude, so there is no
-- three-valued logic worth modelling.
CREATE TABLE workflow_steps (
  id              INTEGER PRIMARY KEY,
  workflow_id     INTEGER NOT NULL REFERENCES workflows(id) ON DELETE CASCADE,
  name            TEXT    NOT NULL,
  kind            TEXT    NOT NULL DEFAULT 'agent' CHECK (kind IN ('agent', 'gate')),
  prompt_md       TEXT    NOT NULL DEFAULT '',
  model           TEXT    NOT NULL DEFAULT '',
  permission_mode TEXT    NOT NULL DEFAULT '',
  sort_order      INTEGER NOT NULL DEFAULT 0,
  created_at      TEXT    NOT NULL DEFAULT (datetime('now')),
  updated_at      TEXT    NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX idx_workflow_steps_workflow ON workflow_steps(workflow_id, sort_order);

-- Edges are keyed by outcome: the edges leaving a step are its allowed
-- outcomes, and an outcome with no edge ends the run. This one primitive
-- covers handoff (done -> next), review verdicts (approve/reject) and
-- loop-back with feedback (reject -> an earlier step, bounded by
-- max_iterations; NULL means unbounded). Outcomes are normalised
-- (trimmed, lower-cased) by the Store before they land here; the NOCASE
-- collation is belt and braces for the unique index.
--
-- Both endpoints cascade: an edge whose step is gone means nothing.
CREATE TABLE workflow_edges (
  id             INTEGER PRIMARY KEY,
  from_step_id   INTEGER NOT NULL REFERENCES workflow_steps(id) ON DELETE CASCADE,
  outcome        TEXT    NOT NULL COLLATE NOCASE,
  to_step_id     INTEGER NOT NULL REFERENCES workflow_steps(id) ON DELETE CASCADE,
  max_iterations INTEGER,
  UNIQUE (from_step_id, outcome)
);

CREATE INDEX idx_workflow_edges_to ON workflow_edges(to_step_id);

-- A run binds a workflow to a task, like a session does. It cascades with
-- the task for the same reason agent_sessions does: the run is a record
-- of work on that task and has no meaning without it. It also cascades
-- with its workflow -- the Store refuses to delete a workflow while a
-- run is live, and a finished run's history is history *of that
-- definition*, so it goes when the definition does. The task's own log
-- (recaps, notes) is untouched either way.
--
-- current_step_run_id points forward at a table created below; SQLite
-- resolves foreign keys at write time, so the order here is fine. It is
-- SET NULL rather than CASCADE because the step run going away must not
-- take the run with it.
--
-- tmux_session names the runner's tmux session (the process that drives
-- this run), '' when none has been started -- the same convention as
-- agent_sessions.tmux_session.
CREATE TABLE workflow_runs (
  id                  INTEGER PRIMARY KEY,
  workflow_id         INTEGER NOT NULL REFERENCES workflows(id) ON DELETE CASCADE,
  task_id             INTEGER NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
  cwd                 TEXT    NOT NULL,
  state               TEXT    NOT NULL DEFAULT 'pending'
                      CHECK (state IN ('pending', 'running', 'waiting_review', 'paused', 'done', 'failed', 'cancelled')),
  current_step_run_id INTEGER REFERENCES workflow_step_runs(id) ON DELETE SET NULL,
  tmux_session        TEXT    NOT NULL DEFAULT '',
  started_at          TEXT    NOT NULL DEFAULT (datetime('now')),
  ended_at            TEXT
);

CREATE INDEX idx_workflow_runs_task     ON workflow_runs(task_id);
CREATE INDEX idx_workflow_runs_workflow ON workflow_runs(workflow_id);

-- One step run is one execution of one step within a run: one agent
-- session (session_external_id) for an agent step, a pause for a gate.
-- iteration counts how many times this step has run within the run, for
-- max_iterations enforcement and the prompt's {{.Iteration}}.
--
-- prompt_rendered, model and permission_mode are what actually ran, since
-- the definition is read live and may change under a run. input is the
-- previous step's deliverable; outcome and deliverable are what this step
-- handed off. outcome is '' until the step finishes (ended_at is the
-- authoritative "finished" marker), and can never be '' afterwards
-- because the Store rejects an empty outcome. log_path is the stream-json
-- file for the step; the DB stores paths, not content.
CREATE TABLE workflow_step_runs (
  id                  INTEGER PRIMARY KEY,
  run_id              INTEGER NOT NULL REFERENCES workflow_runs(id)  ON DELETE CASCADE,
  step_id             INTEGER NOT NULL REFERENCES workflow_steps(id) ON DELETE CASCADE,
  iteration           INTEGER NOT NULL DEFAULT 1,
  session_external_id TEXT    NOT NULL DEFAULT '',
  prompt_rendered     TEXT    NOT NULL DEFAULT '',
  model               TEXT    NOT NULL DEFAULT '',
  permission_mode     TEXT    NOT NULL DEFAULT '',
  input               TEXT    NOT NULL DEFAULT '',
  outcome             TEXT    NOT NULL DEFAULT '',
  deliverable         TEXT    NOT NULL DEFAULT '',
  log_path            TEXT    NOT NULL DEFAULT '',
  started_at          TEXT    NOT NULL DEFAULT (datetime('now')),
  ended_at            TEXT
);

CREATE INDEX idx_workflow_step_runs_run  ON workflow_step_runs(run_id);
CREATE INDEX idx_workflow_step_runs_step ON workflow_step_runs(step_id);

-- A session launched by a workflow step points back at its step run so
-- the SESSIONS section can say which step it belonged to. Nullable with a
-- NULL default, which is the one shape SQLite lets ALTER TABLE add with a
-- REFERENCES clause (see migration 00007 for the shape it refuses).
-- Ordinary sessions leave it NULL. SET NULL rather than CASCADE: the
-- session and its transcript outlive the run record.
ALTER TABLE agent_sessions ADD COLUMN workflow_step_run_id INTEGER REFERENCES workflow_step_runs(id) ON DELETE SET NULL;

-- +goose Down
ALTER TABLE agent_sessions DROP COLUMN workflow_step_run_id;

DROP INDEX idx_workflow_step_runs_step;
DROP INDEX idx_workflow_step_runs_run;
DROP TABLE workflow_step_runs;

DROP INDEX idx_workflow_runs_workflow;
DROP INDEX idx_workflow_runs_task;
DROP TABLE workflow_runs;

DROP INDEX idx_workflow_edges_to;
DROP TABLE workflow_edges;

DROP INDEX idx_workflow_steps_workflow;
DROP TABLE workflow_steps;

DROP TABLE workflows;
