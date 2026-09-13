defmodule Tend.Task.Session do
  @moduledoc """
  A Claude Code session launched or resumed against a task -- a port of
  `task.Session` in `internal/task/session.go`.

  `external_id` is the `claude --session-id` UUID; `label` is the task title
  snapshotted at launch time, so sessions still read correctly after a rename
  or delete -- the same convention the event and log entries use.

  `tmux_session` is the name of the tmux session wrapping this one, empty when
  it wasn't launched under tmux (no tmux on `$PATH`, or a row written before
  tmux-backed launch/attach existed). A non-empty name is a candidate for
  attaching, not a promise the session is still alive -- only
  `tmux has-session` answers that.

  `needs_recap` marks a session that was backgrounded rather than exited, so
  its recap was deliberately skipped and is still owed. Any tend instance
  drains it once the session is really gone.

  `status` is what tend last observed about the session and `status_updated_at`
  is when that changed; `nil` only for a row from before rows were written at
  launch. Both are a convenience indicator, deliberately not a source of truth
  (`tmux has-session` is; see `:ended`). The row is written at launch, ahead of
  the terminal handoff, with status `:starting` -- so the session's own hooks
  find it from the very first turn, and a fresh session reads starting, then
  idle, without ever being resumed.

  `step_run_id` is set when the session ran a workflow step (it points at that
  step run) so the SESSIONS section can say which step a session belonged to;
  `nil` for an ordinary session.

  Struct fields default to their Go zero values wherever Go's zero has an
  Elixir counterpart, the same caveat `Tend.Task` carries: the timestamps and
  `status` are `nil` here, where Go has `time.Time{}` and `SessionStatus("")`.
  """

  alias Tend.Task.SessionStatus

  @typedoc "A session, field for field the Go struct."
  @type t :: %__MODULE__{
          id: integer(),
          task_id: integer(),
          external_id: String.t(),
          cwd: String.t(),
          label: String.t(),
          tmux_session: String.t(),
          needs_recap: boolean(),
          status: SessionStatus.t() | nil,
          status_updated_at: DateTime.t() | nil,
          started_at: DateTime.t() | nil,
          last_active_at: DateTime.t() | nil,
          step_run_id: integer() | nil
        }

  defstruct id: 0,
            task_id: 0,
            external_id: "",
            cwd: "",
            label: "",
            tmux_session: "",
            needs_recap: false,
            status: nil,
            status_updated_at: nil,
            started_at: nil,
            last_active_at: nil,
            step_run_id: nil

  @doc """
  Whether the session ran a workflow step under the runner rather than in a
  terminal of its own: it has a step run and no tmux session (the pane is the
  runner's, not claude's).

  Such a session has no pane to attach to, and its transcript is the runner's
  to drive while its run is live.
  """
  @spec headless?(t()) :: boolean()
  def headless?(%__MODULE__{step_run_id: step_run_id, tmux_session: tmux_session}) do
    step_run_id != nil and tmux_session == ""
  end
end
