defmodule Tend.ErrorTest do
  use ExUnit.Case, async: true

  alias Tend.Error
  alias Tend.Store
  alias Tend.Template

  # The one-to-one mapping with the Go sentinels is checked against the Go
  # sources themselves, in Tend.GoParityTest. These are the module's own rules.

  describe "sentinels/0" do
    test "lists the sentinels of the ported files, sorted" do
      assert Error.sentinels() == [
               :cross_workflow_edge,
               :dependency_cycle,
               :empty_name,
               :empty_note,
               :empty_outcome,
               :empty_project_name,
               :empty_title,
               :in_use,
               :invalid_prompt,
               :project_not_found,
               :protected_project,
               :run_ended,
               :run_not_failed,
               :run_not_found,
               :self_dependency,
               :step_not_found,
               :step_run_finished,
               :step_run_not_found,
               :workflow_not_found
             ]
    end

    test "has no duplicates" do
      assert Enum.uniq(Error.sentinels()) == Error.sentinels()
    end
  end

  describe "sentinel?/1" do
    test "is true for every listed sentinel" do
      for reason <- Error.sentinels(), do: assert(Error.sentinel?(reason))
    end

    test "is false for anything else, of any shape" do
      refute Error.sentinel?(:nope)
      refute Error.sentinel?({:invalid_date, "x"})
      refute Error.sentinel?("empty_title")
      refute Error.sentinel?(nil)
    end
  end

  describe "message/1" do
    test "renders every sentinel" do
      for reason <- Error.sentinels() do
        message = Error.message(reason)
        assert is_binary(message) and message != ""
      end
    end

    test "renders the interpolating date error the way Go's fmt.Errorf does" do
      assert Error.message({:invalid_date, "06/09/2026"}) ==
               ~s|invalid date "06/09/2026" (want YYYY-MM-DD)|

      assert Error.message({:invalid_date, ""}) == ~s|invalid date "" (want YYYY-MM-DD)|
    end

    test "renders the wrapping in-use error the way Go's InUseError does" do
      # The literal output of workflow.InUseError("workflow 3", 7), the case
      # TestInUseErrorMatchesSentinel in internal/workflow/workflow_test.go
      # pins.
      assert Error.message({:in_use, "workflow 3", 7}) ==
               "workflow 3 is referenced by an active run 7"

      # The wrapped sentinel still renders on its own, as errors.Is sees it.
      assert Error.message(:in_use) == "referenced by an active run"
    end

    test "renders the store's task-surface errors the way their Go call sites do" do
      # fmt.Errorf("unknown state %q", st) in Store.SetState. The atom is
      # rendered back into the stored spelling before it is quoted.
      assert Error.message({:unknown_state, "archived"}) == ~s|unknown state "archived"|
      assert Error.message({:unknown_state, :archived}) == ~s|unknown state "archived"|

      # fmt.Errorf("priority %d out of range %d..%d", ...) in SetPriority.
      assert Error.message({:priority_out_of_range, 5}) == "priority 5 out of range 1..4"
      assert Error.message({:priority_out_of_range, -1}) == "priority -1 out of range 1..4"

      # Go's sql.ErrNoRows, wrapped by GetTask's "loading task %d: %w".
      assert Error.message({:task_not_found, 42}) == "loading task 42: no rows in result set"

      # toDomain's "task %d created_at: %w" around parseTime's "parsing %q".
      assert Error.message({:invalid_timestamp, "task 7 created_at", "yesterday"}) ==
               ~s|task 7 created_at: parsing "yesterday"|

      # The generic wrap every store call site puts around a driver failure.
      # A SQLite message is passed through, because Go's %w prints exactly it.
      assert Error.message({:query_failed, "inserting task", "UNIQUE constraint failed"}) ==
               "inserting task: UNIQUE constraint failed"

      assert Error.message({:query_failed, "inserting task", :misuse}) ==
               "inserting task: :misuse"
    end

    test "refuses a reason nobody registered, and says how to fix it" do
      assert_raise ArgumentError, ~r/add it to Tend.Error/, fn -> Error.message(:invented) end
      assert_raise ArgumentError, fn -> Error.message({:invented, 1}) end
    end
  end

  # The fold-in the template and workflow ports were waiting for: both
  # Tend.Template exceptions reach a user through :invalid_prompt, and
  # Tend.Error.message/1 has to splice their text in rather than restate it,
  # because that text is what names the offending variable and its position.
  describe "message/1 renders {:invalid_prompt, cause}" do
    test "prepends ErrInvalidPrompt's own text to a parse failure's message" do
      {:error, cause} = Template.parse("{{end}}")

      assert Error.message({:invalid_prompt, cause}) ==
               "invalid prompt template: template: prompt:1:3: unexpected {{end}} at byte 2"
    end

    test "prepends it to a render failure's message, variable and position intact" do
      {:error, cause} = Template.render("Hello {{.Nope}}", %{name: "me"})

      assert Error.message({:invalid_prompt, cause}) ==
               ~s(invalid prompt template: template: prompt:1:9: ) <>
                 ~s(executing "prompt" at <.Nope>: map has no entry for key "Nope")
    end

    test "refuses a cause that is not an exception" do
      assert_raise ArgumentError, fn -> Error.message({:invalid_prompt, "a string"}) end
    end
  end

  # The store half of the same fold-in. Its reasons are descriptive tuples
  # rather than sentinels, so the check that matters is that every tag the
  # store tree actually builds has a clause here -- an untagged one would blow
  # up in the catch-all the first time anything rendered it.
  @store_reasons [
    {:db_directory_failed, "/nope", :enotdir},
    {:db_open_failed, "/nope/tend.db", "unable to open database file"},
    {:pragma_failed, "PRAGMA journal_mode = WAL", "disk I/O error"},
    {:migration_failed, :up, 7, "add_workflows", "no such table: tasks"},
    {:query_failed, "SELECT 1", "database is locked"},
    {:data_version_failed, :timeout}
  ]

  # The cause is the last element of every store reason.
  defp put_cause(reason, cause) do
    put_elem(reason, tuple_size(reason) - 1, cause)
  end

  describe "message/1 renders the store's reasons" do
    test "one clause per reason, each naming what was being done" do
      assert Error.message({:db_directory_failed, "/tmp/x", :enotdir}) ==
               "creating db directory /tmp/x: enotdir"

      assert Error.message({:db_open_failed, "/tmp/x/tend.db", "unable to open database file"}) ==
               "opening db /tmp/x/tend.db: unable to open database file"

      assert Error.message({:pragma_failed, "PRAGMA foreign_keys = ON", "disk I/O error"}) ==
               "applying PRAGMA foreign_keys = ON: disk I/O error"

      assert Error.message({:migration_failed, :up, 7, "add_workflows", "no such table"}) ==
               "migrating up 7_add_workflows: no such table"

      assert Error.message({:query_failed, "SELECT 1", "database is locked"}) ==
               "running SELECT 1: database is locked"

      assert Error.message({:data_version_failed, :timeout}) ==
               "reading PRAGMA data_version: timeout"
    end

    test "every reason the store tree builds has one" do
      # Scanned rather than listed: a tuple added to the store with no clause
      # here is exactly the drift this fold-in exists to stop, and it would
      # otherwise only surface when something rendered it.
      built =
        ["store.ex", "store/migrator.ex", "store/watcher.ex"]
        |> Enum.map(&Path.join(Path.expand("../../lib/tend", __DIR__), &1))
        |> Enum.map(&File.read!/1)
        |> Enum.flat_map(&Regex.scan(~r/\{:error, \{:(\w+),/, &1))
        |> Enum.map(fn [_whole, tag] -> String.to_atom(tag) end)
        |> Enum.uniq()
        |> Enum.sort()

      assert built == Enum.sort(Enum.map(@store_reasons, &elem(&1, 0)))
    end

    test "each renders whatever shape of cause the layer underneath hands back" do
      for reason <- @store_reasons, cause <- [:enoent, "a string", %RuntimeError{}, {:odd, 1}] do
        rendered = reason |> put_cause(cause) |> Error.message()
        assert is_binary(rendered) and rendered != ""
      end
    end

    @tag :tmp_dir
    test "a real Tend.Store.open/1 failure renders through message/1", %{tmp_dir: tmp_dir} do
      # Not a hypothetical tuple: a store opened beneath a regular file cannot
      # create its directory, and this is the reason it returns.
      blocker = Path.join(tmp_dir, "not-a-directory")
      File.write!(blocker, "")
      path = Path.join([blocker, "db", "tend.db"])

      assert {:error, {:db_directory_failed, dir, cause} = reason} = Store.open(path)
      assert dir == Path.dirname(path)
      assert Error.message(reason) == "creating db directory #{dir}: #{cause}"
    end
  end

  # The value message/1 interpolated, with the fixed text stripped off.
  defp quoted(s) do
    "invalid date " <> rest = Error.message({:invalid_date, s})
    String.replace_suffix(rest, " (want YYYY-MM-DD)", "")
  end

  # Every expectation below is literal output of
  # fmt.Errorf("invalid date %q (want YYYY-MM-DD)", s) on the same input, run
  # against go1.26. They are the cases where inspect/1 -- the obvious but
  # wrong stand-in for %q -- disagrees with it.
  describe "message/1 quotes an interpolated value the way Go's %q does" do
    test "leaves an interpolation marker alone" do
      assert quoted("a" <> <<?#, ?{>> <> "b}") == "\"a" <> <<?#, ?{>> <> "b}\""
    end

    test "renders a control character as a quoted string, not a binary literal" do
      assert quoted(<<0>>) == ~S("\x00")
      assert quoted(<<0x7F>>) == ~S("\x7f")
    end

    test "renders a non-ASCII non-printable as a quoted escape" do
      assert quoted(<<0x85::utf8>>) == ~S("\u0085")
      assert quoted(<<0xA0::utf8>>) == ~S("\u00a0")
      assert quoted(<<0x2028::utf8>>) == ~S("\u2028")
      assert quoted(<<0xE0001::utf8>>) == ~S("\U000e0001")
    end

    test "uses Go's escape spelling, not Elixir's" do
      assert quoted(<<0x1B>>) == ~S("\x1b")
    end

    test "uses the seven escapes Go names" do
      assert quoted(<<7, 8, 9, 10, 11, 12, 13>>) == ~S("\a\b\t\n\v\f\r")
    end

    test "backslashes the quote and the backslash" do
      assert quoted(~S(a"b\c)) == ~S("a\"b\\c")
    end

    test "passes printable text through, ASCII or not" do
      assert quoted("café") == ~s("café")
      assert quoted("😀") == ~s("😀")
    end

    test "renders a byte that is not valid UTF-8 one \\xNN at a time" do
      assert quoted(<<0xFF, 0xFE>>) == ~S("\xff\xfe")
      assert quoted(<<0xC3, ?(>>) == ~S("\xc3(")
    end
  end

  # The same verb, reached directly: Tend.Workflow.Graph quotes an outcome into
  # a problem message with it, and a problem is not an error.
  describe "quote_go/1" do
    test "is the verb message/1 applies, callable on its own" do
      for value <- ["approve", ~S(a"b\c), <<0x1B>>, "café", <<0xFF>>] do
        assert Error.quote_go(value) == quoted(value)
      end
    end
  end

  describe "the convention" do
    test "the modules ported so far return sentinels, not strings or exceptions" do
      assert Tend.Task.normalize_title("") == {:error, :empty_title}
      assert Tend.Task.Project.normalize_name(" ") == {:error, :empty_project_name}
    end

    test "every sentinel a ported function can return is one this module lists" do
      {:error, title} = Tend.Task.normalize_title("")
      {:error, name} = Tend.Task.Project.normalize_name(" ")

      for reason <- [title, name], do: assert(Error.sentinel?(reason))
    end
  end
end
