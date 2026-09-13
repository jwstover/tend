defmodule Tend.Store.MigratorTest do
  @moduledoc """
  A port of `internal/store/migration_test.go`.

  The Go original drives its assertions through `Store` query functions
  (`ListLive`, `TagsForTask`, `AddTask`...). Those land in a later sub-task, so
  the same facts are asserted here in SQL. That is not a downgrade: these are
  migration tests, and what they are really about is what the *schema* does.
  """

  use ExUnit.Case, async: true

  alias Exqlite.Sqlite3
  alias Tend.Store
  alias Tend.Store.Migrator
  alias Tend.Test.SQL

  @moduletag :tmp_dir

  # The Go test's schemaBefore* constants: the last migration before each of
  # the ones under test. Seeding at these versions is what makes these
  # migration tests rather than tests of the current schema.
  @before_projects 6
  @before_project_events 7
  @before_workflows 8
  @before_review 13
  @before_parent_events 14

  # `openAt` in the Go test: a connection migrated only as far as `version`,
  # so a test can seed rows in an older schema shape. Same pragmas as
  # `Tend.Store.open/1`, since migration 00007 depends on foreign keys being
  # on and says so in its own comments.
  defp open_at(path, version) do
    {:ok, conn} = Sqlite3.open(path)
    SQL.exec!(conn, "PRAGMA journal_mode = WAL")
    SQL.exec!(conn, "PRAGMA busy_timeout = 5000")
    SQL.exec!(conn, "PRAGMA foreign_keys = ON")
    :ok = Migrator.migrate_to(conn, version)
    conn
  end

  defp reopen!(path) do
    {:ok, store} = Store.open(path)
    on_exit(fn -> Store.close(store) end)
    store.conn
  end

  # title => its tags, for the tasks that have any.
  defp tags_by_title(conn) do
    conn
    |> SQL.rows!("""
    SELECT t.title, g.name
    FROM tasks t
    LEFT JOIN task_tags tt ON tt.task_id = t.id
    LEFT JOIN tags g ON g.id = tt.tag_id
    ORDER BY t.title, g.name
    """)
    |> Enum.group_by(fn [title, _tag] -> title end, fn [_title, tag] -> tag end)
    |> Map.new(fn {title, tags} -> {title, Enum.reject(tags, &is_nil/1)} end)
  end

  # The states table as name => sort_order (`stateOrders` in the Go test).
  defp state_orders(conn) do
    conn
    |> SQL.rows!("SELECT name, sort_order FROM states")
    |> Map.new(fn [name, order] -> {name, order} end)
  end

  defp event_kinds(conn) do
    conn
    |> SQL.rows!("SELECT kind FROM task_events ORDER BY id")
    |> Enum.map(&hd/1)
  end

  # TestMigrationMovesProjectStringsToTags. Migration 00007 is the one step in
  # the projects feature that can destroy real user data -- it ends in
  # ALTER TABLE tasks DROP COLUMN project -- so the old flat strings surviving
  # as tags is asserted directly.
  describe "00007 projects and tags" do
    test "the old flat project strings survive as tags", %{tmp_dir: dir} do
      path = Path.join(dir, "tend.db")
      conn = open_at(path, @before_projects)

      # Rows in the OLD shape: a plain value, a NULL, a whitespace-only
      # value, and a case variant the new NOCASE unique index folds into an
      # existing tag.
      SQL.exec!(conn, """
      INSERT INTO tasks (title, project) VALUES
        ('has a project', 'tend'),
        ('no project',     NULL),
        ('blank project',  '   '),
        ('padded project', '  home  '),
        ('case variant',   'HOME')
      """)

      :ok = Sqlite3.close(conn)

      # Opening runs the remaining migrations, 00007 among them.
      conn = reopen!(path)

      assert SQL.scalar!(conn, "SELECT COUNT(*) FROM tasks") == 5

      tags = tags_by_title(conn)
      assert tags["has a project"] == ["tend"]
      assert tags["no project"] == []
      assert tags["blank project"] == []
      # The value was stored padded; trim() in the migration is what makes
      # this "home" rather than a distinct "  home  " tag.
      assert tags["padded project"] == ["home"]
      # 'HOME' collides with 'home' under NOCASE, so it attaches to the same
      # tag row; which spelling won the insert race is not worth asserting.
      assert [variant] = tags["case variant"]
      assert String.downcase(variant) == "home"

      # Two tag rows, not three: 'HOME' folded into 'home'.
      assert SQL.scalar!(conn, "SELECT COUNT(*) FROM tags") == 2

      # Every migrated task lands in the seeded default project: the
      # migration moves the old strings to tags only, it does not infer
      # projects.
      assert SQL.scalar!(conn, "SELECT COUNT(*) FROM tasks WHERE project_id <> 1") == 0

      # And the old column is really gone, not merely unused.
      assert {:error, _} = SQL.exec(conn, "SELECT project FROM tasks LIMIT 1")
    end

    # TestFreshDatabaseHasDefaultProject. The seeded default project has to
    # exist on a brand-new database too, or every capture path that falls back
    # to it breaks.
    test "a fresh database has the default project", %{tmp_dir: dir} do
      conn = reopen!(Path.join(dir, "tend.db"))

      assert SQL.rows!(conn, "SELECT id, name FROM projects") == [[1, "Unsorted"]]
    end
  end

  # TestTaskEventsRebuildKeepsHistoryAndTriggers. Migration 00008 rebuilds
  # task_events to widen a CHECK, which means dropping and recreating the
  # table and the three triggers that write to it. A rebuild that silently
  # dropped history would be the worst kind of quiet failure.
  describe "00008 task_events rebuild" do
    test "history and triggers survive the rebuild", %{tmp_dir: dir} do
      path = Path.join(dir, "tend.db")
      conn = open_at(path, @before_project_events)
      SQL.exec!(conn, "INSERT INTO tasks (title) VALUES ('pre-existing')")
      before = SQL.scalar!(conn, "SELECT COUNT(*) FROM task_events")
      assert before > 0, "expected the created trigger to have written an event"
      :ok = Sqlite3.close(conn)

      conn = reopen!(path)

      assert SQL.scalar!(conn, "SELECT COUNT(*) FROM task_events") == before

      # The triggers have to survive being dropped and recreated.
      SQL.exec!(conn, "INSERT INTO tasks (title) VALUES ('written after the rebuild')")

      SQL.exec!(
        conn,
        "UPDATE tasks SET state = 'doing' WHERE title = 'written after the rebuild'"
      )

      kinds =
        conn
        |> SQL.rows!("""
        SELECT kind FROM task_events
        WHERE task_title = 'written after the rebuild' ORDER BY id
        """)
        |> Enum.map(&hd/1)

      assert kinds == ["created", "state"]

      # ...and the widened CHECK admits the new kind.
      assert :ok =
               SQL.exec(conn, """
               INSERT INTO task_events (task_id, task_title, kind, old_value, new_value)
               VALUES (1, 'pre-existing', 'project', 'Unsorted', 'somewhere else')
               """)
    end
  end

  # TestReviewStateMigrationRoundTrips. Migration 00014 seeds the review state
  # and renumbers the states after it. Down has to park any review tasks
  # somewhere first or the FK on tasks.state refuses the delete, so both
  # directions are driven here.
  describe "00014 review state" do
    test "round-trips", %{tmp_dir: dir} do
      path = Path.join(dir, "tend.db")
      conn = open_at(path, @before_review)
      SQL.exec!(conn, "INSERT INTO tasks (title, state) VALUES ('in flight', 'doing')")

      # Up: review exists, is live, and sits right before doing.
      :ok = Migrator.migrate_to(conn, @before_review + 1)

      assert state_orders(conn) == %{
               "inbox" => 0,
               "todo" => 1,
               "review" => 2,
               "doing" => 3,
               "blocked" => 4,
               "done" => 5,
               "someday" => 6
             }

      assert SQL.rows!(
               conn,
               "SELECT is_terminal, hidden_by_default FROM states WHERE name = 'review'"
             ) == [[0, 0]]

      SQL.exec!(conn, "UPDATE tasks SET state = 'review' WHERE title = 'in flight'")

      # Down: the row goes, its tasks land in doing, and the numbering is
      # back to the 00001 seed.
      :ok = Migrator.rollback_to(conn, @before_review)

      assert state_orders(conn) == %{
               "inbox" => 0,
               "todo" => 1,
               "doing" => 2,
               "blocked" => 3,
               "done" => 4,
               "someday" => 5
             }

      assert SQL.scalar!(conn, "SELECT state FROM tasks WHERE title = 'in flight'") == "doing"

      :ok = Sqlite3.close(conn)
    end
  end

  # TestWorkflowsMigrationRoundTrips. Migration 00009 adds five tables and a
  # REFERENCES column on agent_sessions. Up is exercised by every other store
  # test; this one drives it down and back up on a database with existing
  # sessions, since ALTER TABLE ... DROP COLUMN has a list of reasons it can
  # refuse and a Down that cannot run is a trap discovered only when it is
  # needed.
  describe "00009 workflows" do
    test "round-trips on a database with sessions", %{tmp_dir: dir} do
      path = Path.join(dir, "tend.db")
      conn = open_at(path, @before_workflows)
      SQL.exec!(conn, "INSERT INTO tasks (title) VALUES ('has a session')")

      SQL.exec!(conn, """
      INSERT INTO agent_sessions (task_id, external_id, cwd, label)
      VALUES (1, 'sess-1', '/tmp', 'label')
      """)

      # Up.
      :ok = Migrator.migrate_to(conn, @before_workflows + 1)

      for table <- ~w(workflows workflow_steps workflow_edges workflow_runs workflow_step_runs) do
        assert SQL.scalar!(conn, "SELECT COUNT(*) FROM #{table}") == 0
      end

      # A pre-existing session is unbound, not retro-fitted to some run.
      assert SQL.rows!(
               conn,
               "SELECT workflow_step_run_id FROM agent_sessions WHERE external_id = 'sess-1'"
             ) == [[nil]]

      # Down: the column and the tables go, the session stays.
      :ok = Migrator.rollback_to(conn, @before_workflows)

      assert {:error, _} =
               SQL.exec(conn, "SELECT workflow_step_run_id FROM agent_sessions LIMIT 1")

      assert {:error, _} = SQL.exec(conn, "SELECT 1 FROM workflows LIMIT 1")
      assert SQL.scalar!(conn, "SELECT COUNT(*) FROM agent_sessions") == 1
      :ok = Sqlite3.close(conn)

      # And up again through the normal path, then use it.
      conn = reopen!(path)

      SQL.exec!(conn, "INSERT INTO workflows (id, name) VALUES (1, 'after round trip')")
      SQL.exec!(conn, "INSERT INTO workflow_steps (workflow_id, name) VALUES (1, 'step')")

      assert SQL.rows!(conn, "SELECT workflow_step_run_id FROM agent_sessions") == [[nil]]
    end
  end

  # TestParentEventMigrationRoundTrips. Migration 00015 rebuilds task_events to
  # widen its CHECK, the way 00008 did for 'project'. The rebuild must keep
  # every row, the triggers must survive being dropped and recreated, the
  # widened CHECK must admit 'parent', and Down must drop the rows it can no
  # longer hold rather than fail.
  describe "00015 parent event kind" do
    test "round-trips on a database with history", %{tmp_dir: dir} do
      path = Path.join(dir, "tend.db")
      conn = open_at(path, @before_parent_events)
      SQL.exec!(conn, "INSERT INTO tasks (title) VALUES ('pre-existing')")

      SQL.exec!(conn, """
      INSERT INTO task_events (task_id, task_title, kind, old_value, new_value)
      VALUES (1, 'pre-existing', 'project', 'Unsorted', 'elsewhere')
      """)

      assert SQL.scalar!(conn, "SELECT COUNT(*) FROM task_events") == 2

      # The old CHECK refuses what the migration is about to admit.
      assert {:error, _} =
               SQL.exec(
                 conn,
                 "INSERT INTO task_events (task_id, task_title, kind) VALUES (1, 'x', 'parent')"
               )

      # Up: rows survive, and the new kind is admitted.
      :ok = Migrator.migrate_to(conn, @before_parent_events + 1)
      assert SQL.scalar!(conn, "SELECT COUNT(*) FROM task_events") == 2

      SQL.exec!(conn, """
      INSERT INTO task_events (task_id, task_title, kind, old_value, new_value)
      VALUES (1, 'pre-existing', 'parent', '(top level)', 'host')
      """)

      # Down: the parent row goes, everything else stays.
      :ok = Migrator.rollback_to(conn, @before_parent_events)
      assert event_kinds(conn) == ["created", "project"]
      :ok = Sqlite3.close(conn)

      # And up again through the normal path, then use it end to end.
      conn = reopen!(path)
      SQL.exec!(conn, "INSERT INTO tasks (title) VALUES ('host')")

      SQL.exec!(conn, """
      INSERT INTO task_events (task_id, task_title, kind, old_value, new_value)
      VALUES (1, 'pre-existing', 'parent', '(top level)', 'host')
      """)

      # The triggers still fire after two rebuilds.
      SQL.exec!(conn, "UPDATE tasks SET state = 'doing' WHERE title = 'host'")

      assert "state" in event_kinds(conn)
      assert "parent" in event_kinds(conn)
    end
  end

  # Not in the Go test, because the Go binary is the one that writes
  # goose_db_version. This is the ladder's whole reason for being goose-aware.
  describe "the goose-aware ladder" do
    test "seeds user_version from goose_db_version instead of replaying", %{tmp_dir: dir} do
      path = Path.join(dir, "tend.db")
      conn = reopen!(path)
      schema = SQL.schema!(conn)
      goose = SQL.goose_rows!(conn)

      # Exactly the shape a Go-created database arrives in: goose knows it is
      # at 16, the header still says 0. Replaying from 0 would fail on the
      # first CREATE TABLE.
      SQL.exec!(conn, "PRAGMA user_version = 0")

      assert Migrator.version(conn) == {:ok, Migrator.latest_version()}
      assert :ok = Migrator.migrate(conn)
      assert SQL.scalar!(conn, "PRAGMA user_version") == Migrator.latest_version()
      assert SQL.schema!(conn) == schema
      assert SQL.goose_rows!(conn) == goose
    end

    test "a partially migrated goose database resumes where goose left off", %{tmp_dir: dir} do
      path = Path.join(dir, "tend.db")
      conn = open_at(path, @before_workflows)

      # Forget the header the way an older Go binary would have left it.
      SQL.exec!(conn, "PRAGMA user_version = 0")

      assert :ok = Migrator.migrate(conn)
      assert SQL.scalar!(conn, "PRAGMA user_version") == Migrator.latest_version()

      assert SQL.scalar!(conn, "SELECT MAX(version_id) FROM goose_db_version") ==
               Migrator.latest_version()

      :ok = Sqlite3.close(conn)
    end
  end

  # The point of copying the files rather than retyping the DDL is that they
  # stay copies. This is the assertion that keeps them honest.
  describe "priv/migrations" do
    test "is a verbatim copy of internal/store/migrations" do
      ours = Path.wildcard(Path.join(Migrator.directory(), "*.sql"))
      theirs_dir = Path.expand("../internal/store/migrations", File.cwd!())

      assert length(ours) == 16

      for path <- ours do
        theirs = Path.join(theirs_dir, Path.basename(path))

        assert File.read!(path) == File.read!(theirs),
               "#{Path.basename(path)} has drifted from the Go tree's copy"
      end
    end

    test "every file parses into an up and a down section" do
      for migration <- Migrator.migrations() do
        assert migration.up =~ ~r/\S/, "#{migration.name} has an empty Up"
        assert migration.down =~ ~r/\S/, "#{migration.name} has an empty Down"
        refute migration.up =~ "+goose Down"
      end
    end
  end
end
