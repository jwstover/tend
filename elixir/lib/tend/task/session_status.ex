defmodule Tend.Task.SessionStatus do
  @moduledoc """
  What tend last observed about a Claude Code session's own state -- a port of
  `task.SessionStatus` in `internal/task/session.go`.

  It is a cache of something watched from outside the process, not a workflow
  the user moves a row through, which is why it is a plain string column with
  no `states`-table foreign key the way `tasks.state` has. A value tend does
  not recognize reads as `:unknown` rather than failing, so `parse/1` cannot
  error.

  > #### One divergence from Go {: .info}
  >
  > Go keeps an unrecognized status as the raw string it read and only treats
  > it as unknown at the point of display. `parse/1` folds it to `:unknown`
  > immediately, so the raw text is lost. Nothing in the port can see the
  > difference today -- the only Go code that renders the raw value is the
  > TUI, which is not ported -- but a UI port that wants to echo a status from
  > the future will need to carry it.
  """

  @typedoc "A status tend recognizes."
  @type t :: :unknown | :starting | :working | :idle | :blocked | :ended

  # Declaration order matches the Go constants.
  @statuses [
    # The honest default for a session tend has never heard from: a row from
    # before launch-time rows existed, or a status value tend doesn't
    # recognize. A session tend launched itself never starts here -- it starts
    # at :starting.
    :unknown,
    # claude is being started, or is up, but nothing has been observed about
    # what it's doing yet. Written at launch when the row is created, and
    # again by the SessionStart hook.
    :starting,
    # claude is mid-turn. No hook reports this; Claude Code fires nothing
    # during a tool call, so only the capture-pane poller ever writes it.
    :working,
    # Set by the Stop hook -- claude finished a turn and is sitting at the
    # input prompt.
    :idle,
    # Set by the Notification hook -- claude is waiting on the user, typically
    # a permission prompt.
    :blocked,
    # Set by the SessionEnd hook. Note this fires on /clear too, where the
    # process keeps running, so it is never the authority on liveness --
    # `tmux has-session` is.
    :ended
  ]

  @by_name Map.new(@statuses, &{Atom.to_string(&1), &1})

  @doc """
  Every status tend recognizes, in the order the Go constants declare them.
  """
  @spec all() :: [t()]
  def all, do: @statuses

  @doc """
  Whether `status` is a status tend recognizes.
  """
  @spec valid?(term()) :: boolean()
  def valid?(status), do: status in @statuses

  @doc """
  The status's stored name, the string the `sessions.status` column holds.
  """
  @spec format(t()) :: String.t()
  def format(status) when status in @statuses, do: Atom.to_string(status)

  @doc """
  The status named by `name`, or `:unknown` for anything else -- including the
  empty string a row written before the column existed leaves behind.

  Deliberately total: a status is an observation, and refusing to load a
  session because something wrote a word tend has not heard of would be worse
  than admitting the observation is unknown.
  """
  @spec parse(String.t()) :: t()
  def parse(name) when is_binary(name), do: Map.get(@by_name, name, :unknown)
end
