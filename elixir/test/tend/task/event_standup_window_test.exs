defmodule Tend.Task.EventStandupWindowTest do
  @moduledoc """
  Regression cases for the standup window, carried over from the review of the
  port (`review/299-pass1`).

  Go's `LastWorkdayStart` is only ever called as `task.LastWorkdayStart(time.Now())`
  (`internal/cli/standup.go:23`, `internal/tui/standup.go:20`), i.e. with a
  *local* wall clock, and it returns *local* midnight of the previous weekday.
  The port takes an instant -- `DateTime.utc_now/0` is all a caller has, the
  port carrying no time zone database -- and an earlier version walked back from
  the calendar day of that instant's own offset, which east of UTC starts the
  window on the wrong day and labels it with the wrong weekday.

  Unlike `Tend.Task.EventTest`'s window cases, the instants here are fixed
  rather than derived from the local zone, so the local day and the UTC day
  genuinely differ -- for one instant east of UTC, for the other west of it.
  Run under either to see the two apart:

      cd elixir && TZ=Pacific/Auckland mix test test/tend/task/event_standup_window_test.exs
  """

  use ExUnit.Case, async: true

  alias Tend.Task.Event

  # The OS conversion, reached directly rather than through the port, so this
  # cannot agree with the code under test on a shared mistake.
  defp local_date(%DateTime{} = at) do
    at
    |> DateTime.to_naive()
    |> NaiveDateTime.to_erl()
    |> :calendar.universal_time_to_local_time()
    |> NaiveDateTime.from_erl!()
    |> NaiveDateTime.to_date()
  end

  defp previous_weekday(date) do
    prev = Date.add(date, -1)
    if Date.day_of_week(prev) in [6, 7], do: previous_weekday(prev), else: prev
  end

  # One instant either side of the day boundary, so the local day differs from
  # the UTC day whichever side of UTC the machine sits on:
  #
  #   * 2026-07-06T21:00:00Z is Tuesday 2026-07-07 09:00 in Pacific/Auckland, so
  #     Go's LastWorkdayStart(time.Now()) there lands on Monday 2026-07-06 and
  #     WindowLabel says "Yesterday" -- where reading the instant in UTC gives
  #     Friday 2026-07-03 and "Since Friday", three days early.
  #   * 2026-07-07T02:00:00Z is Monday 2026-07-06 22:00 in America/New_York, so
  #     the window opens on Friday 2026-07-03 -- where reading it in UTC gives
  #     Monday 2026-07-06.
  @instants [~U[2026-07-06 21:00:00Z], ~U[2026-07-07 02:00:00Z]]

  test "last_workday_start/1 starts the window at the previous weekday's *local* day" do
    for now <- @instants do
      from = Event.last_workday_start(now)

      assert local_date(from) == previous_weekday(local_date(now))
    end
  end

  test "window_label/2 names that window the way Go does" do
    for now <- @instants do
      from = Event.last_workday_start(now)
      today = local_date(now)
      want_start = previous_weekday(today)

      # What Go prints, given that it starts the window at local midnight of
      # want_start: "Yesterday" when that is the local day before today.
      want =
        if want_start == Date.add(today, -1),
          do: "Yesterday",
          else: "Since " <> Calendar.strftime(want_start, "%A")

      assert Event.window_label(from, now) == want
    end
  end
end
