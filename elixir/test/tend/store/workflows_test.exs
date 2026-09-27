defmodule Tend.Store.WorkflowsTest do
  @moduledoc """
  The workflow definition surface -- the `workflows` rows -- against a real
  temp database.

  A port of the workflow-row cases in `internal/store/workflows_test.go`:
  `TestWorkflowCRUD`, `TestListWorkflowsCountsSteps`, the workflow half of
  `TestDeleteWorkflowRefusedWhileARunIsLive`, and
  `TestDuplicateWorkflowCopiesStepsAndRemapsEdges`.

  Go's cases build their fixtures with `AddStep`, `SetEdge` and `CreateRun`.
  Those are a later part of the store port, so the fixtures here are written
  straight into the tables those methods write. Reading them back is raw SQL
  too, deliberately: an assertion that the copy's edges point inside the copy
  should look at the rows, not at a mapper that could agree with the writer
  about the same mistake.
  """

  use ExUnit.Case, async: true

  alias Tend.Error
  alias Tend.Store
  alias Tend.Test.SQL

  @moduletag :tmp_dir

  setup %{tmp_dir: dir} do
    {:ok, store} = Store.open(Path.join(dir, "tend.db"))
    on_exit(fn -> Store.close(store) end)
    %{store: store}
  end

  # -- fixtures, in the tables the step and edge API will own ----------------

  defp workflow!(store, name, description \\ "") do
    {:ok, workflow} = Store.create_workflow(store, name, description)
    workflow
  end

  # AddStep's row: appended after every existing step, prompt/model/mode empty
  # unless the case says otherwise.
  defp step!(store, workflow_id, name, opts \\ []) do
    sort =
      SQL.scalar!(
        store.conn,
        "SELECT COALESCE(MAX(sort_order) + 1, 0) FROM workflow_steps WHERE workflow_id = #{workflow_id}"
      )

    SQL.exec!(store.conn, """
    INSERT INTO workflow_steps (workflow_id, name, kind, prompt_md, model, permission_mode, sort_order)
    VALUES (
      #{workflow_id},
      '#{name}',
      '#{Keyword.get(opts, :kind, "agent")}',
      '#{Keyword.get(opts, :prompt_md, "")}',
      '#{Keyword.get(opts, :model, "")}',
      '#{Keyword.get(opts, :permission_mode, "")}',
      #{sort}
    )
    """)

    SQL.scalar!(store.conn, "SELECT last_insert_rowid()")
  end

  defp edge!(store, from_step_id, outcome, to_step_id, max_iterations \\ nil) do
    SQL.exec!(store.conn, """
    INSERT INTO workflow_edges (from_step_id, outcome, to_step_id, max_iterations)
    VALUES (#{from_step_id}, '#{outcome}', #{to_step_id}, #{max_iterations || "NULL"})
    """)
  end

  defp run!(store, workflow_id, state \\ "pending") do
    {:ok, task} = Store.add_task(store, "task under test")

    SQL.exec!(store.conn, """
    INSERT INTO workflow_runs (workflow_id, task_id, cwd, state)
    VALUES (#{workflow_id}, #{task.id}, '/tmp/work', '#{state}')
    """)

    {SQL.scalar!(store.conn, "SELECT last_insert_rowid()"), task}
  end

  defp step_run!(store, run_id, step_id) do
    SQL.exec!(store.conn, """
    INSERT INTO workflow_step_runs (run_id, step_id) VALUES (#{run_id}, #{step_id})
    """)
  end

  defp count(store, table, where) do
    SQL.scalar!(store.conn, "SELECT COUNT(*) FROM #{table} WHERE #{where}")
  end

  # A workflow's steps as plain tuples, so two workflows can be compared
  # structurally without their ids getting in the way.
  defp step_shapes(store, workflow_id) do
    SQL.rows!(store.conn, """
    SELECT name, kind, prompt_md, model, permission_mode, sort_order
    FROM workflow_steps WHERE workflow_id = #{workflow_id}
    ORDER BY sort_order, id
    """)
  end

  defp step_ids(store, workflow_id) do
    store.conn
    |> SQL.rows!(
      "SELECT id FROM workflow_steps WHERE workflow_id = #{workflow_id} ORDER BY sort_order, id"
    )
    |> Enum.map(&hd/1)
  end

  # Every edge of a workflow with its endpoints given as *positions* in the
  # step list rather than ids -- which is exactly what "structurally identical"
  # means for a copy.
  defp edge_shapes(store, workflow_id) do
    positions = store |> step_ids(workflow_id) |> Enum.with_index() |> Map.new()

    store.conn
    |> SQL.rows!("""
    SELECT e.from_step_id, e.outcome, e.to_step_id, e.max_iterations
    FROM workflow_edges e
    JOIN workflow_steps s ON s.id = e.from_step_id
    WHERE s.workflow_id = #{workflow_id}
    ORDER BY s.sort_order, s.id, e.outcome
    """)
    |> Enum.map(fn [from, outcome, to, max] ->
      {Map.fetch(positions, from), outcome, Map.fetch(positions, to), max}
    end)
  end

  describe "create_workflow/3" do
    test "trims the name and keeps the description", %{store: store} do
      assert {:ok, workflow} = Store.create_workflow(store, "  fix a bug  ", "the usual")
      assert workflow.name == "fix a bug"
      assert workflow.description == "the usual"
      assert workflow.step_count == 0
      assert %DateTime{} = workflow.created_at
      assert %DateTime{} = workflow.updated_at
    end

    test "refuses a blank name with the same sentinel Go does", %{store: store} do
      assert Store.create_workflow(store, "   ", "") == {:error, :empty_name}
      assert Store.create_workflow(store, "", "") == {:error, :empty_name}
    end

    test "refuses a case-variant duplicate, because the column is NOCASE", %{store: store} do
      workflow!(store, "fix a bug")

      assert {:error, {:query_failed, context, reason}} =
               Store.create_workflow(store, "FIX A BUG", "")

      assert context == ~s(creating workflow "FIX A BUG")
      assert reason =~ "UNIQUE constraint failed: workflows.name"
      assert count(store, "workflows", "1 = 1") == 1
    end
  end

  describe "get_workflow/2 and workflow_by_name/2" do
    test "resolve the same workflow, the name case-insensitively", %{store: store} do
      workflow = workflow!(store, "fix a bug")

      assert {:ok, found} = Store.workflow_by_name(store, "Fix A Bug")
      assert found.id == workflow.id
      assert {:ok, ^found} = Store.get_workflow(store, workflow.id)
    end

    test "trim the name before looking it up, and refuse a blank one", %{store: store} do
      workflow = workflow!(store, "fix a bug")

      assert {:ok, found} = Store.workflow_by_name(store, "  fix a bug\n")
      assert found.id == workflow.id
      assert Store.workflow_by_name(store, "  ") == {:error, :empty_name}
    end

    test "report a miss as the not-found sentinel", %{store: store} do
      assert Store.workflow_by_name(store, "nope") == {:error, :workflow_not_found}
      assert Store.get_workflow(store, 9999) == {:error, :workflow_not_found}
    end
  end

  describe "rename_workflow/3 and set_workflow_description/3" do
    test "write the row, normalizing the name the way create does", %{store: store} do
      workflow = workflow!(store, "fix a bug")

      assert :ok = Store.rename_workflow(store, workflow.id, "  fix a bug, carefully  ")
      assert :ok = Store.set_workflow_description(store, workflow.id, "slower")
      assert {:ok, got} = Store.get_workflow(store, workflow.id)
      assert got.name == "fix a bug, carefully"
      assert got.description == "slower"
    end

    test "refuse a blank name and a duplicate one", %{store: store} do
      workflow = workflow!(store, "fix a bug")
      workflow!(store, "ship it")

      assert Store.rename_workflow(store, workflow.id, " \t ") == {:error, :empty_name}

      assert {:error, {:query_failed, "renaming workflow " <> _, reason}} =
               Store.rename_workflow(store, workflow.id, "SHIP IT")

      assert reason =~ "UNIQUE constraint failed: workflows.name"
      assert {:ok, unchanged} = Store.get_workflow(store, workflow.id)
      assert unchanged.name == "fix a bug"
    end
  end

  describe "list_workflows/1" do
    test "orders by name and carries each one's step count", %{store: store} do
      two = workflow!(store, "two steps")
      step!(store, two.id, "a")
      step!(store, two.id, "b")
      workflow!(store, "empty")

      assert {:ok, [first, second]} = Store.list_workflows(store)
      assert {first.name, first.step_count} == {"empty", 0}
      assert {second.name, second.step_count} == {"two steps", 2}
    end

    test "is empty on a fresh database", %{store: store} do
      assert Store.list_workflows(store) == {:ok, []}
    end
  end

  describe "delete_workflow/2" do
    test "takes the steps, edges and runs with it, and leaves the task", %{store: store} do
      workflow = workflow!(store, "wf")
      first = step!(store, workflow.id, "implement")
      second = step!(store, workflow.id, "review")
      edge!(store, first, "done", second)
      edge!(store, second, "reject", first, 2)
      {run_id, task} = run!(store, workflow.id, "done")
      step_run!(store, run_id, first)

      assert :ok = Store.delete_workflow(store, workflow.id)

      assert Store.get_workflow(store, workflow.id) == {:error, :workflow_not_found}
      assert count(store, "workflows", "id = #{workflow.id}") == 0
      assert count(store, "workflow_steps", "workflow_id = #{workflow.id}") == 0
      assert count(store, "workflow_edges", "from_step_id IN (#{first}, #{second})") == 0
      assert count(store, "workflow_runs", "workflow_id = #{workflow.id}") == 0
      assert count(store, "workflow_step_runs", "run_id = #{run_id}") == 0
      # The run's task is not the run's to delete, and stays.
      assert {:ok, _task} = Store.get_task(store, task.id)
    end

    test "is refused, naming the run, while a run of it is live", %{store: store} do
      workflow = workflow!(store, "wf")
      only = step!(store, workflow.id, "only")
      {run_id, _task} = run!(store, workflow.id)
      step_run!(store, run_id, only)

      for state <- ~w(pending running waiting_review paused) do
        SQL.exec!(store.conn, "UPDATE workflow_runs SET state = '#{state}' WHERE id = #{run_id}")

        assert {:error, {:in_use, _what, ^run_id} = reason} =
                 Store.delete_workflow(store, workflow.id)

        assert reason == {:in_use, "workflow #{workflow.id}", run_id}
        # Go asserts the rendered message names the run, which is the point of
        # carrying the id rather than just the sentinel.
        assert Error.message(reason) =~ "run #{run_id}"
      end

      # Still there: the refusal rolled the transaction back.
      assert {:ok, _workflow} = Store.get_workflow(store, workflow.id)
      assert count(store, "workflow_steps", "workflow_id = #{workflow.id}") == 1
    end

    test "goes through once the run reaches a terminal state", %{store: store} do
      workflow = workflow!(store, "wf")
      {run_id, _task} = run!(store, workflow.id, "running")

      assert {:error, {:in_use, _what, ^run_id}} = Store.delete_workflow(store, workflow.id)

      SQL.exec!(store.conn, "UPDATE workflow_runs SET state = 'cancelled' WHERE id = #{run_id}")
      assert :ok = Store.delete_workflow(store, workflow.id)
      assert count(store, "workflow_runs", "id = #{run_id}") == 0
    end

    test "names the oldest live run when several are live", %{store: store} do
      workflow = workflow!(store, "wf")
      {oldest, _task} = run!(store, workflow.id, "running")
      {_newer, _task} = run!(store, workflow.id, "paused")

      assert {:error, {:in_use, _what, ^oldest}} = Store.delete_workflow(store, workflow.id)
    end

    test "deleting an id that is not there is not an error", %{store: store} do
      assert :ok = Store.delete_workflow(store, 9999)
    end
  end

  describe "duplicate_workflow/3" do
    setup %{store: store} do
      workflow = workflow!(store, "original", "desc")

      implement =
        step!(store, workflow.id, "implement",
          prompt_md: "Implement {{.Task.Title}}",
          model: "opus"
        )

      review = step!(store, workflow.id, "review", kind: "gate", permission_mode: "plan")
      edge!(store, implement, "done", review)
      edge!(store, review, "reject", implement, 2)

      %{workflow: workflow, implement: implement, review: review}
    end

    test "copies the description and reports the step count", %{store: store, workflow: w} do
      assert {:ok, copy} = Store.duplicate_workflow(store, w.id, "  copy  ")
      assert copy.name == "copy"
      assert copy.description == "desc"
      assert copy.step_count == 2
      assert copy.id != w.id
    end

    test "produces steps and edges structurally identical to the original", %{
      store: store,
      workflow: w
    } do
      assert {:ok, copy} = Store.duplicate_workflow(store, w.id, "copy")

      assert step_shapes(store, copy.id) == step_shapes(store, w.id)
      assert edge_shapes(store, copy.id) == edge_shapes(store, w.id)

      # Spelled out, so the shape comparison above cannot pass by agreeing on
      # the wrong thing: the loop-back edge kept its bound.
      assert [
               {{:ok, 0}, "done", {:ok, 1}, nil},
               {{:ok, 1}, "reject", {:ok, 0}, 2}
             ] = edge_shapes(store, copy.id)
    end

    test "every edge of the copy points inside the copy, and none back at the original", %{
      store: store,
      workflow: w,
      implement: implement,
      review: review
    } do
      assert {:ok, copy} = Store.duplicate_workflow(store, w.id, "copy")

      copied = MapSet.new(step_ids(store, copy.id))
      assert MapSet.size(copied) == 2
      assert MapSet.disjoint?(copied, MapSet.new([implement, review]))

      edges =
        SQL.rows!(store.conn, """
        SELECT e.from_step_id, e.to_step_id
        FROM workflow_edges e
        JOIN workflow_steps s ON s.id = e.from_step_id
        WHERE s.workflow_id = #{copy.id}
        """)

      assert length(edges) == 2

      for [from, to] <- edges do
        assert MapSet.member?(copied, from)
        assert MapSet.member?(copied, to)
      end
    end

    test "leaves the original untouched", %{store: store, workflow: w} do
      before_steps = step_shapes(store, w.id)
      before_edges = edge_shapes(store, w.id)

      assert {:ok, _copy} = Store.duplicate_workflow(store, w.id, "copy")

      assert step_shapes(store, w.id) == before_steps
      assert edge_shapes(store, w.id) == before_edges
      assert length(before_edges) == 2
    end

    test "refuses a blank, a taken and a missing name or id", %{store: store, workflow: w} do
      assert Store.duplicate_workflow(store, w.id, "  ") == {:error, :empty_name}

      assert {:error, {:query_failed, ~s(creating workflow "original"), reason}} =
               Store.duplicate_workflow(store, w.id, "original")

      assert reason =~ "UNIQUE constraint failed: workflows.name"

      assert Store.duplicate_workflow(store, 9999, "ghost") == {:error, :workflow_not_found}
    end

    test "a refusal leaves nothing behind: the copy is whole or absent", %{
      store: store,
      workflow: w
    } do
      steps_before = count(store, "workflow_steps", "1 = 1")

      assert {:error, _reason} = Store.duplicate_workflow(store, w.id, "original")

      assert count(store, "workflows", "1 = 1") == 1
      assert count(store, "workflow_steps", "1 = 1") == steps_before
    end

    test "copies a workflow with no steps and no edges", %{store: store} do
      empty = workflow!(store, "empty")

      assert {:ok, copy} = Store.duplicate_workflow(store, empty.id, "empty copy")
      assert copy.step_count == 0
      assert step_shapes(store, copy.id) == []
      assert edge_shapes(store, copy.id) == []
    end
  end
end
