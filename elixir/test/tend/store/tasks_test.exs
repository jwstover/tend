defmodule Tend.Store.TasksTest do
  @moduledoc """
  The flat task surface, against a real temp database.

  A port of the matching cases in `internal/store/store_test.go`. Where a
  fixture needs a write this part of the port does not own yet -- a sub-task,
  a session, a workflow run -- the test does it in raw SQL rather than waiting
  for the sub-task that adds the method, exactly as the Go tests drive
  `snooze_until` with `db.ExecContext` because the snooze mutator arrived
  later than the query that reads it.
  """

  use ExUnit.Case, async: true

  alias Tend.Store
  alias Tend.Task.ChildCount
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

  # The row CreateChildTask would write, and its id. AddChild belongs to a
  # later part of the store port; child_counts/1 and the cascade trigger need
  # children now.
  defp add_child!(store, parent, title) do
    SQL.exec!(store.conn, """
    INSERT INTO tasks (title, parent_id, project_id)
    SELECT '#{title}', p.id, p.project_id
    FROM tasks p
    WHERE p.id = #{parent.id}
    """)

    SQL.scalar!(store.conn, "SELECT id FROM tasks WHERE title = '#{title}'")
  end

  # CreateSession, SetSessionStatus, CreateWorkflow and CreateRun all belong to
  # later parts of the store port, so session_statuses/1's fixtures are written
  # straight into the tables those methods write.
  defp session!(store, task, external_id, status \\ nil) do
    SQL.exec!(store.conn, """
    INSERT INTO agent_sessions (task_id, external_id, cwd, label)
    VALUES (#{task.id}, '#{external_id}', '/tmp/work', '#{task.title}')
    """)

    if status do
      SQL.exec!(store.conn, """
      UPDATE agent_sessions
      SET status = '#{status}', status_updated_at = strftime('%Y-%m-%d %H:%M:%f', 'now')
      WHERE external_id = '#{external_id}'
      """)
    end
  end

  defp run!(store, task, state) do
    SQL.exec!(store.conn, "INSERT OR IGNORE INTO workflows (id, name) VALUES (1, 'ship it')")

    SQL.exec!(store.conn, """
    INSERT INTO workflow_runs (workflow_id, task_id, cwd, state)
    VALUES (1, #{task.id}, '/tmp/work', '#{state}')
    """)
  end

  defp titles(tasks), do: Enum.map(tasks, & &1.title)

  defp events(store) do
    SQL.rows!(
      store.conn,
      "SELECT task_id, task_title, kind, old_value, new_value FROM task_events ORDER BY id"
    )
  end

  describe "add_task/2" do
    test "defaults everything but the title", %{store: store} do
      assert {:ok, task} = Store.add_task(store, "  buy milk ")

      assert task.id > 0
      assert task.title == "buy milk"
      assert task.state == :inbox
      assert task.body_md == ""
      assert task.priority == nil
      assert task.due == nil
      assert task.snooze_until == nil
      assert task.parent_id == nil
      assert task.completed_at == nil
      assert task.project_id == Project.default_id()
      assert %DateTime{} = task.created_at
      assert %DateTime{} = task.updated_at
    end

    test "a blank title is refused, with the sentinel", %{store: store} do
      assert Store.add_task(store, "   ") == {:error, :empty_title}
      assert SQL.scalar!(store.conn, "SELECT COUNT(*) FROM tasks") == 0
    end
  end

  describe "add_task_with_body/3" do
    test "keeps the body and still normalizes the title", %{store: store} do
      link = "https://example.atlassian.net/browse/PROJ-1\n"

      assert {:ok, task} = Store.add_task_with_body(store, " PROJ-1: fix it ", link)
      assert task.title == "PROJ-1: fix it"
      assert task.body_md == link
      assert task.state == :inbox
      assert task.project_id == Project.default_id()
    end

    test "a blank title is refused", %{store: store} do
      assert Store.add_task_with_body(store, "   ", "body") == {:error, :empty_title}
    end
  end

  describe "add_task_in/3 and add_task_with_body_in/4" do
    test "capture into a named project", %{store: store} do
      SQL.exec!(store.conn, "INSERT INTO projects (id, name) VALUES (2, 'work')")

      assert {:ok, bare} = Store.add_task_in(store, 2, "in work")
      assert {:ok, bodied} = Store.add_task_with_body_in(store, 2, "also work", "# notes")

      assert bare.project_id == 2
      assert bodied.project_id == 2
      assert bodied.body_md == "# notes"
    end
  end

  describe "get_task/2" do
    test "round-trips every field, nullable ones included", %{store: store} do
      SQL.exec!(store.conn, "INSERT INTO projects (id, name) VALUES (2, 'work')")
      {:ok, parent} = Store.add_task_in(store, 2, "parent")
      {:ok, created} = Store.add_task_with_body_in(store, 2, "child", "the body")

      :ok = Store.set_state(store, created.id, :doing)
      :ok = Store.set_priority(store, created.id, 2)
      :ok = Store.set_due(store, created.id, "2026-12-01")

      SQL.exec!(store.conn, """
      UPDATE tasks SET parent_id = #{parent.id}, snooze_until = '2026-11-01'
      WHERE id = #{created.id}
      """)

      assert {:ok, task} = Store.get_task(store, created.id)
      assert task.id == created.id
      assert task.title == "child"
      assert task.body_md == "the body"
      assert task.state == :doing
      assert task.parent_id == parent.id
      assert task.project_id == 2
      assert task.priority == 2
      assert task.due == "2026-12-01"
      assert task.snooze_until == "2026-11-01"
      assert task.created_at == created.created_at
      assert DateTime.compare(task.updated_at, created.updated_at) in [:eq, :gt]
      assert task.completed_at == nil
    end

    test "the nullable fields come back nil, not a zero value", %{store: store} do
      created = add!(store, "bare")

      assert {:ok, task} = Store.get_task(store, created.id)
      assert task.parent_id == nil
      assert task.priority == nil
      assert task.due == nil
      assert task.snooze_until == nil
      assert task.completed_at == nil
    end

    test "a task that is not there is named as such", %{store: store} do
      assert Store.get_task(store, 999) == {:error, {:task_not_found, 999}}
    end
  end

  describe "list_live/2" do
    test "hides terminal, hidden and snoozed states", %{store: store} do
      live = add!(store, "live one")
      future = Date.utc_today() |> Date.add(7) |> Date.to_iso8601()
      past = Date.utc_today() |> Date.add(-7) |> Date.to_iso8601()

      fixtures = [
        {"done task", "state", "done", false},
        {"someday task", "state", "someday", false},
        {"review task", "state", "review", true},
        {"snoozed future", "snooze_until", future, false},
        {"snoozed past", "snooze_until", past, true}
      ]

      want =
        for {title, column, value, visible?} <- fixtures, reduce: [live.title] do
          acc ->
            created = add!(store, title)

            SQL.exec!(
              store.conn,
              "UPDATE tasks SET #{column} = '#{value}' WHERE id = #{created.id}"
            )

            if visible?, do: [title | acc], else: acc
        end

      assert {:ok, tasks} = Store.list_live(store, nil)
      assert Enum.sort(titles(tasks)) == Enum.sort(want)
    end

    test "orders by the states table's sort_order, review before doing", %{store: store} do
      for state <- [:blocked, :doing, :review, :todo, :inbox] do
        created = add!(store, Atom.to_string(state))
        :ok = Store.set_state(store, created.id, state)
      end

      assert {:ok, tasks} = Store.list_live(store, nil)
      assert Enum.map(tasks, & &1.state) == [:inbox, :todo, :review, :doing, :blocked]
    end

    test "orders by priority within a state, unprioritized last", %{store: store} do
      for {title, priority} <- [{"no priority", nil}, {"priority C", 3}, {"priority A", 1}] do
        created = add!(store, title)
        :ok = Store.set_state(store, created.id, :todo)
        if priority, do: :ok = Store.set_priority(store, created.id, priority)
      end

      assert {:ok, tasks} = Store.list_live(store, nil)
      assert titles(tasks) == ["priority A", "priority C", "no priority"]
    end
  end

  describe "list_live_with_completed/2" do
    test "adds done and someday but still hides snoozed", %{store: store} do
      future = Date.utc_today() |> Date.add(7) |> Date.to_iso8601()

      fixtures = [
        {"live task", "state", "todo", true},
        {"done task", "state", "done", true},
        {"someday task", "state", "someday", true},
        {"snoozed future", "snooze_until", future, false}
      ]

      want =
        for {title, column, value, visible?} <- fixtures, reduce: [] do
          acc ->
            created = add!(store, title)

            SQL.exec!(
              store.conn,
              "UPDATE tasks SET #{column} = '#{value}' WHERE id = #{created.id}"
            )

            if visible?, do: [title | acc], else: acc
        end

      assert {:ok, tasks} = Store.list_live_with_completed(store, nil)
      assert Enum.sort(titles(tasks)) == Enum.sort(want)

      # The plain live view must still hide someday: only the toggle reveals it.
      assert {:ok, live} = Store.list_live(store, nil)
      refute Enum.any?(live, &(&1.state == :someday))
    end
  end

  describe "list_inbox/2 and count_inbox/2" do
    test "only un-triaged top-level tasks count", %{store: store} do
      assert Store.count_inbox(store, nil) == {:ok, 0}

      captured = add!(store, "one")
      _second = add!(store, "two")
      add_child!(store, captured, "a sub-task, still inbox")
      :ok = Store.set_state(store, captured.id, :todo)

      assert {:ok, inbox} = Store.list_inbox(store, nil)
      assert titles(inbox) == ["two"]
      assert Store.count_inbox(store, nil) == {:ok, 1}
    end
  end

  describe "the nullable project filter (Go's projectFilter)" do
    setup %{store: store} do
      SQL.exec!(store.conn, "INSERT INTO projects (id, name) VALUES (2, 'work')")
      {:ok, unsorted} = Store.add_task(store, "unsorted task")
      {:ok, work} = Store.add_task_in(store, 2, "work task")

      %{unsorted: unsorted, work: work}
    end

    test "nil means every project", %{store: store} do
      assert {:ok, live} = Store.list_live(store, nil)
      assert Enum.sort(titles(live)) == ["unsorted task", "work task"]

      assert {:ok, inbox} = Store.list_inbox(store, nil)
      assert Enum.sort(titles(inbox)) == ["unsorted task", "work task"]

      assert {:ok, completed} = Store.list_live_with_completed(store, nil)
      assert Enum.sort(titles(completed)) == ["unsorted task", "work task"]

      assert Store.count_inbox(store, nil) == {:ok, 2}
    end

    test "a concrete id scopes every filtered query to it", %{store: store} do
      assert {:ok, live} = Store.list_live(store, 2)
      assert titles(live) == ["work task"]

      assert {:ok, inbox} = Store.list_inbox(store, 2)
      assert titles(inbox) == ["work task"]

      assert {:ok, completed} = Store.list_live_with_completed(store, 2)
      assert titles(completed) == ["work task"]

      assert Store.count_inbox(store, 2) == {:ok, 1}
    end

    test "a project with nothing in it is empty, not everything", %{store: store} do
      SQL.exec!(store.conn, "INSERT INTO projects (id, name) VALUES (3, 'empty')")

      assert Store.list_live(store, 3) == {:ok, []}
      assert Store.list_inbox(store, 3) == {:ok, []}
      assert Store.list_live_with_completed(store, 3) == {:ok, []}
      assert Store.count_inbox(store, 3) == {:ok, 0}
    end
  end

  describe "list_project_tasks/2" do
    test "every task in the project at any depth and in any state", %{store: store} do
      SQL.exec!(store.conn, "INSERT INTO projects (id, name) VALUES (2, 'work')")
      {:ok, parent} = Store.add_task_in(store, 2, "parent")
      child_id = add_child!(store, parent, "child")
      {:ok, done} = Store.add_task_in(store, 2, "finished")
      :ok = Store.set_state(store, done.id, :done)
      _elsewhere = add!(store, "unsorted task")

      assert {:ok, tasks} = Store.list_project_tasks(store, 2)
      assert titles(tasks) == ["parent", "child", "finished"]
      assert Enum.map(tasks, & &1.id) == [parent.id, child_id, done.id]
    end
  end

  describe "child_counts/1" do
    test "done and total per parent, and nothing for a childless task", %{store: store} do
      parent = add!(store, "parent")
      first = add_child!(store, parent, "child one")
      add_child!(store, parent, "child two")
      :ok = Store.set_state(store, first, :done)
      _loner = add!(store, "loner")

      assert {:ok, counts} = Store.child_counts(store)
      assert map_size(counts) == 1
      assert counts[parent.id] == %ChildCount{done: 1, total: 2}
    end

    test "an empty database is an empty map", %{store: store} do
      assert Store.child_counts(store) == {:ok, %{}}
    end
  end

  describe "set_state/3" do
    test "stamps completed_at on done and clears it on the way out", %{store: store} do
      created = add!(store, "needs triage")

      :ok = Store.set_state(store, created.id, :done)
      assert {:ok, done} = Store.get_task(store, created.id)
      assert done.state == :done
      assert %DateTime{} = done.completed_at

      :ok = Store.set_state(store, created.id, :todo)
      assert {:ok, todo} = Store.get_task(store, created.id)
      assert todo.state == :todo
      assert todo.completed_at == nil
    end

    test "a state nothing seeded is refused before the write", %{store: store} do
      created = add!(store, "needs triage")

      assert Store.set_state(store, created.id, :bogus) == {:error, {:unknown_state, "bogus"}}
      assert {:ok, task} = Store.get_task(store, created.id)
      assert task.state == :inbox
    end
  end

  describe "set_priority/3" do
    test "sets, clears, and refuses out of range", %{store: store} do
      created = add!(store, "rank me")

      :ok = Store.set_priority(store, created.id, 1)
      assert {:ok, %{priority: 1}} = Store.get_task(store, created.id)

      for bad <- [0, 5, -1] do
        assert Store.set_priority(store, created.id, bad) ==
                 {:error, {:priority_out_of_range, bad}}
      end

      assert {:ok, %{priority: 1}} = Store.get_task(store, created.id)

      :ok = Store.set_priority(store, created.id, nil)
      assert {:ok, %{priority: nil}} = Store.get_task(store, created.id)
    end
  end

  describe "set_due/3" do
    test "normalizes, clears with nil, and refuses a non-date", %{store: store} do
      created = add!(store, "when")

      :ok = Store.set_due(store, created.id, " 2026-12-01 ")
      assert {:ok, %{due: "2026-12-01"}} = Store.get_task(store, created.id)

      assert Store.set_due(store, created.id, "not a date") ==
               {:error, {:invalid_date, "not a date"}}

      assert {:ok, %{due: "2026-12-01"}} = Store.get_task(store, created.id)

      :ok = Store.set_due(store, created.id, nil)
      assert {:ok, %{due: nil}} = Store.get_task(store, created.id)
    end
  end

  describe "set_title/3" do
    test "trims, and a blank rename changes nothing", %{store: store} do
      created = add!(store, "old title")

      :ok = Store.set_title(store, created.id, "  new title  ")
      assert {:ok, %{title: "new title"}} = Store.get_task(store, created.id)

      assert Store.set_title(store, created.id, "   ") == {:error, :empty_title}
      assert {:ok, %{title: "new title"}} = Store.get_task(store, created.id)
    end
  end

  describe "set_body/3 and append_body/3" do
    test "an append lands as a new paragraph, and never a leading blank line", %{store: store} do
      created = add!(store, "notes")
      body = fn -> Store.get_task(store, created.id) |> elem(1) |> Map.fetch!(:body_md) end

      :ok = Store.append_body(store, created.id, "first")
      assert body.() == "first"

      :ok = Store.append_body(store, created.id, "second")
      assert body.() == "first\n\nsecond"

      # Trailing whitespace on the existing body is normalized, so the
      # separator is always exactly one blank line.
      :ok = Store.set_body(store, created.id, "hand edited\n\n\n")
      :ok = Store.append_body(store, created.id, "- link")
      assert body.() == "hand edited\n\n- link"

      # Whitespace-only text is a no-op.
      :ok = Store.append_body(store, created.id, "  \n")
      assert body.() == "hand edited\n\n- link"

      # A whitespace-only body is treated as empty.
      :ok = Store.set_body(store, created.id, "\n  \n")
      :ok = Store.append_body(store, created.id, "fresh")
      assert body.() == "fresh"
    end
  end

  describe "delete_task/2" do
    test "takes the sub-tree with it", %{store: store} do
      parent = add!(store, "parent")
      child_id = add_child!(store, parent, "child")

      assert Store.delete_task(store, parent.id) == :ok
      assert Store.get_task(store, parent.id) == {:error, {:task_not_found, parent.id}}
      assert Store.get_task(store, child_id) == {:error, {:task_not_found, child_id}}
    end

    test "deleting an id that is not there is not an error", %{store: store} do
      assert Store.delete_task(store, 999) == :ok
    end
  end

  describe "the task_events triggers fire on these writes" do
    test "capture writes a created event", %{store: store} do
      created = add!(store, "log me")

      assert [[task_id, title, kind, old, new]] = events(store)
      assert task_id == created.id
      assert title == "log me"
      assert kind == "created"
      assert old == nil
      assert new == "inbox"
    end

    test "a state change writes one event, and a no-op writes none", %{store: store} do
      created = add!(store, "log me")

      :ok = Store.set_state(store, created.id, :doing)
      assert [_created, [_id, _title, "state", "inbox", "doing"]] = events(store)

      :ok = Store.set_state(store, created.id, :doing)
      assert length(events(store)) == 2
    end

    test "metadata changes are deliberately not logged", %{store: store} do
      created = add!(store, "log me")

      :ok = Store.set_priority(store, created.id, 1)
      :ok = Store.set_due(store, created.id, "2026-12-01")
      :ok = Store.set_title(store, created.id, "renamed")
      :ok = Store.set_body(store, created.id, "# notes")
      :ok = Store.append_body(store, created.id, "more")

      assert length(events(store)) == 1
    end

    test "a cascade delete logs the child too", %{store: store} do
      parent = add!(store, "parent")
      child_id = add_child!(store, parent, "child")

      :ok = Store.delete_task(store, parent.id)

      deleted =
        for [task_id, title, "deleted", _old, _new] <- events(store), into: %{} do
          {task_id, title}
        end

      assert deleted == %{parent.id => "parent", child_id => "child"}
    end
  end

  describe "session_statuses/1" do
    test "the most recently active session per task wins", %{store: store} do
      a = add!(store, "task a")
      b = add!(store, "task b")
      _c = add!(store, "task c")

      session!(store, a, "a-old", "blocked")
      session!(store, a, "a-new", "working")
      session!(store, b, "b-1")

      assert {:ok, statuses} = Store.session_statuses(store)
      assert statuses[a.id] == :working
      # A session no hook has reported on yet still appears, which is not the
      # same as absent. Go's case reads :starting there because CreateSession
      # writes that at launch; the row here was inserted straight into the
      # table, so it carries the column's schema default instead.
      assert statuses[b.id] == :unknown
      assert map_size(statuses) == 2
    end

    test "a live run outranks the task's sessions, and a paused one yields", %{store: store} do
      running = add!(store, "running run, starting session")
      session!(store, running, "run-step", "starting")
      run!(store, running, "running")

      gate = add!(store, "gate with no sessions")
      run!(store, gate, "waiting_review")

      pending = add!(store, "pending run")
      run!(store, pending, "pending")

      paused_alone = add!(store, "paused, no session")
      run!(store, paused_alone, "paused")

      paused_takeover = add!(store, "paused, session working")
      session!(store, paused_takeover, "takeover", "working")
      run!(store, paused_takeover, "paused")

      ended = add!(store, "ended run, idle session")
      session!(store, ended, "old", "idle")
      run!(store, ended, "done")

      ended_only = add!(store, "ended run, no session")
      run!(store, ended_only, "failed")

      assert {:ok, statuses} = Store.session_statuses(store)

      assert statuses == %{
               running.id => :working,
               gate.id => :blocked,
               pending.id => :starting,
               paused_alone.id => :idle,
               paused_takeover.id => :working,
               ended.id => :idle
             }

      refute Map.has_key?(statuses, ended_only.id)
    end

    test "the newest active run per task is the one that speaks", %{store: store} do
      task = add!(store, "two live runs")
      run!(store, task, "waiting_review")
      run!(store, task, "running")

      # Active runs come newest-first (started_at DESC, id DESC), so the
      # second row inserted is the one read.
      assert {:ok, statuses} = Store.session_statuses(store)
      assert statuses[task.id] == :working
    end

    test "no sessions and no runs is an empty map", %{store: store} do
      _task = add!(store, "quiet")
      assert Store.session_statuses(store) == {:ok, %{}}
    end
  end

  describe "transaction/2" do
    test "commits what the function wrote", %{store: store} do
      assert {:ok, task} =
               Store.transaction(store, fn tx ->
                 Store.add_task(tx, "inside a transaction")
               end)

      assert {:ok, ^task} = Store.get_task(store, task.id)
    end

    test "rolls back on {:error, reason} and passes the reason through", %{store: store} do
      assert Store.transaction(store, fn tx ->
               {:ok, _task} = Store.add_task(tx, "doomed")
               {:error, :changed_my_mind}
             end) == {:error, :changed_my_mind}

      assert SQL.scalar!(store.conn, "SELECT COUNT(*) FROM tasks") == 0
    end

    test "a raise rolls back, re-raises, and leaves the connection usable", %{store: store} do
      assert_raise RuntimeError, "boom", fn ->
        Store.transaction(store, fn tx ->
          {:ok, _task} = Store.add_task(tx, "doomed")
          raise "boom"
        end)
      end

      assert SQL.scalar!(store.conn, "SELECT COUNT(*) FROM tasks") == 0

      # The transaction really was closed: a second one can begin.
      assert {:ok, _task} = Store.transaction(store, &Store.add_task(&1, "after the raise"))
      assert SQL.scalar!(store.conn, "SELECT COUNT(*) FROM tasks") == 1
    end
  end
end
