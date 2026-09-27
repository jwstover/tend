defmodule Tend.Store.ProjectsTest do
  @moduledoc """
  The project rows and the `active_project_id` setting, against a real temp
  database.

  A port of the project-CRUD, delete-reassignment, live-count and
  active-project cases in `internal/store/projects_test.go`. The four
  `SetProject`/task-move cases there are `Tend.Store.HierarchyTest`'s; the
  three cases exercising `list_live/2`, `add_child/3` and `add_task/2` against
  a project other than the default are also `Tend.Store.HierarchyTest`'s,
  since they are really about the hierarchy and capture surfaces, not this
  one.
  """

  use ExUnit.Case, async: true

  alias Tend.Error
  alias Tend.Store
  alias Tend.Task.Project
  alias Tend.Test.SQL

  @moduletag :tmp_dir

  setup %{tmp_dir: dir} do
    {:ok, store} = Store.open(Path.join(dir, "tend.db"))
    on_exit(fn -> Store.close(store) end)
    %{store: store}
  end

  defp project!(store, name) do
    {:ok, project} = Store.create_project(store, name)
    project
  end

  test "create_project/2 trims the name, get_project/2 and project_by_name/2 read it back",
       %{store: store} do
    {:ok, project} = Store.create_project(store, "  tend  ")
    assert project.name == "tend"

    assert Store.create_project(store, "   ") == {:error, :empty_project_name}

    # Unique is NOCASE, so this is the same project: the driver's constraint
    # error, not a sentinel.
    assert {:error, {:query_failed, "creating project \"TEND\"", _}} =
             Store.create_project(store, "TEND")

    assert {:ok, found} = Store.project_by_name(store, "TeNd")
    assert found.id == project.id

    assert Store.project_by_name(store, "nope") == {:error, {:project_not_found, "nope"}}

    assert Error.message({:project_not_found, "nope"}) == ~s|project "nope": project not found|
  end

  test "get_project/2 on an id that is not there", %{store: store} do
    assert Store.get_project(store, 999) == {:error, {:project_not_found, 999}}
    assert Error.message({:project_not_found, 999}) == "project 999: project not found"
  end

  test "rename_project/3 and set_project_archived/3 round-trip through get_project/2", %{
    store: store
  } do
    project = project!(store, "tend")

    assert Store.rename_project(store, project.id, "tend-cli") == :ok
    assert {:ok, got} = Store.get_project(store, project.id)
    assert got.name == "tend-cli"
    refute Project.archived?(got)

    assert Store.set_project_archived(store, project.id, true) == :ok
    assert {:ok, got} = Store.get_project(store, project.id)
    assert Project.archived?(got)
    assert got.archived_at != nil

    assert Store.set_project_archived(store, project.id, false) == :ok
    assert {:ok, got} = Store.get_project(store, project.id)
    refute Project.archived?(got)
    assert got.archived_at == nil
  end

  # project_id carries no foreign key (migration 00007), so the store is what
  # stops a delete from orphaning tasks. This is that guarantee.
  test "delete_project/2 reassigns its tasks to the default project", %{store: store} do
    project = project!(store, "doomed")
    {:ok, stranded} = Store.add_task_in(store, project.id, "do not orphan me")

    assert Store.delete_project(store, project.id) == :ok

    assert {:ok, got} = Store.get_task(store, stranded.id)
    assert got.project_id == Project.default_id()

    assert Store.get_project(store, project.id) == {:error, {:project_not_found, project.id}}
  end

  test "delete_project/2 refuses the default project", %{store: store} do
    assert Store.delete_project(store, Project.default_id()) == {:error, :protected_project}
  end

  test "list_projects/1 counts only live top-level tasks", %{store: store} do
    project = project!(store, "counted")
    {:ok, parent} = Store.add_task_in(store, project.id, "top level")

    # A sub-task must not inflate the count: the number beside a project is
    # what selecting it puts on screen as rows.
    {:ok, _child} = Store.add_child(store, parent.id, "sub")

    # Nor should a completed one, which the live view filters out.
    {:ok, done} = Store.add_task_in(store, project.id, "finished")
    :ok = Store.set_state(store, done.id, :done)

    assert {:ok, projects} = Store.list_projects(store)
    assert %Project{live_count: 1} = Enum.find(projects, &(&1.id == project.id))
  end

  test "list_projects/1 includes archived projects, ordered by sort_order then name", %{
    store: store
  } do
    archived = project!(store, "archived one")
    :ok = Store.set_project_archived(store, archived.id, true)

    zeta = project!(store, "zeta")
    alpha = project!(store, "alpha")

    assert {:ok, projects} = Store.list_projects(store)
    assert Enum.any?(projects, &(&1.id == archived.id and Project.archived?(&1)))

    # Every project here shares sort_order 0 (nothing in this port yet sets
    # it), so the tie-break is name -- alpha before zeta regardless of
    # creation order.
    ids = Enum.map(projects, & &1.id)
    assert Enum.find_index(ids, &(&1 == alpha.id)) < Enum.find_index(ids, &(&1 == zeta.id))
  end

  test "active_project_id/1 and set_active_project/2 round-trip, with fallbacks", %{
    store: store
  } do
    # Never set: the default, not an error.
    assert Store.active_project_id(store) == {:ok, Project.default_id()}

    project = project!(store, "active")
    assert Store.set_active_project(store, project.id) == :ok
    assert Store.active_project_id(store) == {:ok, project.id}

    # Deleting the remembered project must not strand the TUI's restore.
    assert Store.delete_project(store, project.id) == :ok
    assert Store.active_project_id(store) == {:ok, Project.default_id()}

    # A corrupt stored value degrades the same way rather than failing: a bad
    # UI preference must not stop the TUI from starting. There is no public
    # setter for a non-numeric value, so this seeds the row directly -- the
    # row already exists from set_active_project/2 above, so it is an UPDATE.
    SQL.exec!(
      store.conn,
      "UPDATE settings SET value = 'not a number' WHERE key = 'active_project_id'"
    )

    assert Store.active_project_id(store) == {:ok, Project.default_id()}
  end
end
