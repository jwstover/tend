defmodule Tend.Store.ProjectCwdTest do
  @moduledoc """
  `set_project_cwd/3`, against a real temp database.

  A port of `internal/store/project_cwd_test.go`: a project's default cwd
  round-trips through every read path, is normalized on the way in, and a
  blank value clears it (tend task #190).
  """

  use ExUnit.Case, async: true

  alias Tend.Store
  alias Tend.Task.Project

  @moduletag :tmp_dir

  setup %{tmp_dir: dir} do
    {:ok, store} = Store.open(Path.join(dir, "tend.db"))
    on_exit(fn -> Store.close(store) end)
    %{store: store}
  end

  test "round-trips through get_project/2, project_by_name/2 and list_projects/1, and clears", %{
    store: store
  } do
    {:ok, project} = Store.create_project(store, "app")
    assert project.cwd == ""

    # Trailing whitespace and a redundant slash are typing noise, not part of
    # the path.
    assert Store.set_project_cwd(store, project.id, "  /tmp/app/  ") == :ok

    assert {:ok, got} = Store.get_project(store, project.id)
    assert got.cwd == "/tmp/app"

    assert {:ok, by_name} = Store.project_by_name(store, "app")
    assert by_name.cwd == "/tmp/app"

    assert {:ok, projects} = Store.list_projects(store)
    assert %Project{cwd: "/tmp/app"} = Enum.find(projects, &(&1.id == project.id))

    assert Store.set_project_cwd(store, project.id, "   ") == :ok
    assert {:ok, got} = Store.get_project(store, project.id)
    assert got.cwd == ""
  end

  test "the seeded default project has no cwd", %{store: store} do
    assert {:ok, default} = Store.get_project(store, Project.default_id())
    assert default.cwd == ""
  end
end
