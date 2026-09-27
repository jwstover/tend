defmodule Tend.Store.HierarchyTest do
  @moduledoc """
  The task hierarchy and project assignment, against a real temp database.

  A port of `internal/store/parent_test.go` plus the project-move cases of
  `internal/store/projects_test.go` (`TestSetProjectMovesWholeSubtree`,
  `TestAddChildInheritsParentProject`,
  `TestSetProjectLogsOneEventForTheWholeSubtree`,
  `TestSetProjectToTheSameProjectIsANoop`,
  `TestProjectEventSurvivesRenamingTheProject`).
  `internal/store/project_cwd_test.go` has no project-*move* case in it -- it
  covers `SetProjectCwd`, which belongs to the projects part of the port.

  `CHILD-ROW-PARITY.md` in the repo root is about how the TUI *draws* a
  sub-task, not about what the store holds; its one claim on this layer is that
  a sub-task is a whole task, which `list_children/2` above asserts directly.

  `project!/2` and `rename_project!/3` go through `Store.create_project/2` and
  `Store.rename_project/3` now that the projects part of the port exists; the
  event fixture is still raw SQL, the way `Tend.Store.TasksTest` writes its
  sessions and runs, because `ListEvents` is a later part of the port.
  """

  use ExUnit.Case, async: true

  alias Tend.Error
  alias Tend.Store
  alias Tend.Task.Event
  alias Tend.Task.Project
  alias Tend.Test.SQL

  @moduletag :tmp_dir

  setup %{tmp_dir: dir} do
    {:ok, store} = Store.open(Path.join(dir, "tend.db"))
    on_exit(fn -> Store.close(store) end)
    %{store: store}
  end

  defp add!(store, title) do
    {:ok, task} = Store.add_task(store, title)
    task
  end

  defp add_in!(store, project_id, title) do
    {:ok, task} = Store.add_task_in(store, project_id, title)
    task
  end

  defp child!(store, parent, title) do
    {:ok, task} = Store.add_child(store, parent.id, title)
    task
  end

  defp get!(store, id) do
    {:ok, task} = Store.get_task(store, id)
    task
  end

  defp project!(store, name) do
    {:ok, project} = Store.create_project(store, name)
    project.id
  end

  defp rename_project!(store, id, name) do
    :ok = Store.rename_project(store, id, name)
  end

  # ListEvents is the events part of the store port; the rows are read
  # straight out of the table, oldest first, as `eventsSince` reads them.
  defp events(store) do
    SQL.rows!(
      store.conn,
      "SELECT task_id, task_title, kind, old_value, new_value FROM task_events ORDER BY id"
    )
  end

  defp events_of(store, kind) do
    kind = Event.format_kind(kind)
    for [_id, _title, ^kind, _old, _new] = row <- events(store), do: row
  end

  # The whole ledger as kinds, oldest first -- what "row for row" means when
  # the question is which events an operation left behind, trigger-written
  # ones included.
  defp kinds(store) do
    for [_id, _title, kind, _old, _new] <- events(store), do: kind
  end

  defp snapshot(store) do
    SQL.rows!(
      store.conn,
      "SELECT id, title, parent_id, project_id FROM tasks ORDER BY id"
    )
  end

  describe "add_child/3" do
    test "hangs a sub-task off its parent, oldest first", %{store: store} do
      parent = add!(store, "parent")
      first = child!(store, parent, "first")
      second = child!(store, parent, "second")

      assert first.parent_id == parent.id
      assert first.state == :inbox
      assert {:ok, kids} = Store.list_children(store, parent.id)
      assert Enum.map(kids, & &1.id) == [first.id, second.id]
    end

    # A sub-task can never sit in a different project from its parent, so the
    # insert copies the parent's project rather than defaulting --
    # TestAddChildInheritsParentProject.
    test "inherits the parent's project rather than the default", %{store: store} do
      elsewhere = project!(store, "parent project")
      parent = add_in!(store, elsewhere, "parent")

      assert child!(store, parent, "child").project_id == elsewhere
    end

    test "normalizes the title and refuses a blank one", %{store: store} do
      parent = add!(store, "parent")

      assert child!(store, parent, "  trimmed ").title == "trimmed"
      assert Store.add_child(store, parent.id, "   ") == {:error, :empty_title}
    end

    # The insert is an INSERT ... SELECT, so a parent that is not there matches
    # no row and RETURNING yields none. Go's QueryRow.Scan reports that as
    # sql.ErrNoRows wrapped in "inserting sub-task of %d".
    test "a parent that does not exist is an error, and writes nothing", %{store: store} do
      assert {:error, reason} = Store.add_child(store, 404, "orphan")

      assert Error.message(reason) ==
               "inserting sub-task of 404: no rows in result set"

      assert SQL.scalar!(store.conn, "SELECT COUNT(*) FROM tasks") == 0
    end
  end

  describe "list_children/2" do
    test "is one level deep, and empty for a leaf", %{store: store} do
      root = add!(store, "root")
      mid = child!(store, root, "mid")
      child!(store, mid, "leaf")

      assert {:ok, [only]} = Store.list_children(store, root.id)
      assert only.id == mid.id
      assert Store.list_children(store, mid.id) |> elem(1) |> length() == 1
      assert Store.list_children(store, 404) == {:ok, []}
    end

    # CHILD-ROW-PARITY.md's premise: "a sub-task is a full task.Task. It
    # carries tags, a due date, a priority, its own sub-tasks and its own agent
    # sessions -- the child row just doesn't show most of it." That is a
    # rendering gap in the TUI (Phase 2), not a storage one, and this pins the
    # storage half so the port cannot quietly become the reason for it.
    test "hands back whole tasks, not the subset a child row draws", %{store: store} do
      root = add!(store, "root")
      kid = child!(store, root, "kid")
      child!(store, kid, "grandkid")
      :ok = Store.set_due(store, kid.id, "2026-12-01")
      :ok = Store.set_priority(store, kid.id, 1)

      assert {:ok, [only]} = Store.list_children(store, root.id)
      assert only.due == "2026-12-01"
      assert only.priority == 1
      assert only.state == :inbox
      assert only.project_id == Project.default_id()
      assert {:ok, [_grandkid]} = Store.list_children(store, only.id)
    end
  end

  describe "set_parent/3" do
    # TestSetParentPromotesChildToTopLevel.
    test "promotes a child to the top level and logs the parent it left", %{store: store} do
      parent = add!(store, "parent")
      child = child!(store, parent, "child")

      assert Store.set_parent(store, child.id, nil) == :ok
      assert get!(store, child.id).parent_id == nil

      assert [[task_id, "child", "parent", "parent", to]] = events_of(store, :parent)
      assert task_id == child.id
      assert to == Event.top_level_label()
    end

    # TestSetParentDemotesTopLevelUnderAnother.
    test "demotes a top-level task under another", %{store: store} do
      host = add!(store, "host")
      moved = add!(store, "moved")

      assert Store.set_parent(store, moved.id, host.id) == :ok
      assert get!(store, moved.id).parent_id == host.id
      assert {:ok, [only]} = Store.list_children(store, host.id)
      assert only.id == moved.id

      assert [[_id, "moved", "parent", from, "host"]] = events_of(store, :parent)
      assert from == Event.top_level_label()
    end

    # TestSetParentMovesBetweenParentsWithSubtree: only the one edge changes,
    # and the sub-tree rides along attached to the task that moved.
    test "moves between parents carrying the sub-tree", %{store: store} do
      a = add!(store, "a")
      b = add!(store, "b")
      mid = child!(store, a, "mid")
      leaf = child!(store, mid, "leaf")

      assert Store.set_parent(store, mid.id, b.id) == :ok
      assert get!(store, mid.id).parent_id == b.id
      assert get!(store, leaf.id).parent_id == mid.id
      assert Store.list_children(store, a.id) == {:ok, []}

      assert [[_id, "mid", "parent", "a", "b"]] = events_of(store, :parent)
    end

    # TestSetParentCrossProjectReprojectsSubtree.
    test "re-projects the whole sub-tree onto the new parent's project", %{store: store} do
      other = project!(store, "elsewhere")
      host = add_in!(store, other, "host")
      moved = add!(store, "moved")
      kid = child!(store, moved, "kid")
      grandkid = child!(store, kid, "grandkid")

      assert Store.set_parent(store, moved.id, host.id) == :ok

      for id <- [moved.id, kid.id, grandkid.id] do
        assert get!(store, id).project_id == other
      end

      # One parent event, and no project event: the project change is a
      # consequence of the move, not a second user action.
      assert length(events_of(store, :parent)) == 1
      assert events_of(store, :project) == []
    end

    # The criterion behind the two counts above, stated as the whole ledger:
    # the four captures each fire the `created` trigger, re-parenting fires no
    # trigger at all (trg_events_task_state is AFTER UPDATE OF state), and the
    # move itself adds exactly one row.
    test "leaves the event log matching Go's kind for kind", %{store: store} do
      other = project!(store, "elsewhere")
      host = add_in!(store, other, "host")
      moved = add!(store, "moved")
      kid = child!(store, moved, "kid")
      child!(store, kid, "grandkid")

      assert Store.set_parent(store, moved.id, host.id) == :ok

      assert kinds(store) == ~w(created created created created parent)
    end

    # TestSetParentPromoteKeepsProject.
    test "a promotion keeps the project the task already had", %{store: store} do
      other = project!(store, "elsewhere")
      parent = add_in!(store, other, "parent")
      child = child!(store, parent, "child")

      assert Store.set_parent(store, child.id, nil) == :ok
      assert get!(store, child.id).project_id == other
    end

    # TestSetParentRejectsSelf.
    test "refuses to make a task its own parent, and changes nothing", %{store: store} do
      a = add!(store, "a")
      before = snapshot(store)

      assert Store.set_parent(store, a.id, a.id) == {:error, {:own_parent, a.id}}
      assert snapshot(store) == before
      assert events_of(store, :parent) == []
    end

    # TestSetParentRejectsDescendant -- a direct child and a deeper descendant
    # alike.
    test "refuses a move under one of its own descendants", %{store: store} do
      root = add!(store, "root")
      mid = child!(store, root, "mid")
      leaf = child!(store, mid, "leaf")
      before = snapshot(store)

      for bad <- [mid, leaf] do
        assert Store.set_parent(store, root.id, bad.id) ==
                 {:error, {:own_subtask, root.id, bad.id}}
      end

      assert snapshot(store) == before
      assert events_of(store, :parent) == []
    end

    # TestSetParentUnchangedWritesNothing: both the "already there" cases,
    # mirroring the state trigger's OLD <> NEW guard.
    test "a move to where the task already is writes nothing", %{store: store} do
      parent = add!(store, "parent")
      child = child!(store, parent, "child")
      top = add!(store, "top")
      before = snapshot(store)

      assert Store.set_parent(store, child.id, parent.id) == :ok
      assert Store.set_parent(store, top.id, nil) == :ok

      assert snapshot(store) == before
      assert events_of(store, :parent) == []
    end

    # TestSetParentMissingParentErrors.
    test "a parent that does not exist is an error, and rolls back", %{store: store} do
      a = add!(store, "a")
      missing = a.id + 1000
      before = snapshot(store)

      assert {:error, {:query_failed, context, _reason}} =
               Store.set_parent(store, a.id, missing)

      assert context == "loading parent task #{missing}"
      assert snapshot(store) == before
      assert events_of(store, :parent) == []
    end

    # TestSetParentMissingTaskErrors.
    test "a task that does not exist is an error", %{store: store} do
      assert Store.set_parent(store, 9999, nil) == {:error, {:task_not_found, 9999}}
    end
  end

  describe "set_project/3" do
    # TestSetProjectMovesWholeSubtree, three levels deep: the walk replaces a
    # recursive CTE, so depth is exactly what could regress.
    test "moves the whole sub-tree", %{store: store} do
      dest = project!(store, "destination")
      root = add!(store, "root")
      child = child!(store, root, "child")
      grandchild = child!(store, child, "grandchild")

      assert Store.set_project(store, root.id, dest) == :ok

      for id <- [root.id, child.id, grandchild.id] do
        assert get!(store, id).project_id == dest
      end
    end

    # TestSetProjectLogsOneEventForTheWholeSubtree.
    test "logs one event for the whole sub-tree, naming both projects", %{store: store} do
      dest = project!(store, "destination")
      root = add!(store, "root")
      child = child!(store, root, "child")
      child!(store, child, "grandchild")

      assert Store.set_project(store, root.id, dest) == :ok

      assert [[task_id, "root", "project", "Unsorted", "destination"]] =
               events_of(store, :project)

      assert task_id == root.id

      # And nothing else: the two descendants moved without a row apiece, which
      # is the whole reason this event is written here rather than by a trigger.
      assert kinds(store) == ~w(created created created project)
    end

    # TestSetProjectToTheSameProjectIsANoop.
    test "a move to where the task already is writes nothing", %{store: store} do
      created = add!(store, "staying put")
      before = snapshot(store)

      assert Store.set_project(store, created.id, Project.default_id()) == :ok
      assert snapshot(store) == before
      assert events_of(store, :project) == []
    end

    # TestProjectEventSurvivesRenamingTheProject: the names are snapshotted,
    # the way task_events snapshots task_title.
    test "the event survives renaming the project it names", %{store: store} do
      dest = project!(store, "original name")
      created = add!(store, "moved")

      assert Store.set_project(store, created.id, dest) == :ok
      rename_project!(store, dest, "renamed later")

      assert [[_id, _title, _kind, "Unsorted", "original name"]] = events_of(store, :project)
    end

    test "a project that does not exist is an error, and rolls back", %{store: store} do
      root = add!(store, "root")
      child!(store, root, "child")
      before = snapshot(store)

      assert {:error, {:query_failed, "loading project 404", _reason}} =
               Store.set_project(store, root.id, 404)

      # tasks.project_id carries no foreign key, so the UPDATE went through
      # before projectName refused: the rollback is what puts it back.
      assert snapshot(store) == before
      assert events_of(store, :project) == []
    end

    test "a task that does not exist is an error", %{store: store} do
      dest = project!(store, "destination")
      assert Store.set_project(store, 9999, dest) == {:error, {:task_not_found, 9999}}
    end
  end

  describe "the sub-tree walk" do
    # subtreeIDs is breadth-first over ListChildIDs, and it is the part of
    # SetProject and SetParent with no SQL of its own to check. A wide, deep
    # and lopsided tree exercises the queue in a way the three-node fixtures
    # above do not.
    test "carries every descendant at every depth", %{store: store} do
      dest = project!(store, "destination")
      root = add!(store, "root")

      ids =
        for branch <- 1..3, reduce: [root.id] do
          acc ->
            top = child!(store, root, "branch #{branch}")

            deep =
              for depth <- 1..branch, reduce: {top, []} do
                {parent, seen} ->
                  node = child!(store, parent, "branch #{branch} depth #{depth}")
                  {node, [node.id | seen]}
              end

            acc ++ [top.id | elem(deep, 1)]
        end

      assert Store.set_project(store, root.id, dest) == :ok

      for id <- ids do
        assert get!(store, id).project_id == dest
      end

      # A sibling tree is untouched: the walk starts at the root it was given.
      assert length(events_of(store, :project)) == 1
    end
  end
end
