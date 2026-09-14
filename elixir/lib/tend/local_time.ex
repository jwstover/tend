defmodule Tend.LocalTime do
  @moduledoc """
  The machine's wall clock, for the parts of the port Go reads through
  `time.Local`.

  `tend` stores instants in UTC and the standup reads them *locally*: a note
  written at 23:30 belongs to today's section rather than tomorrow's, and the
  reporting window opens at local midnight. Go takes that zone from
  `time.Local`; this port has no time zone database (`mix.exs` takes no
  dependencies, and Elixir ships only a UTC-only one), so both directions go
  through `:calendar`, which asks the OS the same question `time.Local` does
  and honours `$TZ` the same way.

  The consequence is that everything built on this -- `Tend.Task.LogEntry`'s
  bucketing and stamping, `Tend.Task.Event`'s reporting window -- is pure in
  its arguments but reads the machine's zone, exactly as its Go counterpart
  does.
  """

  @doc """
  Reads an instant as the machine's wall clock: Go's `time.Time.Local()`.

  Sub-second precision is dropped, since the OS conversion works in whole
  seconds and nothing the standup renders is finer.
  """
  @spec to_naive(DateTime.t()) :: NaiveDateTime.t()
  def to_naive(%DateTime{} = at) do
    at
    |> DateTime.shift_zone!("Etc/UTC")
    |> DateTime.to_naive()
    |> NaiveDateTime.truncate(:second)
    |> NaiveDateTime.to_erl()
    |> :calendar.universal_time_to_local_time()
    |> NaiveDateTime.from_erl!()
  end

  @doc """
  The instant a local wall clock names, in UTC: the inverse of `to_naive/1`,
  and what Go's `time.Date(..., time.Local)` computes.

  A wall clock the zone repeats -- the hour a DST transition replays -- resolves
  to its first occurrence. One the zone skips never happens at all; it resolves
  through standard time, which is the reading Go's own normalization lands on.
  """
  @spec from_naive(NaiveDateTime.t()) :: DateTime.t()
  def from_naive(%NaiveDateTime{} = local) do
    erl = local |> NaiveDateTime.truncate(:second) |> NaiveDateTime.to_erl()

    case :calendar.local_time_to_universal_time_dst(erl) do
      [utc | _later_occurrence] -> utc_from_erl(utc)
      [] -> erl |> :calendar.local_time_to_universal_time(false) |> utc_from_erl()
    end
  end

  defp utc_from_erl(erl), do: erl |> NaiveDateTime.from_erl!() |> DateTime.from_naive!("Etc/UTC")
end
