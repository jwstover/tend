defmodule Tend.Task.SessionTest do
  use ExUnit.Case, async: true

  alias Tend.Task.Session
  alias Tend.Task.SessionStatus
  alias Tend.Task.TaskSession

  describe "the struct" do
    test "a bare session defaults to Go's zero values" do
      assert %Session{} == %Session{
               id: 0,
               task_id: 0,
               external_id: "",
               cwd: "",
               label: "",
               tmux_session: "",
               needs_recap: false,
               status: nil,
               status_updated_at: nil,
               started_at: nil,
               last_active_at: nil,
               step_run_id: nil
             }
    end
  end

  describe "headless?/1" do
    test "a step run with no tmux session is headless" do
      assert Session.headless?(%Session{step_run_id: 7, tmux_session: ""})
    end

    test "a step run in a tmux session of its own is not" do
      refute Session.headless?(%Session{step_run_id: 7, tmux_session: "tend-7"})
    end

    test "an ordinary session is not, with or without tmux" do
      refute Session.headless?(%Session{step_run_id: nil, tmux_session: ""})
      refute Session.headless?(%Session{step_run_id: nil, tmux_session: "tend-1"})
    end
  end

  describe "TaskSession" do
    test "carries the task's title and state alongside the session" do
      ts = %TaskSession{
        session: %Session{external_id: "abc"},
        task_title: "buy milk",
        task_state: :doing
      }

      assert ts.session.external_id == "abc"
      assert ts.task_title == "buy milk"
      assert ts.task_state == :doing
    end
  end

  describe "SessionStatus" do
    test "all/0 is the statuses the Go constants declare, in order" do
      assert SessionStatus.all() == [:unknown, :starting, :working, :idle, :blocked, :ended]
    end

    test "every status round trips through its stored name" do
      for status <- SessionStatus.all() do
        assert SessionStatus.parse(SessionStatus.format(status)) == status
      end
    end

    test "the stored names are the Go constants' strings" do
      assert Enum.map(SessionStatus.all(), &SessionStatus.format/1) ==
               ~w(unknown starting working idle blocked ended)
    end

    test "a status tend does not recognize reads as unknown rather than failing" do
      for name <- ["", "weird", "from-the-future", "IDLE", "in progress"] do
        assert SessionStatus.parse(name) == :unknown,
               "SessionStatus.parse(#{inspect(name)}) should be :unknown"
      end
    end

    test "valid?/1 accepts only the six statuses" do
      for status <- SessionStatus.all(), do: assert(SessionStatus.valid?(status))
      refute SessionStatus.valid?(:weird)
      refute SessionStatus.valid?("idle")
      refute SessionStatus.valid?(nil)
    end

    test "format/1 refuses a status that does not exist" do
      assert_raise FunctionClauseError, fn -> SessionStatus.format(:weird) end
    end
  end
end
