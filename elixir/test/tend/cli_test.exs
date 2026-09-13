defmodule Tend.CLITest do
  use ExUnit.Case, async: true

  import ExUnit.CaptureIO

  alias Tend.CLI
  alias Tend.CLI.Commands
  alias Tend.CLI.Invocation
  alias Tend.CLI.Stubs
  alias Tend.Version

  @entry_points [
    {[], :tui},
    {["mcp"], :mcp},
    {["workflow", "run"], :workflow_run},
    {["add"], :add},
    {["ls"], :ls},
    {["log"], :log},
    {["standup"], :standup},
    {["projects"], :projects},
    {["version"], :version},
    {["auth", "jira"], :auth_jira},
    {["agent-hook"], :agent_hook}
  ]

  describe "the four entry-point shapes" do
    test "the table holds exactly the entry points this phase dispatches" do
      assert Enum.sort(Commands.entry_points()) == Enum.sort(@entry_points)
    end

    test "each one dispatches to its own command" do
      for {path, command} <- @entry_points do
        assert {:run, %Invocation{command: ^command, path: ^path, argv: []}} = CLI.plan(path)
      end
    end

    test "each one has a distinct stub of its own" do
      Code.ensure_loaded!(Stubs)

      for {_path, command} <- @entry_points do
        assert function_exported?(Stubs, command, 1),
               "Tend.CLI.Stubs.#{command}/1 is missing"
      end

      stubs = Enum.map(@entry_points, &elem(&1, 1))
      assert Enum.uniq(stubs) == stubs
    end

    test "running each one produces its own output and exits zero" do
      outputs =
        for {path, _command} <- @entry_points do
          capture_io(fn -> assert CLI.run(path ++ ["--db", "/tmp/tend-test.db"]) == 0 end)
        end

      assert Enum.uniq(outputs) == outputs
      refute Enum.any?(outputs, &(&1 == ""))
    end

    test "arguments and flags after the command are left for it to parse" do
      assert {:run, %Invocation{command: :add, argv: ["-p", "work", "write it down"]}} =
               CLI.plan(["add", "-p", "work", "write it down"])

      assert {:run, %Invocation{command: :mcp, argv: ["--task-id", "7"]}} =
               CLI.plan(["mcp", "--task-id", "7"])

      assert {:run, %Invocation{command: :workflow_run, argv: ["12", "--takeover"]}} =
               CLI.plan(["workflow", "run", "12", "--takeover"])
    end

    test "aliases reach the same commands cobra's do" do
      assert {:run, %Invocation{command: :add}} = CLI.plan(["a", "hi"])
      assert {:run, %Invocation{command: :projects}} = CLI.plan(["proj"])
      assert {:run, %Invocation{command: :projects}} = CLI.plan(["project"])
      assert {:run, %Invocation{command: :workflow_run}} = CLI.plan(["wf", "run", "1"])
      assert {:run, %Invocation{command: :workflow_run}} = CLI.plan(["workflows", "run", "1"])
    end
  end

  describe "version" do
    test "prints a version string" do
      output = capture_io(fn -> assert CLI.run(["version"]) == 0 end)

      assert String.trim(output) == Version.string()
      assert Version.string() =~ ~r/^v\d+\.\d+\.\d+/
    end

    test "--version prints it the way the root command does" do
      output = capture_io(fn -> assert CLI.run(["--version"]) == 0 end)

      assert String.trim(output) == "tend version " <> Version.string()
    end
  end

  describe "--db" do
    test "the flag lands on the invocation, before or after the command" do
      assert {:run, %Invocation{db_path: "/tmp/a.db"}} = CLI.plan(["--db", "/tmp/a.db", "ls"])
      assert {:run, %Invocation{db_path: "/tmp/a.db"}} = CLI.plan(["ls", "--db", "/tmp/a.db"])
      assert {:run, %Invocation{db_path: "/tmp/a.db"}} = CLI.plan(["ls", "--db=/tmp/a.db"])
      assert {:run, %Invocation{db_path: "/tmp/a.db"}} = CLI.plan(["--db", "/tmp/a.db"])
    end

    test "it is taken off the argv the command sees" do
      assert {:run, %Invocation{command: :add, argv: ["hi"]}} =
               CLI.plan(["add", "--db", "/tmp/a.db", "hi"])
    end

    test "a missing value is an error, not a silent nil" do
      assert {:error, "flag needs an argument: --db", []} = CLI.plan(["ls", "--db"])
    end

    test "nothing past -- is parsed" do
      assert {:run, %Invocation{command: :add, argv: ["--", "--db", "/tmp/a.db"]}} =
               CLI.plan(["add", "--", "--db", "/tmp/a.db"])
    end

    test "without the flag it falls back through the environment" do
      # The precedence itself is Tend.DBPathTest's; this is the wiring.
      assert {:run, %Invocation{db_path: db_path}} = CLI.plan(["ls"])
      assert db_path == Tend.DBPath.resolve(nil)
    end
  end

  describe "help" do
    test "--help, -h and `help` all print the root help and exit zero" do
      root = Tend.CLI.Help.root()

      for argv <- [["--help"], ["-h"], ["help"]] do
        assert capture_io(fn -> assert CLI.run(argv) == 0 end) == root
      end
    end

    test "a command's --help describes that command" do
      output = capture_io(fn -> assert CLI.run(["workflow", "--help"]) == 0 end)

      assert output =~ "List, start, watch and steer agent workflow runs"
      assert output =~ "Usage:\n  tend workflow [command]"
      assert output =~ "  start       Run a workflow on a task"
    end

    test "a group with no subcommand prints its help" do
      output = capture_io(fn -> assert CLI.run(["auth"]) == 0 end)

      assert output =~ "Manage credentials for external services"
      assert output =~ "  jira        Manage Jira credentials"
    end
  end

  describe "errors" do
    test "an unknown command exits non-zero with a usage message" do
      output =
        capture_io(:stderr, fn ->
          assert CLI.run(["bogus"]) == 1
        end)

      assert output =~ ~s(tend: unknown command "bogus" for "tend")
      assert output =~ "Usage:"
      assert output =~ "Available Commands:"
    end

    test "an unknown subcommand names the command it is unknown to" do
      output = capture_io(:stderr, fn -> assert CLI.run(["auth", "bogus"]) == 1 end)

      assert output =~ ~s(tend: unknown command "bogus" for "tend auth")
    end

    test "an unknown root flag exits non-zero" do
      output = capture_io(:stderr, fn -> assert CLI.run(["--nope"]) == 1 end)

      assert output =~ "tend: unknown flag: --nope"
    end

    test "a command the port has not reached yet says so and exits non-zero" do
      output = capture_io(:stderr, fn -> assert CLI.run(["workflow", "start", "review"]) == 1 end)

      assert output =~ "tend workflow start: not implemented in the Elixir port yet"
    end
  end
end
