defmodule Subproc do
  @moduledoc """
  Spike 3 entry point: `mix run -e 'Subproc.main(System.argv())' -- <scenario>`.

  Scenarios (see `Subproc.Scenarios`):

      fake            stream, daemon-truncation, stdin hazard, cancel x2 (no network)
      claude-run      real claude -p on haiku to completion
      claude-cancel   real claude with MCP server + Bash sleep, cancelled by group_term
      claude-cancel-port-close   same, cancelled by MuonTrap's Port.close alone
      hold <file>     block forever running the fake child; used by test/drive.sh
      all             fake + claude-run + claude-cancel
  """

  alias Subproc.Scenarios

  def main(argv) do
    failures =
      case argv do
        ["fake"] ->
          fake()

        ["claude-run"] ->
          section("claude-run", &Scenarios.claude_run/0)

        ["claude-cancel"] ->
          section("claude-cancel group_term", fn -> Scenarios.claude_cancel(:group_term) end)

        ["claude-cancel-port-close"] ->
          section("claude-cancel port_close", fn -> Scenarios.claude_cancel(:port_close) end)

        ["hold", out] ->
          Scenarios.hold(out)

        ["all"] ->
          fake() ++
            section("claude-run", &Scenarios.claude_run/0) ++
            section("claude-cancel group_term", fn -> Scenarios.claude_cancel(:group_term) end)

        _ ->
          IO.puts(@moduledoc) && System.halt(2)
      end

    IO.puts("")

    case failures do
      [] ->
        IO.puts("ALL PASS")
        System.halt(0)

      fs ->
        IO.puts("#{length(fs)} FAILED:")
        Enum.each(fs, &IO.puts("  - " <> &1))
        System.halt(1)
    end
  end

  defp fake do
    section("fake-stream", &Scenarios.fake_stream/0) ++
      section("daemon-truncation", &Scenarios.daemon_truncation/0) ++
      section("fake-stdin-hazard", &Scenarios.fake_stdin_hazard/0) ++
      section("fake-cancel port_close", fn -> Scenarios.fake_cancel(:port_close) end) ++
      section("fake-cancel group_term", fn -> Scenarios.fake_cancel(:group_term) end)
  end

  defp section(name, fun) do
    IO.puts("\n== #{name}")
    fun.()
  end
end
