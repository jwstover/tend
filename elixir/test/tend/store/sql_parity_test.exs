defmodule Tend.Store.SQLParityTest do
  @moduledoc """
  Every statement `Tend.Store` holds in a module attribute, diffed against the
  sqlc-generated constant it was copied from.

  sqlc has no Elixir counterpart, so the port's half of the SQL is hand-typed;
  the whole argument for typing it verbatim is that a reviewer can diff the two
  by eye. This does the diff mechanically, so a query that drifts -- on either
  side -- is a test failure rather than something a reviewer has to catch.

  It reads the Go tree directly, the way `Tend.GoParityTest` reads the Go
  sources for their error sentinels. No Go toolchain is needed: these are
  source files, not a build.
  """

  use ExUnit.Case, async: true

  alias Tend.Template.Parity.Corpus

  # Elixir attribute name => the Go constant in internal/store/gen/*.sql.go.
  @pairs %{
    "create_task" => "createTask",
    "create_task_with_body" => "createTaskWithBody",
    "get_task" => "getTask",
    "list_live_tasks" => "listLiveTasks",
    "list_live_with_completed_tasks" => "listLiveWithCompletedTasks",
    "list_inbox_tasks" => "listInboxTasks",
    "list_project_tasks" => "listProjectTasks",
    "count_inbox_tasks" => "countInboxTasks",
    "list_child_counts" => "listChildCounts",
    "list_session_statuses" => "listSessionStatuses",
    "list_active_runs" => "listActiveRuns",
    "set_task_state" => "setTaskState",
    "set_task_priority" => "setTaskPriority",
    "set_task_due" => "setTaskDue",
    "set_task_title" => "setTaskTitle",
    "set_task_body" => "setTaskBody",
    "append_task_body" => "appendTaskBody",
    "delete_task" => "deleteTask"
  }

  # The one statement that is not a byte-for-byte copy. sqlc folded the
  # terminator of the query declared before it in queries/tasks.sql into this
  # one's text, and `sqlite3_prepare` compiles the *first* statement it is
  # handed -- an empty one compiles to nothing at all, so the port drops it.
  # See the comment above @set_task_state.
  @go_preamble %{"set_task_state" => ";\n\n"}

  @gen_files ~w(tasks sessions workflows)

  defp store_source do
    Corpus.repo_root() |> Path.join("elixir/lib/tend/store.ex") |> File.read!()
  end

  defp gen_source do
    root = Corpus.repo_root()

    Enum.map_join(@gen_files, "\n", fn name ->
      root |> Path.join("internal/store/gen/#{name}.sql.go") |> File.read!()
    end)
  end

  # The heredoc's body, with the two-space module indentation stripped back
  # off -- what actually reaches sqlite3_prepare.
  defp elixir_statement(source, attr) do
    case Regex.run(~r/^  @#{attr} """\n(.*?)^  """$/ms, source) do
      [_, body] -> body |> String.replace(~r/^  /m, "") |> String.trim()
      nil -> flunk("Tend.Store has no @#{attr} heredoc")
    end
  end

  # The raw constant body, preamble and all.
  defp go_raw(source, const) do
    case Regex.run(~r/^const #{const} = `-- name: \w+ :\w+\n(.*?)^`$/ms, source) do
      [_, body] -> body
      nil -> flunk("internal/store/gen has no constant #{const}")
    end
  end

  # The same body with the one documented preamble taken off, which is what
  # the port copied.
  defp go_statement(source, const, attr) do
    preamble = Map.get(@go_preamble, attr, "")

    source
    |> go_raw(const)
    |> String.replace_prefix(preamble, "")
    |> String.trim()
  end

  test "the store's inline SQL is the generated SQL, statement for statement" do
    ex = store_source()
    go = gen_source()

    for {attr, const} <- @pairs do
      assert elixir_statement(ex, attr) == go_statement(go, const, attr),
             "@#{attr} has drifted from #{const} in internal/store/gen"
    end
  end

  test "the one statement that is not a verbatim copy still needs its exception" do
    go = gen_source()

    for {attr, preamble} <- @go_preamble do
      const = Map.fetch!(@pairs, attr)

      assert String.starts_with?(go_raw(go, const), preamble),
             "#{const} no longer carries the stray #{inspect(preamble)} the port drops; " <>
               "delete it from @go_preamble so the copy is checked byte for byte"
    end
  end

  test "every statement Tend.Store holds is one this test knows about" do
    # Every module attribute written as a heredoc, minus the documentation
    # ones -- what is left is the SQL.
    attrs =
      ~r/^  @(\w+) """\n/m
      |> Regex.scan(store_source(), capture: :all_but_first)
      |> List.flatten()
      |> Enum.reject(&(&1 in ~w(moduledoc doc typedoc)))

    assert MapSet.new(attrs) == MapSet.new(Map.keys(@pairs)),
           "a SQL heredoc was added to or removed from Tend.Store without pairing it " <>
             "with its generated constant here"
  end
end
