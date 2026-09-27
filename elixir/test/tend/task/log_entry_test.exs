defmodule Tend.Task.LogEntryTest do
  use ExUnit.Case, async: true

  alias Tend.Task.DayNotes
  alias Tend.Task.LogEntry
  alias Tend.Task.MovedItem
  alias Tend.Task.NoteGroup
  alias Tend.Task.Summary
  alias Tend.Task.SummaryItem

  # Go's tests write their timestamps in time.Local and expect the buckets and
  # stamps to come back in the same zone. `created_at` is stored UTC, so the
  # instant a local wall clock names is what a note carries -- computed here by
  # the inverse of the conversion the module under test performs, so the two
  # have to agree without either hard-coding an offset.
  defp local(year, month, day, hour, minute) do
    [utc | _ambiguous] =
      :calendar.local_time_to_universal_time_dst({{year, month, day}, {hour, minute, 0}})

    utc |> NaiveDateTime.from_erl!() |> DateTime.from_naive!("Etc/UTC")
  end

  defp note(task_id, title, body, at) do
    %LogEntry{task_id: task_id, task_title: title, body: body, created_at: at}
  end

  describe "the struct" do
    test "a bare note defaults to Go's zero values" do
      assert %LogEntry{} == %LogEntry{
               id: 0,
               task_id: nil,
               task_title: "",
               body: "",
               created_at: nil
             }
    end
  end

  describe "ref/1" do
    test "a freestanding note has no reference" do
      assert LogEntry.ref(%LogEntry{}) == ""
    end

    test "a note whose task is gone falls back to the bare id" do
      assert LogEntry.ref(%LogEntry{task_id: 42}) == "#42"
    end

    test "a note with a title reads by title" do
      assert LogEntry.ref(%LogEntry{task_id: 42, task_title: "ship it"}) == "ship it"
    end
  end

  describe "group_notes/1" do
    test "buckets by task, keeping first-appearance and chronological order" do
      base = local(2026, 7, 6, 9, 0)

      groups =
        LogEntry.group_notes([
          note(1, "ship it", "started on the retry logic", base),
          note(nil, "", "paired with sam", DateTime.add(base, 1, :hour)),
          note(2, "review pipeline", "waiting on infra", DateTime.add(base, 2, :hour)),
          note(1, "ship it", "fixed for real", DateTime.add(base, 3, :hour))
        ])

      assert [first, freestanding, third] = groups
      assert first.task_id == 1
      assert first.title == "ship it"
      assert freestanding.task_id == nil
      assert third.task_id == 2

      assert Enum.map(first.notes, & &1.body) == [
               "started on the retry logic",
               "fixed for real"
             ]
    end

    test "an empty window has no groups" do
      assert LogEntry.group_notes([]) == []
    end
  end

  describe "split_notes_by_day/1" do
    test "buckets a window into local calendar days" do
      mon = local(2026, 7, 6, 9, 0)
      tue = local(2026, 7, 7, 8, 0)

      days =
        LogEntry.split_notes_by_day([
          note(1, "ship it", "monday morning", mon),
          note(nil, "", "monday afternoon", DateTime.add(mon, 6, :hour)),
          note(1, "ship it", "tuesday", tue)
        ])

      assert [%DayNotes{day: ~D[2026-07-06]} = monday, %DayNotes{day: ~D[2026-07-07]} = tuesday] =
               days

      assert length(monday.notes) == 2
      assert length(tuesday.notes) == 1
    end

    test "a note just before local midnight stays on its own day" do
      late = local(2026, 7, 6, 23, 30)
      early = local(2026, 7, 7, 0, 30)

      days =
        LogEntry.split_notes_by_day([note(nil, "", "late", late), note(nil, "", "early", early)])

      assert Enum.map(days, & &1.day) == [~D[2026-07-06], ~D[2026-07-07]]
    end

    test "an empty window has no days" do
      assert LogEntry.split_notes_by_day([]) == []
    end
  end

  describe "day_label/2" do
    test "names a day relative to now" do
      now = ~D[2026-07-07]

      assert LogEntry.day_label(~D[2026-07-07], now) == "Today"
      assert LogEntry.day_label(~D[2026-07-06], now) == "Yesterday"
      assert LogEntry.day_label(~D[2026-07-03], now) == "Fri Jul 3"
    end

    test "a two-digit day is not padded, the way Go's Jan 2 is not" do
      assert LogEntry.day_label(~D[2026-07-13], ~D[2026-07-20]) == "Mon Jul 13"
    end
  end

  describe "normalize_note/1" do
    test "trims surrounding whitespace" do
      assert LogEntry.normalize_note("  paired with sam \n") == {:ok, "paired with sam"}
    end

    test "rejects a blank note with the Go sentinel" do
      assert LogEntry.normalize_note("") == {:error, :empty_note}
      assert LogEntry.normalize_note("   \n\t ") == {:error, :empty_note}
      assert Tend.Error.message(:empty_note) == "log entry is empty"
    end
  end

  describe "standup_markdown/4" do
    test "groups the notes of a day under one header" do
      base = local(2026, 7, 6, 9, 0)

      md =
        LogEntry.standup_markdown(
          "Yesterday",
          [
            note(1, "ship it", "started", base),
            note(nil, "", "paired with sam", DateTime.add(base, 1, :hour)),
            note(1, "ship it", "finished", DateTime.add(base, 2, :hour))
          ],
          %Summary{},
          []
        )

      assert md =~ "- ship it (#1)\n"
      assert md =~ "- general\n"
      # Both task notes nest under one group header.
      assert length(String.split(md, "- ship it (#1)")) == 2

      started = :binary.match(md, "started") |> elem(0)
      finished = :binary.match(md, "finished") |> elem(0)
      paired = :binary.match(md, "paired with sam") |> elem(0)
      assert started < finished
      assert paired > finished
    end

    test "the deleted-task fallback keeps the bare id as the group header" do
      md =
        LogEntry.standup_markdown(
          "Yesterday",
          [note(9, "", "orphaned", local(2026, 7, 6, 9, 0))],
          %Summary{},
          []
        )

      assert md =~ "- #9\n"
      refute md =~ "(#9)"
    end

    test "splits notes into a section per day" do
      mon = local(2026, 7, 6, 9, 0)
      tue = local(2026, 7, 7, 9, 0)

      md =
        LogEntry.standup_markdown(
          "Yesterday",
          [note(1, "ship it", "monday note", mon), note(1, "ship it", "tuesday note", tue)],
          %Summary{},
          []
        )

      mon_header = :binary.match(md, "**Notes — Mon Jul 6**") |> elem(0)
      tue_header = :binary.match(md, "**Notes — Tue Jul 7**") |> elem(0)
      mon_note = :binary.match(md, "monday note") |> elem(0)
      tue_note = :binary.match(md, "tuesday note") |> elem(0)

      assert mon_header < mon_note
      assert mon_note < tue_header
      assert tue_header < tue_note
      # The task heads a group in each day it has notes.
      assert length(String.split(md, "- ship it (#1)\n")) == 3
    end

    # The whole export, byte for byte, against what the Go implementation
    # prints for the same input -- the rendering is a copied-to-a-terminal
    # artifact, so a stray space is a real regression.
    test "renders every section exactly as Go does" do
      notes = [
        note(1, "ship it", "started", local(2026, 7, 6, 9, 0)),
        note(nil, "", "paired with sam\nsecond line", local(2026, 7, 6, 10, 30)),
        note(9, "", "orphaned", local(2026, 7, 7, 8, 5))
      ]

      summary = %Summary{
        completed: [%SummaryItem{task_id: 1, title: "ship it"}],
        blocked: [%SummaryItem{task_id: 2, title: "stuck"}],
        started: [%SummaryItem{task_id: 3, title: "underway"}],
        moved: [%MovedItem{task_id: 4, title: "wanderer", to: "hapi"}],
        triaged: 2
      }

      live = [
        %Tend.Task{id: 3, title: "underway", state: :doing},
        %Tend.Task{id: 2, title: "stuck", state: :blocked},
        %Tend.Task{id: 5, title: "later", state: :todo}
      ]

      assert LogEntry.standup_markdown("Since Friday", notes, summary, live) ==
               """
               **Notes — Mon Jul 6**
               - ship it (#1)
                 - 09:00 — started
               - general
                 - 10:30 — paired with sam
                   second line

               **Notes — Tue Jul 7**
               - #9
                 - 08:05 — orphaned

               **Since Friday**
               - Completed: ship it (#1)
               - Blocked: stuck (#2)
               - Started: underway (#3)
               - Moved to hapi: wanderer (#4)
               - Triaged 2 inbox item(s)

               **Today**
               - underway (#3)

               **Blockers**
               - stuck (#2)
               """
    end

    test "an empty window still prints all three sections" do
      assert LogEntry.standup_markdown("Yesterday", [], %Summary{}, []) ==
               """
               **Yesterday**
               - nothing logged

               **Today**
               - nothing in progress

               **Blockers**
               - none
               """
    end

    test "a triaged-only window is not nothing logged" do
      md = LogEntry.standup_markdown("Yesterday", [], %Summary{triaged: 1}, [])

      assert md =~ "- Triaged 1 inbox item(s)\n"
      refute md =~ "nothing logged"
    end
  end

  describe "NoteGroup and DayNotes" do
    test "default to empty" do
      assert %NoteGroup{} == %NoteGroup{task_id: nil, title: "", notes: []}
      assert %DayNotes{} == %DayNotes{day: nil, notes: []}
    end
  end
end
