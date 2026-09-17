defmodule Tend.Store.SessionsTest do
  @moduledoc """
  The agent-session and tmux-session surface, against a real temp database.

  A port of the session cases in `internal/store/store_test.go`. Where a
  fixture needs a write this part of the port does not own yet -- a project,
  or the pre-launch-row shape `forgetStatus` manufactures -- the test does it
  in raw SQL, the same latitude `Tend.Store.TasksTest` takes and the same one
  the Go tests take with `db.ExecContext`.

  The compare-and-swap cases are the point of the module. `status_updated_at`
  is a millisecond-precision freshness token, and the CAS compares it as text,
  so these run real concurrent writers against the real file rather than
  asserting the SQL by eye.
  """

  use ExUnit.Case, async: true

  alias Tend.Store
  alias Tend.Store.Row
  alias Tend.Task.Project
  alias Tend.Test.SQL

  @moduletag :tmp_dir

  setup %{tmp_dir: dir} do
    path = Path.join(dir, "tend.db")
    {:ok, store} = Store.open(path)
    on_exit(fn -> Store.close(store) end)
    %{store: store, path: path}
  end

  defp add!(store, title) do
    {:ok, task} = Store.add_task(store, title)
    task
  end

  defp session!(store, task, external_id, tmux \\ "") do
    {:ok, session} =
      Store.create_session(store, task.id, external_id, "/tmp/work", task.title, tmux)

    session
  end

  defp sessions!(store, task) do
    {:ok, sessions} = Store.list_sessions_for_task(store, task.id)
    sessions
  end

  # The row CreateProject would write. Projects are a later part of the store
  # port, and list_sessions_for_project/2 needs two of them now.
  defp project!(store, name) do
    SQL.exec!(store.conn, "INSERT INTO projects (name) VALUES ('#{name}')")
    SQL.scalar!(store.conn, "SELECT id FROM projects WHERE name = '#{name}'")
  end

  # The port of the Go test's forgetStatus helper: resets a row to the shape
  # rows had before launch-time writes existed -- status unknown and
  # status_updated_at NULL, "never observed". No store function produces that
  # any more, but such rows are still in real databases, so the NULL branch of
  # the poller's CAS still needs exercising.
  defp forget_status!(store, external_id) do
    SQL.exec!(store.conn, """
    UPDATE agent_sessions
    SET status = 'unknown', status_updated_at = NULL
    WHERE external_id = '#{external_id}'
    """)
  end

  # The port of nextStatusTick: waits out the millisecond status_updated_at is
  # stamped at. A row is created already stamped, so a "concurrent" hook write
  # straight after creation can land in the same millisecond and mint a
  # byte-identical CAS token -- a timeline no real launch has (a hook needs a
  # running claude to fire), but one a back-to-back test easily does.
  defp next_status_tick, do: Process.sleep(2)

  defp stored_status_time(store, external_id) do
    SQL.scalar!(
      store.conn,
      "SELECT status_updated_at FROM agent_sessions WHERE external_id = '#{external_id}'"
    )
  end

  describe "create_session/6, list_sessions_for_task/2 and touch_session/2" do
    test "a task's sessions come back newest first", %{store: store} do
      parent = add!(store, "fix the bug")

      assert {:ok, []} = Store.list_sessions_for_task(store, parent.id)

      first = session!(store, parent, "ext-1")
      assert first.task_id == parent.id
      assert first.external_id == "ext-1"
      assert first.cwd == "/tmp/work"
      assert first.label == parent.title

      second = session!(store, parent, "ext-2")

      # Both were created in the same instant, so id descending is the
      # tiebreak behind last_active_at.
      assert [^second, ^first] = sessions!(store, parent)

      assert :ok = Store.touch_session(store, first.id)
    end

    test "the tmux name round-trips, and a fresh session owes no recap", %{store: store} do
      parent = add!(store, "fix the bug")

      session = session!(store, parent, "ext-1", "tend-ext-1")
      assert session.tmux_session == "tend-ext-1"
      refute session.needs_recap

      assert [stored] = sessions!(store, parent)
      assert stored.tmux_session == "tend-ext-1"
    end

    test "a session launched without tmux stores an empty name", %{store: store} do
      parent = add!(store, "fix the bug")

      assert session!(store, parent, "ext-1").tmux_session == ""
    end

    test "the row is written at launch as :starting, already stamped", %{store: store} do
      parent = add!(store, "do the thing")

      session = session!(store, parent, "ext-1", "tend-ext-1")
      assert session.status == :starting
      assert session.status_updated_at != nil

      # Then the first turn's hooks land on the launch row: starting from
      # SessionStart, idle from Stop, with no resume in between.
      for status <- [:starting, :idle] do
        assert :ok = Store.set_session_status(store, "ext-1", status)
        assert [stored] = sessions!(store, parent)
        assert stored.status == status
      end
    end

    test "sessions cascade away with their task", %{store: store} do
      parent = add!(store, "parent")
      session!(store, parent, "ext-1")

      assert :ok = Store.delete_task(store, parent.id)
      assert {:ok, []} = Store.list_sessions_for_task(store, parent.id)
    end
  end

  describe "delete_session/2" do
    test "takes back the row a failed launch left behind, idempotently", %{store: store} do
      parent = add!(store, "do the thing")
      failed = session!(store, parent, "ext-1", "tend-ext-1")
      keep = session!(store, parent, "ext-2", "tend-ext-2")

      assert :ok = Store.delete_session(store, failed.id)
      # Deleting an id that is already gone is a no-op, not an error.
      assert :ok = Store.delete_session(store, failed.id)

      assert [only] = sessions!(store, parent)
      assert only.id == keep.id
    end
  end

  describe "update_session_label/3" do
    test "renames a session by its external id", %{store: store} do
      parent = add!(store, "fix the bug")
      session!(store, parent, "ext-1")

      assert :ok = Store.update_session_label(store, "ext-1", "fixed the flaky test")

      assert [stored] = sessions!(store, parent)
      assert stored.label == "fixed the flaky test"
    end
  end

  describe "list_sessions_for_project/2" do
    test "scopes through the owning task and attaches its title and state", %{store: store} do
      project_id = project!(store, "hapi")
      {:ok, in_project} = Store.add_task_in(store, project_id, "fix the bug")
      :ok = Store.set_state(store, in_project.id, :doing)
      elsewhere = add!(store, "unrelated")

      older = session!(store, in_project, "ext-1", "tend-ext-1")
      newer = session!(store, in_project, "ext-2")
      session!(store, elsewhere, "ext-3")

      assert {:ok, [first, second]} = Store.list_sessions_for_project(store, project_id)
      assert first.session.id == newer.id
      assert second.session.id == older.id

      assert first.task_title == in_project.title
      assert first.task_state == :doing
      assert first.session.task_id == in_project.id

      assert second.session.tmux_session == "tend-ext-1"
      assert second.session.status == :starting

      # nil is every project, the same convention list_live/2 uses.
      assert {:ok, all} = Store.list_sessions_for_project(store, nil)
      assert length(all) == 3

      # Deleting the task takes its sessions out of the project's list too.
      assert :ok = Store.delete_task(store, in_project.id)
      assert {:ok, []} = Store.list_sessions_for_project(store, project_id)

      # The unrelated task's session lives in the default project.
      assert {:ok, [remaining]} = Store.list_sessions_for_project(store, Project.default_id())
      assert remaining.session.external_id == "ext-3"
    end
  end

  describe "set_session_status/3" do
    test "records the status and stamps status_updated_at", %{store: store} do
      parent = add!(store, "do the thing")
      session!(store, parent, "ext-1", "tend-ext-1")

      assert :ok = Store.set_session_status(store, "ext-1", :blocked)

      assert [stored] = sessions!(store, parent)
      assert stored.status == :blocked
      assert stored.status_updated_at != nil
    end

    test "a session id with no row is not an error", %{store: store} do
      # A hook can still fire for a session tend has no row for -- a launch
      # that failed and had its row deleted, a claude started outside tend
      # with tend's settings. Failing would print into the user's transcript.
      assert :ok = Store.set_session_status(store, "never-seen", :idle)
    end

    test "a status outside the closed set raises, because only this tree writes one",
         %{store: store} do
      assert_raise FunctionClauseError, fn ->
        Store.set_session_status(store, "ext-1", :compacting)
      end
    end
  end

  describe "set_session_needs_recap/3 and list_sessions_needing_recap/1" do
    test "the flag round-trips both ways", %{store: store} do
      parent = add!(store, "fix the bug")
      session!(store, parent, "ext-1", "tend-ext-1")

      assert :ok = Store.set_session_needs_recap(store, "ext-1", true)
      assert [%{needs_recap: true}] = sessions!(store, parent)

      # The SessionEnd hook clears it.
      assert :ok = Store.set_session_needs_recap(store, "ext-1", false)
      assert [%{needs_recap: false}] = sessions!(store, parent)
    end

    test "only the sessions that owe a recap come back", %{store: store} do
      parent = add!(store, "do the thing")
      for ext <- ["ext-1", "ext-2"], do: session!(store, parent, ext, "tend-" <> ext)
      :ok = Store.set_session_needs_recap(store, "ext-2", true)

      assert {:ok, [owed]} = Store.list_sessions_needing_recap(store)
      assert owed.external_id == "ext-2"
    end

    test "the list is deliberately not filtered on status", %{store: store} do
      # A host that dies never gets to fire SessionEnd, and a status-gated
      # query would strand that session's recap forever.
      parent = add!(store, "do the thing")
      session!(store, parent, "ext-1", "tend-ext-1")
      :ok = Store.set_session_needs_recap(store, "ext-1", true)

      assert {:ok, [owed]} = Store.list_sessions_needing_recap(store)
      assert owed.status == :starting
    end
  end

  describe "sessions_with_tmux/1" do
    test "only sessions that are attachable and not already ended", %{store: store} do
      parent = add!(store, "do the thing")
      session!(store, parent, "no-tmux")
      session!(store, parent, "live", "tend-live")
      session!(store, parent, "ended", "tend-ended")
      :ok = Store.set_session_status(store, "ended", :ended)

      assert {:ok, [only]} = Store.sessions_with_tmux(store)
      assert only.external_id == "live"
    end
  end

  describe "claim_session_recap/2" do
    test "exactly one of two claims wins, and the debt is cleared", %{store: store} do
      parent = add!(store, "do the thing")
      session!(store, parent, "ext-1", "tend-ext-1")
      :ok = Store.set_session_needs_recap(store, "ext-1", true)

      assert {:ok, true} = Store.claim_session_recap(store, "ext-1")
      # Two instances would otherwise both run the expensive recap call.
      assert {:ok, false} = Store.claim_session_recap(store, "ext-1")

      assert {:ok, []} = Store.list_sessions_needing_recap(store)
    end

    test "a debt that was never owed is not claimable", %{store: store} do
      assert {:ok, false} = Store.claim_session_recap(store, "never-seen")
    end

    test "exactly one of many concurrent callers wins", %{store: store, path: path} do
      parent = add!(store, "do the thing")
      session!(store, parent, "ext-1", "tend-ext-1")
      :ok = Store.set_session_needs_recap(store, "ext-1", true)

      won = race(path, 8, &Store.claim_session_recap(&1, "ext-1"))

      assert Enum.count(won, & &1) == 1
      assert {:ok, []} = Store.list_sessions_needing_recap(store)
    end
  end

  describe "set_session_working_if_unchanged/3" do
    test "wins when the row has never been observed at all", %{store: store} do
      parent = add!(store, "do the thing")
      session!(store, parent, "ext-1", "tend-ext-1")
      forget_status!(store, "ext-1")

      assert [observed] = sessions!(store, parent)
      assert observed.status_updated_at == nil

      assert {:ok, true} =
               Store.set_session_working_if_unchanged(store, "ext-1", observed.status_updated_at)

      assert [%{status: :working}] = sessions!(store, parent)
    end

    test "wins against a non-null timestamp that still matches", %{store: store} do
      parent = add!(store, "do the thing")
      session!(store, parent, "ext-1", "tend-ext-1")
      :ok = Store.set_session_status(store, "ext-1", :idle)

      assert [observed] = sessions!(store, parent)
      assert observed.status_updated_at != nil

      assert {:ok, true} =
               Store.set_session_working_if_unchanged(store, "ext-1", observed.status_updated_at)
    end

    test "loses to a hook that landed mid-tick, leaving the row untouched", %{store: store} do
      parent = add!(store, "do the thing")
      # The launch stamp, as the poller would have read it pre-hook.
      stale = session!(store, parent, "ext-1", "tend-ext-1").status_updated_at
      next_status_tick()

      # Simulate a Notification hook landing mid-tick.
      :ok = Store.set_session_status(store, "ext-1", :blocked)
      hook_stamp = stored_status_time(store, "ext-1")

      assert {:ok, false} = Store.set_session_working_if_unchanged(store, "ext-1", stale)

      assert [%{status: :blocked}] = sessions!(store, parent)
      assert stored_status_time(store, "ext-1") == hook_stamp
    end

    test "a session id with no row reports no win rather than an error", %{store: store} do
      assert {:ok, false} = Store.set_session_working_if_unchanged(store, "never-seen", nil)
    end
  end

  describe "set_session_idle_if_unchanged/3" do
    test "takes a stable :working back down", %{store: store} do
      parent = add!(store, "do the thing")
      session = session!(store, parent, "ext-1", "tend-ext-1")

      {:ok, _won} =
        Store.set_session_working_if_unchanged(store, "ext-1", session.status_updated_at)

      assert [observed] = sessions!(store, parent)

      assert {:ok, true} =
               Store.set_session_idle_if_unchanged(store, "ext-1", observed.status_updated_at)

      assert [%{status: :idle}] = sessions!(store, parent)
    end

    test "loses to a hook that landed mid-tick, leaving the row untouched", %{store: store} do
      parent = add!(store, "do the thing")
      session = session!(store, parent, "ext-1", "tend-ext-1")

      {:ok, _won} =
        Store.set_session_working_if_unchanged(store, "ext-1", session.status_updated_at)

      # Read while still 'working', pre-hook.
      assert [observed] = sessions!(store, parent)
      stale = observed.status_updated_at

      # A real race always has at least this much separation -- a hook and a
      # poll tick are distinct OS processes, each with milliseconds of
      # unavoidable spawn overhead.
      next_status_tick()
      :ok = Store.set_session_status(store, "ext-1", :blocked)
      hook_stamp = stored_status_time(store, "ext-1")

      assert {:ok, false} = Store.set_session_idle_if_unchanged(store, "ext-1", stale)

      assert [%{status: :blocked}] = sessions!(store, parent)
      assert stored_status_time(store, "ext-1") == hook_stamp
    end

    test "a session id with no row reports no win rather than an error", %{store: store} do
      assert {:ok, false} = Store.set_session_idle_if_unchanged(store, "never-seen", nil)
    end
  end

  describe "set_session_ended_if_unchanged/3" do
    test "wins when the row has never been observed at all", %{store: store} do
      parent = add!(store, "do the thing")
      session!(store, parent, "ext-1", "tend-ext-1")
      forget_status!(store, "ext-1")

      assert [observed] = sessions!(store, parent)

      assert {:ok, true} =
               Store.set_session_ended_if_unchanged(store, "ext-1", observed.status_updated_at)

      assert [%{status: :ended}] = sessions!(store, parent)
    end

    test "loses to a hook that landed mid-tick, leaving the row untouched", %{store: store} do
      parent = add!(store, "do the thing")
      stale = session!(store, parent, "ext-1", "tend-ext-1").status_updated_at
      next_status_tick()

      :ok = Store.set_session_status(store, "ext-1", :blocked)
      hook_stamp = stored_status_time(store, "ext-1")

      assert {:ok, false} = Store.set_session_ended_if_unchanged(store, "ext-1", stale)

      assert [%{status: :blocked}] = sessions!(store, parent)
      assert stored_status_time(store, "ext-1") == hook_stamp
    end

    test "a session id with no row reports no win rather than an error", %{store: store} do
      assert {:ok, false} = Store.set_session_ended_if_unchanged(store, "never-seen", nil)
    end
  end

  describe "the millisecond CAS under real concurrency" do
    setup %{store: store} do
      parent = add!(store, "do the thing")
      session!(store, parent, "ext-1", "tend-ext-1")
      :ok = Store.set_session_status(store, "ext-1", :idle)

      # The token every racer holds is read now and compared later. The wait
      # is what makes the winner's own write land in a *different*
      # millisecond, which is the only reason the losers can tell they lost:
      # a winner that re-stamped the same millisecond would mint a token
      # byte-identical to the one the losers still hold, and they would all
      # win. Two real writers are always at least this far apart -- a hook and
      # a poll tick are distinct OS processes.
      next_status_tick()

      assert [observed] = sessions!(store, parent)
      %{parent: parent, observed: observed.status_updated_at}
    end

    for {name, fun} <- [
          {"working", :set_session_working_if_unchanged},
          {"idle", :set_session_idle_if_unchanged},
          {"ended", :set_session_ended_if_unchanged}
        ] do
      test "two #{name} racers on the same token produce exactly one true and one false",
           context do
        %{path: path, observed: observed} = context

        won = race(path, 2, &apply(Store, unquote(fun), [&1, "ext-1", observed]))

        assert Enum.sort(won) == [false, true]
      end
    end

    test "a stale token loses and leaves the row exactly as it was", context do
      %{store: store, parent: parent, observed: observed} = context

      # One writer moves the row on; the token every other reader holds is now
      # stale by exactly one millisecond-precision write.
      assert {:ok, true} = Store.set_session_working_if_unchanged(store, "ext-1", observed)
      after_win = stored_status_time(store, "ext-1")

      assert {:ok, false} = Store.set_session_working_if_unchanged(store, "ext-1", observed)
      assert {:ok, false} = Store.set_session_idle_if_unchanged(store, "ext-1", observed)
      assert {:ok, false} = Store.set_session_ended_if_unchanged(store, "ext-1", observed)

      assert [%{status: :working}] = sessions!(store, parent)
      assert stored_status_time(store, "ext-1") == after_win
    end
  end

  describe "the stored status_updated_at format" do
    test "the text SQLite wrote survives a parse and a format byte for byte", %{store: store} do
      # The CAS token is this string, compared with SQL `IS`. If the port's
      # own round trip were not byte-identical, a writer could never match a
      # row it had just written, and every CAS would lose forever.
      parent = add!(store, "do the thing")
      session!(store, parent, "ext-1", "tend-ext-1")

      for status <- [:starting, :working, :idle, :blocked, :ended] do
        :ok = Store.set_session_status(store, "ext-1", status)
        stored = stored_status_time(store, "ext-1")

        assert {:ok, parsed} = Row.parse_status_time(stored)
        assert Row.format_status_time(parsed) == stored

        # And the round trip really is what the CAS compares against.
        assert {:ok, true} = Store.set_session_working_if_unchanged(store, "ext-1", parsed)
      end
    end

    test "the column keeps whole milliseconds, never a truncated second", %{store: store} do
      parent = add!(store, "do the thing")
      session!(store, parent, "ext-1", "tend-ext-1")

      assert stored_status_time(store, "ext-1") =~
               ~r/\A\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}\.\d{3}\z/
    end
  end

  # N racers, each on its own connection to the same file, all calling `fun`
  # at once. Separate connections rather than one shared handle because the
  # race this guards is two OS processes -- the hook and the TUI -- not two
  # BEAM processes sharing a statement cache.
  defp race(path, count, fun) do
    1..count
    |> Task.async_stream(
      fn _ ->
        {:ok, store} = Store.open(path)

        try do
          {:ok, won} = fun.(store)
          won
        after
          Store.close(store)
        end
      end,
      max_concurrency: count,
      ordered: false
    )
    |> Enum.map(fn {:ok, won} -> won end)
  end
end
