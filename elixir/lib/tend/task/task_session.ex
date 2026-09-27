defmodule Tend.Task.TaskSession do
  @moduledoc """
  A session together with the title and state of the task it belongs to, for
  lists that span tasks -- the agents view shows every session in a project,
  and a row there has to say whose it is.

  Sessions listed under one task already know, so they stay plain
  `Tend.Task.Session`s.

  A port of `task.TaskSession` in `internal/task/session.go`. Go embeds the
  session, so `ts.ExternalID` reaches through; Elixir has no embedding, so the
  session sits in its own field and callers write `ts.session.external_id`.
  """

  alias Tend.Task.Session
  alias Tend.Task.State

  @type t :: %__MODULE__{
          session: Session.t(),
          task_title: String.t(),
          task_state: State.t() | nil
        }

  defstruct session: %Session{}, task_title: "", task_state: nil
end
