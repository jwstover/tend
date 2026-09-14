defmodule Tend.Workflow.RunState do
  @moduledoc """
  Where a run is in its life -- a port of `workflow.RunState` in
  `internal/workflow/workflow.go`, together with its `SessionStatus` method
  from `internal/workflow/status.go`.

  Unlike `Tend.Task.SessionStatus` this is a workflow the runner moves rows
  through, not a cache of something observed from outside, so the schema pins
  the set with a `CHECK` and `parse/1` is strict: a state the database does not
  recognise cannot slip into a `Tend.Workflow.Run`.

  `terminal?/1` is what makes a state final. A run in a terminal state is
  history: its workflow and steps may be deleted, and its state can no longer
  change.
  """

  alias Tend.Task.SessionStatus

  @typedoc "One of the seven run states."
  @type t :: :pending | :running | :waiting_review | :paused | :done | :failed | :cancelled

  # Declaration order matches the Go constants.
  @states [
    # Created, but its runner has not claimed it yet.
    :pending,
    # The runner owns the run and a step is executing.
    :running,
    # The run is sitting at a gate step.
    :waiting_review,
    # The user stopped the runner, typically to take over the current step
    # interactively.
    :paused,
    # The last step's outcome had no edge to follow.
    :done,
    # The runner gave up: a step could not launch, or an edge's max_iterations
    # was exceeded.
    :failed,
    # The user ended the run deliberately.
    :cancelled
  ]

  @terminal [:done, :failed, :cancelled]

  @by_name Map.new(@states, &{Atom.to_string(&1), &1})

  @doc """
  Every run state, in the order the Go constants declare them.
  """
  @spec all() :: [t()]
  def all, do: @states

  @doc """
  Whether `state` is a known run state.

  The counterpart of Go's `RunState.Valid`. Accepts any term, so it can guard a
  value that came from outside.
  """
  @spec valid?(term()) :: boolean()
  def valid?(state), do: state in @states

  @doc """
  Whether `state` ends a run.

  The counterpart of Go's `RunState.Terminal`, which answers for any string:
  an unknown value is not terminal, so this accepts any term too.
  """
  @spec terminal?(term()) :: boolean()
  def terminal?(state), do: state in @terminal

  @doc """
  The state's stored name, the string the `workflow_runs.state` column holds.
  """
  @spec format(t()) :: String.t()
  def format(state) when state in @states, do: Atom.to_string(state)

  @doc """
  The state named by `name`, or `:error`.

  Rejects everything Go's `Valid` rejects: the empty string, a differently
  cased name, and a state that does not exist.
  """
  @spec parse(String.t()) :: {:ok, t()} | :error
  def parse(name) when is_binary(name), do: Map.fetch(@by_name, name)

  @doc """
  The agent-session status a live run reads as, or `:error` when it has none.

  A port of `RunState.SessionStatus` in `internal/workflow/status.go`. It maps
  a live run's state into the vocabulary the TUI already renders -- the gutter
  marker, the `g a` grouping, the detail pane glyphs -- so a task with a
  headless run in flight reads the same way a task with an interactive session
  does, with no second glyph family or status column.

      pending        -> :starting   (a runner has yet to claim it)
      running        -> :working    (a step is executing)
      waiting_review -> :blocked    (a gate is waiting on the user)
      paused         -> :idle       (nothing is happening until resumed)

  Terminal states have none: a finished run says nothing about the task's agent
  status, and whatever its sessions last reported stands. Go returns
  `(SessionStatus(""), false)` there; `:error` is the same answer in the shape
  `Map.fetch/2` and `Tend.Task.State.parse/1` already use, and it keeps the
  invalid `SessionStatus("")` out of the port entirely.

  Accepts any term, because Go's method does: an unrecognised state falls
  through to the same "no status" answer as a terminal one.
  """
  @spec session_status(term()) :: {:ok, SessionStatus.t()} | :error
  def session_status(:pending), do: {:ok, :starting}
  def session_status(:running), do: {:ok, :working}
  def session_status(:waiting_review), do: {:ok, :blocked}
  def session_status(:paused), do: {:ok, :idle}
  def session_status(_state), do: :error
end
