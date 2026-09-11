defmodule Subproc.Scenarios do
  @moduledoc """
  The pass/fail scenarios. Each prints `PASS`/`FAIL` lines for assertions and
  `NOTE` lines for observations the README records, and returns the list of
  failures. `fake_*` scenarios need no network; `claude_*` run the real CLI
  on haiku.
  """

  alias Subproc.{Procs, Runner}

  @fake Path.expand("../../test/fake_claude.sh", __DIR__)
  @tmp Path.expand("../../tmp", __DIR__)

  # ---------------------------------------------------------------- helpers

  defp check(cond, label) do
    IO.puts(if(cond, do: "PASS", else: "FAIL") <> "  " <> label)
    if cond, do: [], else: [label]
  end

  defp note(text), do: IO.puts("NOTE  " <> text)

  defp log_path(name) do
    File.mkdir_p!(@tmp)
    Path.join(@tmp, "#{name}-#{System.os_time(:millisecond)}.jsonl")
  end

  defp marker, do: "spike3-#{:erlang.unique_integer([:positive])}"

  # Collect runner events until {:exit, _} or {:closed, _} or `timeout` ms
  # have elapsed in total (a deadline, not a per-message timeout: a live
  # stream would otherwise keep resetting it).
  defp collect(ref, timeout) do
    collect_until(ref, System.monotonic_time(:millisecond) + timeout, [])
  end

  defp collect_until(ref, deadline, acc) do
    left = deadline - System.monotonic_time(:millisecond)

    receive do
      {:runner, ^ref, {:exit, _} = ev} -> Enum.reverse([ev | acc])
      {:runner, ^ref, {:closed, _} = ev} -> Enum.reverse([ev | acc])
      {:runner, ^ref, ev} -> collect_until(ref, deadline, [ev | acc])
    after
      max(left, 0) -> Enum.reverse([:timeout | acc])
    end
  end

  defp lines(events), do: for({:line, _, _, _} = l <- events, do: l)

  defp exit_status(events),
    do:
      Enum.find_value(events, fn
        {:exit, s} -> s
        _ -> nil
      end)

  defp read_jsonl(path) do
    path |> File.read!() |> String.split("\n", trim: true)
  end

  defp claude_env do
    System.get_env()
    |> Enum.filter(fn {k, _} -> String.starts_with?(k, "CLAUDE") end)
    |> Enum.map(fn {k, _} -> {k, nil} end)
  end

  # --------------------------------------------------------------- fake_stream

  @doc "Lines land in the file as they arrive; a 40 KB line survives the 10 KB window intact."
  def fake_stream do
    path = log_path("fake-stream")
    {:ok, r} = Runner.start_link(cmd: @fake, args: ["finite", marker()], log_path: path)
    ref = Runner.ref(r)
    events = collect(ref, 10_000)
    ls = lines(events)

    on_disk = read_jsonl(path)
    decoded = Enum.map(on_disk, &Jason.decode/1)
    big = Enum.find(on_disk, &(byte_size(&1) > 30_000))

    gaps =
      ls
      |> Enum.map(fn {:line, _, _, t} -> t end)
      |> Enum.chunk_every(2, 1, :discard)
      |> Enum.map(fn [a, b] -> b - a end)

    note("arrival times (ms since spawn): #{inspect(Enum.map(ls, fn {:line, _, _, t} -> t end))}")

    note(
      "inter-arrival gaps: #{inspect(gaps)}, line sizes: #{inspect(Enum.map(ls, fn {:line, _, s, _} -> s end))}"
    )

    check(exit_status(events) == 0, "fake child exits 0 and the runner reports it") ++
      check(length(on_disk) == 6, "6 lines on disk (#{length(on_disk)})") ++
      check(Enum.all?(decoded, &match?({:ok, _}, &1)), "every line on disk is valid JSON") ++
      check(
        big != nil and byte_size(big) > 40_000,
        "the 40 KB line is intact (#{byte_size(big || "")} bytes)"
      ) ++
      check(
        Enum.all?(gaps, &(&1 >= 200)),
        "lines arrive as emitted (every gap >= 200 ms of a 300 ms schedule), not batched at exit"
      )
  end

  # -------------------------------------------------------- daemon_truncation

  @doc "MuonTrap.Daemon's line logger, same child: does the 40 KB line survive?"
  def daemon_truncation do
    me = self()

    {:ok, d} =
      MuonTrap.Daemon.start_link(@fake, ["finite", marker()],
        logger_fun: fn line -> send(me, {:daemon_line, line}) end
      )

    mref = Process.monitor(d)

    lines =
      Stream.repeatedly(fn ->
        receive do
          {:daemon_line, l} -> {:line, l}
          {:DOWN, ^mref, _, _, _} -> :down
        after
          10_000 -> :down
        end
      end)
      |> Enum.take_while(&(&1 != :down))
      |> Enum.map(fn {:line, l} -> l end)

    big = Enum.find(lines, &(byte_size(&1) > 1_000))
    sizes = Enum.map(lines, &byte_size/1)
    note("MuonTrap.Daemon delivered #{length(lines)} lines, sizes #{inspect(sizes)}")

    check(
      big == nil or byte_size(big) < 40_000 or match?({:error, _}, Jason.decode(big)),
      "observed: MuonTrap.Daemon corrupts the 40 KB line (256-byte partial buffer), so the Daemon's line splitter is not usable for stream-json"
    )
  end

  # --------------------------------------------------------- fake_stdin_hazard

  @doc "A child that reads stdin: with muontrap's stdin inherited it deadlocks; with </dev/null it flows."
  def fake_stdin_hazard do
    mk = marker()
    path1 = log_path("fake-stdin-inherit")

    {:ok, r1} =
      Runner.start_link(
        cmd: @fake,
        args: ["readstdin", mk],
        log_path: path1,
        stdin: :inherit,
        delay_to_sigkill: 200
      )

    ref1 = Runner.ref(r1)
    ev1 = collect(ref1, 2_000)
    n1 = length(lines(ev1))
    Runner.cancel(r1, :group_term)
    collect(ref1, 2_000)

    path2 = log_path("fake-stdin-devnull")

    {:ok, r2} =
      Runner.start_link(
        cmd: @fake,
        args: ["readstdin", mk],
        log_path: path2,
        stdin: :devnull,
        delay_to_sigkill: 200
      )

    ref2 = Runner.ref(r2)
    ev2 = collect(ref2, 2_000)
    n2 = length(lines(ev2))
    Runner.cancel(r2, :group_term)
    collect(ref2, 2_000)

    note(
      "child reads one line of stdin first: stdin inherited -> #{n1} lines in 2 s; stdin </dev/null -> #{n2} lines in 2 s"
    )

    check(
      n1 == 0,
      "inherited stdin: child blocks on the ack pipe and never emits (the hazard is real)"
    ) ++
      check(n2 >= 4, "stdin from /dev/null: child gets EOF and streams normally")
  end

  # --------------------------------------------------------- fake_cancel(mode)

  @doc "Cancel a running fake child by `mode`; report what is left of its tree."
  def fake_cancel(mode) when mode in [:port_close, :group_term] do
    mk = marker()
    path = log_path("fake-cancel-#{mode}")
    delay = 1_000

    {:ok, r} =
      Runner.start_link(
        cmd: @fake,
        args: ["forever", mk],
        log_path: path,
        delay_to_sigkill: delay
      )

    ref = Runner.ref(r)
    mt = Runner.os_pid(r)

    {:ok, tree} =
      Procs.wait_for(
        fn t ->
          (d = Procs.descendants(mt, t))
          |> Enum.count(&String.contains?(&1.command, mk))
          |> Kernel.>=(2) && d
        end,
        5_000
      )

    child = Procs.child_of(mt)

    note(
      "[#{mode}] tree under muontrap #{mt} (pgid #{child.pgid}) before cancel:\n" <>
        Procs.fmt(tree)
    )

    beam_pgid = Enum.find(Procs.table(), &(&1.pid == String.to_integer(System.pid()))).pgid
    grand_pids = tree |> Enum.reject(&(&1.pid == child.pid)) |> Enum.map(& &1.pid)

    # a few lines have streamed
    _ = collect_n(ref, 2, 3_000)
    t0 = System.monotonic_time(:millisecond)
    :ok = Runner.cancel(r, mode)
    events = collect(ref, delay + 3_000)
    t_end = System.monotonic_time(:millisecond) - t0

    Process.sleep(300)
    left_group = Procs.group(child.pgid)
    left_grand = Enum.filter(Procs.table(), &(&1.pid in grand_pids))
    note("[#{mode}] runner finished #{t_end} ms after cancel with #{inspect(List.last(events))}")

    note(
      "[#{mode}] still in process group after cancel + #{delay} ms grace:\n" <>
        Procs.fmt(left_group)
    )

    Procs.reap(Enum.map(left_group, & &1.pid) ++ grand_pids)

    common =
      check(
        child.pgid == mt and child.pgid != beam_pgid,
        "muontrap is the group leader of its own process group, distinct from the BEAM's (#{beam_pgid})"
      ) ++
        check(
          not Procs.alive?(child.pid),
          "[#{mode}] the child (fake claude, pid #{child.pid}) is dead"
        ) ++
        check(not Procs.alive?(mt), "[#{mode}] muontrap (pid #{mt}) is dead")

    case mode do
      :port_close ->
        # MuonTrap alone: record, do not assert, what happens to grandchildren.
        note(
          "[port_close] grandchildren orphaned by MuonTrap alone on this OS: #{length(left_grand)} of #{length(grand_pids)}"
        )

        common ++
          check(
            exit_status(events) == nil,
            "[port_close] no exit status is reported after Port.close (expected: the port is gone)"
          )

      :group_term ->
        common ++
          check(
            left_group == [],
            "[group_term] no process from the group survives (grandchildren included)"
          ) ++
          check(
            exit_status(events) != nil,
            "[group_term] an exit event still arrives through the port (status #{inspect(exit_status(events))}: muontrap's own 1 for 'interrupted', not the child's 128+signal, since it took the SIGTERM too)"
          ) ++
          check(
            t_end < delay + 1_000,
            "[group_term] runner wound down within the SIGKILL delay (#{t_end} ms)"
          )
    end
  end

  defp collect_n(ref, n, timeout, acc \\ []) do
    if length(acc) >= n do
      Enum.reverse(acc)
    else
      receive do
        {:runner, ^ref, {:line, _, _, _} = l} -> collect_n(ref, n, timeout, [l | acc])
        {:runner, ^ref, _} -> collect_n(ref, n, timeout, acc)
      after
        timeout -> Enum.reverse(acc)
      end
    end
  end

  # ---------------------------------------------------------------- claude_run

  @doc "Real `claude -p` on haiku, to completion."
  def claude_run do
    cwd = Path.join(@tmp, "claude-cwd")
    File.mkdir_p!(cwd)
    path = log_path("claude-run")

    args = [
      "-p",
      "--output-format",
      "stream-json",
      "--verbose",
      "--model",
      "haiku",
      "Reply with exactly the word pong and nothing else."
    ]

    {:ok, r} =
      Runner.start_link(cmd: "claude", args: args, cd: cwd, env: claude_env(), log_path: path)

    ref = Runner.ref(r)
    events = collect(ref, 120_000)
    ls = lines(events)
    on_disk = read_jsonl(path)
    decoded = Enum.map(on_disk, &Jason.decode!/1)
    types = Enum.map(decoded, &{&1["type"], &1["subtype"]})
    result = Enum.find(decoded, &(&1["type"] == "result"))
    sizes = Enum.map(ls, fn {:line, _, s, _} -> s end)
    times = Enum.map(ls, fn {:line, _, _, t} -> t end)

    note("claude stream: #{length(on_disk)} lines, types #{inspect(types)}")
    note("line sizes #{inspect(sizes)}; arrival ms #{inspect(times)}; log #{path}")

    if result,
      do:
        note(
          "result: subtype=#{result["subtype"]} is_error=#{result["is_error"]} text=#{inspect(result["result"])} cost=$#{result["total_cost_usd"]}"
        )

    check(exit_status(events) == 0, "claude exits 0 (#{inspect(exit_status(events))})") ++
      check(
        length(on_disk) >= 3 and Enum.all?(on_disk, &match?({:ok, _}, Jason.decode(&1))),
        "every line on disk is a JSON object"
      ) ++
      check(
        Enum.find_index(types, &(&1 == {"system", "init"})) <
          Enum.find_index(types, &match?({"result", _}, &1)),
        "a system/init event precedes the result (claude 2.1.269 emits system/status first)"
      ) ++
      check(result != nil and result["subtype"] == "success", "last event is result/success") ++
      check(
        List.last(times) - List.first(times) >= 100,
        "init event arrived before the result, not with it (#{List.last(times) - List.first(times)} ms apart): the stream is live"
      ) ++
      check(
        Enum.max(sizes) == Enum.max(Enum.map(on_disk, &byte_size/1)),
        "largest line on disk matches the largest line seen (#{Enum.max(sizes)} bytes; the 10 KB window crossing itself is proven by fake-stream)"
      )
  end

  # ------------------------------------------------------------ claude_cancel

  @doc "Real claude with a tend MCP server and a Bash `tail -f hold.log`; cancel mid-run by `mode`."
  def claude_cancel(mode) when mode in [:port_close, :group_term] do
    cwd = Path.join(@tmp, "claude-cwd")
    File.mkdir_p!(cwd)
    # a file inside cwd: claude blocks tail/cat on paths outside its working dir
    File.write!(Path.join(cwd, "hold.log"), "")
    tend = System.get_env("TEND_GO") || "/tmp/tend-go"
    db = Path.join(@tmp, "spike3.db")
    if not File.exists?(db), do: System.cmd(tend, ["--db", db, "add", "spike 3 mcp target"])
    mcp = Path.join(@tmp, "mcp.json")

    File.write!(
      mcp,
      Jason.encode!(%{
        mcpServers: %{tend: %{command: tend, args: ["mcp", "--task-id", "1", "--db", db]}}
      })
    )

    path = log_path("claude-cancel-#{mode}")
    delay = 5_000

    # --allowedTools is variadic and swallows everything up to the next flag,
    # so it cannot sit right before the positional prompt.
    args = [
      "-p",
      "--output-format",
      "stream-json",
      "--verbose",
      "--allowedTools",
      "Bash(tail:*)",
      "--mcp-config",
      mcp,
      "--model",
      "haiku",
      "Use the Bash tool to run exactly this command and nothing else: tail -f hold.log . It blocks on purpose; let it run. Do not explain, do not run anything else."
    ]

    {:ok, r} =
      Runner.start_link(
        cmd: "claude",
        args: args,
        cd: cwd,
        env: claude_env(),
        log_path: path,
        delay_to_sigkill: delay
      )

    ref = Runner.ref(r)
    mt = Runner.os_pid(r)

    found =
      Procs.wait_for(
        fn t ->
          d = Procs.descendants(mt, t)

          # exact argv of the sleep binary: claude's own argv also contains
          # the words "tail -f hold.log" (they are in the prompt)
          (Enum.any?(d, &(&1.command == "tail -f hold.log")) and
             Enum.any?(d, &String.contains?(&1.command, "mcp --task-id"))) && d
        end,
        90_000,
        250
      )

    tree =
      case found,
        do: (
          {:ok, d} -> d
          :timeout -> Procs.descendants(mt)
        )

    child = Procs.child_of(mt)

    note(
      "[#{mode}] tree under muontrap #{mt} at cancel time (#{length(tree)} procs):\n" <>
        Procs.fmt(tree)
    )

    grand_pids = tree |> Enum.map(& &1.pid)
    pgid = if child, do: child.pgid, else: mt

    if not Process.alive?(r) do
      note("[#{mode}] claude finished before the cancel; see the log for what it did: #{path}")
    end

    t0 = System.monotonic_time(:millisecond)
    :ok = if Process.alive?(r), do: Runner.cancel(r, mode), else: :ok
    events = collect(ref, delay + 5_000)
    t_end = System.monotonic_time(:millisecond) - t0
    # With :port_close the port is gone at once, but muontrap is still inside
    # its SIGTERM grace; judge survivors only once muontrap itself has exited.
    mt_gone = Procs.wait_for(fn _ -> not Procs.alive?(mt) end, delay + 2_000)
    t_mt = System.monotonic_time(:millisecond) - t0
    Process.sleep(300)

    left_group = Procs.group(pgid)
    left_tree = Enum.filter(Procs.table(), &(&1.pid in grand_pids))

    note(
      "[#{mode}] runner finished #{t_end} ms after cancel with #{inspect(List.last(events))}; #{length(lines(events))} lines streamed; log #{path}"
    )

    note("[#{mode}] muontrap exited #{t_mt} ms after cancel (#{inspect(mt_gone)})")

    note(
      "[#{mode}] survivors in the process group after cancel + grace:\n" <> Procs.fmt(left_group)
    )

    note("[#{mode}] survivors from the pre-cancel tree:\n" <> Procs.fmt(left_tree))
    Procs.reap(Enum.map(left_group, & &1.pid) ++ grand_pids)

    base =
      check(
        match?({:ok, _}, found),
        "[#{mode}] claude spawned both an MCP server (tend mcp) and a Bash shell running tail -f hold.log"
      ) ++
        check(
          child == nil or not Procs.alive?(child.pid),
          "[#{mode}] claude is dead after cancel"
        ) ++
        check(not Procs.alive?(mt), "[#{mode}] muontrap is dead after cancel")

    case mode do
      :port_close ->
        note(
          "[port_close] grandchildren left by MuonTrap alone: #{length(left_tree)} of #{length(grand_pids)}"
        )

        base

      :group_term ->
        base ++
          check(
            left_group == [] and left_tree == [],
            "[group_term] no MCP server, shell, or sleep survives the cancel"
          ) ++
          check(
            t_end < delay + 1_500,
            "[group_term] wound down within the SIGKILL delay (#{t_end} ms)"
          )
    end
  end

  # ----------------------------------------------------------------------- hold

  @doc "For test/drive.sh: run the fake forever, write pids to `out`, block. The driver kills this BEAM with SIGKILL."
  def hold(out) do
    {:ok, r} =
      Runner.start_link(
        cmd: @fake,
        args: ["forever", marker()],
        log_path: log_path("hold"),
        delay_to_sigkill: 1_000
      )

    mt = Runner.os_pid(r)
    {:ok, child} = Procs.wait_for(fn t -> Procs.child_of(mt, t) end, 5_000)

    {:ok, grand} =
      Procs.wait_for(
        fn t -> (d = Procs.descendants(child.pid, t)) |> length() |> Kernel.>=(2) && d end,
        5_000
      )

    File.write!(
      out,
      Enum.map_join([mt, child.pid | Enum.map(grand, & &1.pid)], " ", &to_string/1) <> "\n"
    )

    IO.puts(
      "holding: muontrap=#{mt} child=#{child.pid} grandchildren=#{inspect(Enum.map(grand, & &1.pid))}"
    )

    Process.sleep(:infinity)
  end
end
