defmodule Tend.GoParityTest do
  @moduledoc """
  Reads the Go sources the domain modules were ported from and fails when the
  two drift apart.

  The other test files check behaviour; these check *shape*, against the one
  source of truth for it. A sentinel error added to `internal/task` with no
  atom in `Tend.Error`, or a struct field added on either side and not the
  other, is a failure here rather than a surprise when the store port tries to
  read a column that has nowhere to go.
  """

  use ExUnit.Case, async: true

  alias Tend.Task.DayNotes
  alias Tend.Task.Event
  alias Tend.Task.LogEntry
  alias Tend.Task.MovedItem
  alias Tend.Task.NoteGroup
  alias Tend.Task.Project
  alias Tend.Task.Session
  alias Tend.Task.Summary
  alias Tend.Task.SummaryItem

  # The Go package this part of the port covers. Its one remaining sibling
  # (brief.go) belongs to a later part and is deliberately not read.
  @go_dir Path.expand("../../../internal/task", __DIR__)
  @go_files ["task.go", "project.go", "session.go", "event.go", "log.go"]

  defp go_source(file) do
    path = Path.join(@go_dir, file)
    assert File.exists?(path), "expected the Go source at #{path}"
    File.read!(path)
  end

  defp go_sources, do: Enum.map_join(@go_files, "\n", &go_source/1)

  # Every `ErrX = errors.New("...")` or `ErrX = fmt.Errorf("...")`, grouped or
  # standalone, as {"ErrX", "message"}. Both constructors count: a sentinel is
  # a package-level Err value callers reach for with errors.Is, and which of
  # the two built it is the author's choice, not a distinction this port cares
  # about.
  defp go_sentinels do
    ~r/(Err\w+)\s*=\s*(?:errors\.New|fmt\.Errorf)\("([^"]*)"\)/
    |> Regex.scan(go_sources())
    |> Enum.map(fn [_whole, name, message] -> {name, message} end)
  end

  # The field names of a Go struct, in declaration order. The structs read here
  # have no nested braces, so matching to the first unindented "}" is enough.
  defp go_struct_fields(file, struct_name) do
    source = go_source(file)

    [_whole, body] =
      Regex.run(~r/\ntype #{struct_name} struct \{\n(.*?)\n\}\n/s, source) ||
        flunk("no `type #{struct_name} struct` in #{file}")

    body
    |> String.split("\n")
    |> Enum.map(&String.trim/1)
    |> Enum.reject(&(&1 == "" or String.starts_with?(&1, "//")))
    |> Enum.map(&(&1 |> String.split(~r/\s+/) |> hd()))
  end

  defp elixir_fields(struct), do: struct |> Map.from_struct() |> Map.keys() |> Enum.sort()

  defp expected_fields(file, struct_name) do
    file
    |> go_struct_fields(struct_name)
    |> Enum.map(&String.to_atom(Macro.underscore(&1)))
    |> Enum.sort()
  end

  describe "sentinel errors" do
    test "the Go sources really do define the sentinels we think they do" do
      # Guards the regex itself: if it silently stopped matching, every other
      # assertion in this block would pass vacuously.
      assert length(go_sentinels()) == 7
    end

    test "each Go sentinel maps to exactly one atom, by the documented rule" do
      expected =
        go_sentinels()
        |> Enum.map(fn {name, _message} ->
          name |> String.replace_prefix("Err", "") |> Macro.underscore() |> String.to_atom()
        end)
        |> Enum.sort()

      assert expected == Tend.Error.sentinels()
      assert length(Enum.uniq(expected)) == length(expected)
    end

    test "each atom's message is its Go counterpart's, verbatim" do
      for {name, message} <- go_sentinels() do
        atom = name |> String.replace_prefix("Err", "") |> Macro.underscore() |> String.to_atom()

        assert Tend.Error.message(atom) == message,
               "#{name} says #{inspect(message)}; :#{atom} says #{inspect(Tend.Error.message(atom))}"
      end
    end
  end

  describe "struct fields" do
    test "Tend.Task has the fields of Go's task.Task" do
      assert elixir_fields(%Tend.Task{}) == expected_fields("task.go", "Task")
    end

    test "Tend.Task.Project has the fields of Go's task.Project" do
      assert elixir_fields(%Project{}) == expected_fields("project.go", "Project")
    end

    test "Tend.Task.Session has the fields of Go's task.Session" do
      assert elixir_fields(%Session{}) == expected_fields("session.go", "Session")
    end

    test "Tend.Task.Event has the fields of Go's task.Event" do
      assert elixir_fields(%Event{}) == expected_fields("event.go", "Event")
    end

    test "the summary structs have the fields of their Go counterparts" do
      assert elixir_fields(%Summary{}) == expected_fields("event.go", "Summary")
      assert elixir_fields(%SummaryItem{}) == expected_fields("event.go", "SummaryItem")
      assert elixir_fields(%MovedItem{}) == expected_fields("event.go", "MovedItem")
    end

    test "the log structs have the fields of their Go counterparts" do
      assert elixir_fields(%LogEntry{}) == expected_fields("log.go", "LogEntry")
      assert elixir_fields(%NoteGroup{}) == expected_fields("log.go", "NoteGroup")
      assert elixir_fields(%DayNotes{}) == expected_fields("log.go", "DayNotes")
    end
  end

  describe "enumerations" do
    test "the states are the Go State constants" do
      names =
        ~r/State(\w+)\s+State = "(\w+)"/
        |> Regex.scan(go_source("task.go"))
        |> Enum.map(fn [_whole, _const, name] -> name end)

      assert names != []
      assert Enum.map(Tend.Task.State.all(), &Tend.Task.State.format/1) == names
    end

    test "the session statuses are the Go SessionStatus constants" do
      names =
        ~r/Session(\w+)\s+SessionStatus = "(\w+)"/
        |> Regex.scan(go_source("session.go"))
        |> Enum.map(fn [_whole, _const, name] -> name end)

      assert names != []
      assert Enum.map(Tend.Task.SessionStatus.all(), &Tend.Task.SessionStatus.format/1) == names
    end

    test "the event kinds are the Go EventKind constants" do
      names =
        ~r/Event(\w+)\s+EventKind = "(\w+)"/
        |> Regex.scan(go_source("event.go"))
        |> Enum.map(fn [_whole, _const, name] -> name end)

      assert names != []
      assert Enum.map(Event.kinds(), &Event.format_kind/1) == names
    end
  end

  describe "labels" do
    test "the top-level label is Go's TopLevelLabel, verbatim" do
      [_whole, label] =
        Regex.run(~r/\nconst TopLevelLabel = "([^"]*)"\n/, go_source("event.go")) ||
          flunk("no `const TopLevelLabel` in event.go")

      assert Event.top_level_label() == label
    end
  end
end
